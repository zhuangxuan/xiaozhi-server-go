package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	store, err := OpenStore(t.TempDir() + "/car.db")
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store)
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(srv.Close)
	return hub, srv
}

func dial(t *testing.T, httpURL, path string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(httpURL, "http") + path
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readJSON(t *testing.T, conn *websocket.Conn, dest any) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := conn.ReadJSON(dest); err != nil {
		t.Fatal(err)
	}
}

func TestPairRejectsUnknownCode(t *testing.T) {
	_, srv := testHub(t)
	phone := dial(t, srv.URL, "/ws/phone")
	if err := phone.WriteJSON(inbound{Type: "hello", Role: "phone"}); err != nil {
		t.Fatal(err)
	}
	var hello helloAck
	readJSON(t, phone, &hello)
	if !hello.OK {
		t.Fatalf("hello ok=%v", hello.OK)
	}
	if err := phone.WriteJSON(inbound{Type: "pair", Code: "000000"}); err != nil {
		t.Fatal(err)
	}
	var pair pairAck
	readJSON(t, phone, &pair)
	if pair.OK {
		t.Fatal("unknown code should fail")
	}
}

func TestDriveForwardAndStaleDrop(t *testing.T) {
	_, srv := testHub(t)
	car := dial(t, srv.URL, "/ws/car")
	if err := car.WriteJSON(inbound{Type: "hello", Role: "car", DeviceID: "AA:BB:CC:DD:EE:FF"}); err != nil {
		t.Fatal(err)
	}
	var carHello helloAck
	readJSON(t, car, &carHello)
	if carHello.PairCode == "" {
		t.Fatal("missing pair code")
	}

	phone := dial(t, srv.URL, "/ws/phone")
	_ = phone.WriteJSON(inbound{Type: "hello", Role: "phone"})
	var phoneHello helloAck
	readJSON(t, phone, &phoneHello)
	_ = phone.WriteJSON(inbound{Type: "pair", Code: carHello.PairCode})
	var pair pairAck
	readJSON(t, phone, &pair)
	if !pair.OK {
		t.Fatalf("pair failed: %s", pair.Message)
	}
	var status statusMsg
	readJSON(t, phone, &status)
	if !status.CarOnline {
		t.Fatal("car should be online")
	}

	now := time.Now().UnixMilli()
	_ = phone.WriteJSON(inbound{Type: "drive", Seq: 2, Ts: now, Throttle: 40, Steer: -10, TTLMS: 300})
	_ = phone.WriteJSON(inbound{Type: "drive", Seq: 1, Ts: now, Throttle: 90, Steer: 0, TTLMS: 300})

	var first driveMsg
	readJSON(t, car, &first)
	if first.Seq != 2 || first.Throttle != 40 || first.Steer != -10 {
		t.Fatalf("forwarded %+v", first)
	}
	_ = car.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var second driveMsg
	if err := car.ReadJSON(&second); err == nil {
		t.Fatalf("stale seq should be dropped, got %+v", second)
	}
}

func TestSecondPhoneRejected(t *testing.T) {
	_, srv := testHub(t)
	car := dial(t, srv.URL, "/ws/car")
	_ = car.WriteJSON(inbound{Type: "hello", Role: "car", DeviceID: "car-1"})
	var carHello helloAck
	readJSON(t, car, &carHello)

	first := dial(t, srv.URL, "/ws/phone")
	_ = first.WriteJSON(inbound{Type: "pair", Code: carHello.PairCode})
	var okAck pairAck
	readJSON(t, first, &okAck)
	if !okAck.OK {
		t.Fatal(okAck.Message)
	}

	second := dial(t, srv.URL, "/ws/phone")
	_ = second.WriteJSON(inbound{Type: "pair", Code: carHello.PairCode})
	var denied pairAck
	readJSON(t, second, &denied)
	if denied.OK || denied.Message != "车已被占用" {
		t.Fatalf("got %+v", denied)
	}
}

func TestPhoneDisconnectSendsStop(t *testing.T) {
	_, srv := testHub(t)
	car := dial(t, srv.URL, "/ws/car")
	_ = car.WriteJSON(inbound{Type: "hello", Role: "car", DeviceID: "car-2"})
	var carHello helloAck
	readJSON(t, car, &carHello)

	phone := dial(t, srv.URL, "/ws/phone")
	_ = phone.WriteJSON(inbound{Type: "pair", Code: carHello.PairCode})
	var pair pairAck
	readJSON(t, phone, &pair)
	var status statusMsg
	readJSON(t, phone, &status)
	_ = phone.WriteJSON(inbound{Type: "drive", Seq: 5, Ts: time.Now().UnixMilli(), Throttle: 20, Steer: 0, TTLMS: 3000})
	var drive driveMsg
	readJSON(t, car, &drive)

	phone.Close()
	var stop driveMsg
	readJSON(t, car, &stop)
	if stop.Throttle != 0 || stop.Steer != 0 || stop.Seq <= drive.Seq {
		t.Fatalf("stop %+v after %d", stop, drive.Seq)
	}
}

func TestPingPong(t *testing.T) {
	_, srv := testHub(t)
	phone := dial(t, srv.URL, "/ws/phone")
	_ = phone.WriteJSON(inbound{Type: "ping", Ts: 42})
	var pong pongMsg
	readJSON(t, phone, &pong)
	if pong.Type != "pong" || pong.Ts != 42 {
		t.Fatalf("%+v", pong)
	}
}
