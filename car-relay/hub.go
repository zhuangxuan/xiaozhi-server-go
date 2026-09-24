package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type client struct {
	conn *websocket.Conn
	send chan []byte
	once sync.Once
}

func newClient(conn *websocket.Conn) *client {
	return &client{conn: conn, send: make(chan []byte, sendQueueSize)}
}

func (c *client) closeSend() {
	c.once.Do(func() { close(c.send) })
}

func (c *client) writeLoop() {
	defer c.conn.Close()
	for msg := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

type phoneSession struct {
	client   *client
	deviceID string
	driver   bool
}

type carSession struct {
	client   *client
	deviceID string
	lastSeq  int64
	battery  int
	fault    string
}

type Hub struct {
	store *Store
	mu    sync.Mutex
	cars  map[string]*carSession
	phones map[*client]*phoneSession
	up    websocket.Upgrader
	now   func() time.Time
	lastDropLog time.Time
}

func (h *Hub) AddDemoCar(deviceID string) {
	c := &client{send: make(chan []byte, sendQueueSize)}
	go func() {
		for msg := range c.send {
			log.Printf("demo car got %s", msg)
		}
	}()
	h.mu.Lock()
	h.cars[deviceID] = &carSession{client: c, deviceID: deviceID, battery: 80}
	h.mu.Unlock()
}

func NewHub(store *Store) *Hub {
	return &Hub{
		store:  store,
		cars:   map[string]*carSession{},
		phones: map[*client]*phoneSession{},
		up: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		now: time.Now,
	}
}

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/ws/phone", h.servePhone)
	mux.HandleFunc("/ws/car", h.serveCar)
	return mux
}

func (h *Hub) servePhone(w http.ResponseWriter, r *http.Request) {
	conn, err := h.up.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("phone upgrade: %v", err)
		return
	}
	c := newClient(conn)
	h.mu.Lock()
	h.phones[c] = &phoneSession{client: c}
	h.mu.Unlock()
	log.Printf("phone connected from %s", r.RemoteAddr)
	go c.writeLoop()
	h.readLoop(c, true)
}

func (h *Hub) serveCar(w http.ResponseWriter, r *http.Request) {
	conn, err := h.up.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("car upgrade: %v", err)
		return
	}
	c := newClient(conn)
	log.Printf("car socket connected")
	go c.writeLoop()
	h.readLoop(c, false)
}

func (h *Hub) readLoop(c *client, isPhone bool) {
	defer h.detach(c, isPhone)
	c.conn.SetReadLimit(maxMessageBytes)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg inbound
		if err := json.Unmarshal(data, &msg); err != nil {
			h.enqueue(c, errorMsg{Type: "error", Message: "消息不是 JSON"})
			continue
		}
		if isPhone {
			h.onPhone(c, msg)
		} else {
			h.onCar(c, msg)
		}
	}
}

func (h *Hub) onPhone(c *client, msg inbound) {
	switch msg.Type {
	case "hello":
		h.enqueue(c, helloAck{Type: "hello", OK: true})
	case "pair":
		h.pairPhone(c, msg.Code)
	case "ping":
		h.enqueue(c, pongMsg{Type: "pong", Ts: msg.Ts})
	case "drive":
		h.forwardDrive(c, msg, false)
	case "estop":
		h.forwardDrive(c, inbound{
			Type: "estop", Seq: msg.Seq, Ts: msg.Ts, Throttle: 0, Steer: 0, TTLMS: 300,
		}, true)
	default:
		h.enqueue(c, errorMsg{Type: "error", Message: "未知消息"})
	}
}

func (h *Hub) onCar(c *client, msg inbound) {
	switch msg.Type {
	case "hello":
		h.registerCar(c, msg.DeviceID)
	case "heartbeat", "status":
		h.carHeartbeat(c, msg)
	default:
		h.enqueue(c, errorMsg{Type: "error", Message: "未知消息"})
	}
}

func (h *Hub) registerCar(c *client, deviceID string) {
	if deviceID == "" {
		h.enqueue(c, errorMsg{Type: "error", Message: "缺少 deviceId"})
		return
	}
	code, err := h.store.IssuePairCode(deviceID, h.now())
	if err != nil {
		log.Printf("issue pair code: %v", err)
		h.enqueue(c, errorMsg{Type: "error", Message: "配对码生成失败"})
		return
	}
	h.mu.Lock()
	prev := h.cars[deviceID]
	battery := -1
	fault := ""
	var lastSeq int64
	if prev != nil {
		battery = prev.battery
		fault = prev.fault
		lastSeq = prev.lastSeq
		if prev.client != c {
			prev.client.closeSend()
		}
	}
	h.cars[deviceID] = &carSession{
		client: c, deviceID: deviceID, lastSeq: lastSeq, battery: battery, fault: fault,
	}
	h.mu.Unlock()
	log.Printf("car online device=%s pair=%s", deviceID, code)
	h.enqueue(c, helloAck{Type: "hello", OK: true, PairCode: code})
	h.pushStatus(deviceID)
}

func (h *Hub) carHeartbeat(c *client, msg inbound) {
	h.mu.Lock()
	deviceID := ""
	for id, car := range h.cars {
		if car.client != c {
			continue
		}
		if msg.Battery != nil {
			car.battery = *msg.Battery
		}
		car.fault = msg.Fault
		deviceID = id
		break
	}
	h.mu.Unlock()
	if deviceID != "" {
		h.pushStatus(deviceID)
	}
}

func (h *Hub) pairPhone(c *client, code string) {
	deviceID, ok, err := h.store.DeviceByCode(code, h.now())
	if err != nil {
		log.Printf("pair lookup: %v", err)
		h.enqueue(c, pairAck{Type: "pair", OK: false, Message: "配对失败"})
		return
	}
	if !ok {
		h.enqueue(c, pairAck{Type: "pair", OK: false, Message: "配对码无效"})
		return
	}
	h.mu.Lock()
	for _, other := range h.phones {
		if other.client != c && other.driver && other.deviceID == deviceID {
			h.mu.Unlock()
			h.enqueue(c, pairAck{Type: "pair", OK: false, Message: "车已被占用"})
			return
		}
	}
	session := h.phones[c]
	if session == nil {
		h.mu.Unlock()
		return
	}
	session.deviceID = deviceID
	session.driver = true
	h.mu.Unlock()
	log.Printf("phone paired device=%s", deviceID)
	h.enqueue(c, pairAck{Type: "pair", OK: true})
	h.pushStatus(deviceID)
}

func (h *Hub) forwardDrive(c *client, msg inbound, estop bool) {
	now := h.now()
	if expired(msg.Ts, msg.TTLMS, now) {
		h.logDrop(msg.Seq, "expired")
		return
	}
	h.mu.Lock()
	session := h.phones[c]
	if session == nil || !session.driver || session.deviceID == "" {
		h.mu.Unlock()
		return
	}
	car := h.cars[session.deviceID]
	if car == nil {
		h.mu.Unlock()
		h.pushStatus(session.deviceID)
		return
	}
	if msg.Seq <= car.lastSeq {
		seq := msg.Seq
		h.mu.Unlock()
		h.logDrop(seq, "stale")
		return
	}
	car.lastSeq = msg.Seq
	msg.Throttle = clampAxis(msg.Throttle)
	msg.Steer = clampAxis(msg.Steer)
	log.Printf("recv drive seq=%d throttle=%d steer=%d", msg.Seq, msg.Throttle, msg.Steer)
	payload, err := json.Marshal(driveMsg{
		Type: msg.Type, Seq: msg.Seq, Ts: msg.Ts,
		Throttle: msg.Throttle, Steer: msg.Steer, TTLMS: msg.TTLMS,
	})
	if estop {
		payload, err = json.Marshal(struct {
			Type string `json:"type"`
			Seq  int64  `json:"seq"`
			Ts   int64  `json:"ts"`
		}{Type: "estop", Seq: msg.Seq, Ts: msg.Ts})
	}
	target := car.client
	h.mu.Unlock()
	if err != nil {
		return
	}
	trySend(target.send, payload)
}

func (h *Hub) pushStatus(deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	car := h.cars[deviceID]
	online := car != nil
	battery := -1
	fault := ""
	if car != nil {
		battery = car.battery
		fault = car.fault
	}
	body, err := json.Marshal(statusMsg{
		Type: "status", CarOnline: online, Battery: battery, Fault: fault,
	})
	if err != nil {
		return
	}
	for _, phone := range h.phones {
		if phone.deviceID == deviceID && phone.driver {
			trySend(phone.client.send, body)
		}
	}
}

func (h *Hub) detach(c *client, isPhone bool) {
	if isPhone {
		h.detachPhone(c)
		return
	}
	h.detachCar(c)
}

func (h *Hub) detachPhone(c *client) {
	h.mu.Lock()
	session := h.phones[c]
	delete(h.phones, c)
	var car *carSession
	if session != nil && session.driver {
		car = h.cars[session.deviceID]
		if car != nil {
			car.lastSeq++
			seq := car.lastSeq
			body, err := json.Marshal(driveMsg{
				Type: "drive", Seq: seq, Ts: h.now().UnixMilli(),
				Throttle: 0, Steer: 0, TTLMS: 300,
			})
			h.mu.Unlock()
			if err == nil && car.client != nil {
				trySend(car.client.send, body)
			}
			log.Printf("phone disconnected, stop sent device=%s", session.deviceID)
			c.closeSend()
			return
		}
	}
	h.mu.Unlock()
	log.Printf("phone disconnected")
	c.closeSend()
}

func (h *Hub) detachCar(c *client) {
	h.mu.Lock()
	deviceID := ""
	for id, car := range h.cars {
		if car.client == c {
			deviceID = id
			delete(h.cars, id)
			break
		}
	}
	h.mu.Unlock()
	c.closeSend()
	if deviceID != "" {
		log.Printf("car offline device=%s", deviceID)
		h.pushStatus(deviceID)
	}
}

func (h *Hub) enqueue(c *client, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	trySend(c.send, body)
}

func (h *Hub) logDrop(seq int64, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if now.Sub(h.lastDropLog) < time.Second {
		return
	}
	h.lastDropLog = now
	log.Printf("drop drive seq=%d reason=%s", seq, reason)
}

func trySend(ch chan []byte, msg []byte) {
	select {
	case ch <- msg:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- msg:
	default:
	}
}
