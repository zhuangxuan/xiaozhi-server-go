package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":8090", "监听地址")
	dbPath := flag.String("db", "car-relay.db", "SQLite 文件，不与语音服务的 config.db 混用")
	demoCode := flag.String("demo-code", "", "固定配对码，并挂一辆虚拟车，供局域网联调")
	flag.Parse()

	store, err := OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	hub := NewHub(store)
	if *demoCode != "" {
		if err := store.SetPairCode("demo-car", *demoCode, time.Now().Add(24*time.Hour)); err != nil {
			log.Fatalf("demo car: %v", err)
		}
		hub.AddDemoCar("demo-car")
		log.Printf("demo car online pair=%s", *demoCode)
	}
	log.Printf("car-relay listening on %s", *addr)
	if err := http.ListenAndServe(*addr, hub.Handler()); err != nil {
		log.Fatal(err)
	}
}
