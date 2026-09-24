package main

import "time"

const (
	clockSkewMS     = int64(2000)
	maxMessageBytes = 4096
	sendQueueSize   = 32
	pairCodeTTL     = 10 * time.Minute
)

type inbound struct {
	Type     string `json:"type"`
	Role     string `json:"role"`
	Code     string `json:"code"`
	DeviceID string `json:"deviceId"`
	Seq      int64  `json:"seq"`
	Ts       int64  `json:"ts"`
	Throttle int    `json:"throttle"`
	Steer    int    `json:"steer"`
	TTLMS    int64  `json:"ttl_ms"`
	Battery  *int   `json:"battery"`
	Fault    string `json:"fault"`
}

type helloAck struct {
	Type     string `json:"type"`
	OK       bool   `json:"ok"`
	PairCode string `json:"pairCode,omitempty"`
}

type pairAck struct {
	Type    string `json:"type"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type statusMsg struct {
	Type      string `json:"type"`
	CarOnline bool   `json:"carOnline"`
	Battery   int    `json:"battery"`
	Fault     string `json:"fault,omitempty"`
}

type pongMsg struct {
	Type string `json:"type"`
	Ts   int64  `json:"ts"`
}

type errorMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type driveMsg struct {
	Type     string `json:"type"`
	Seq      int64  `json:"seq"`
	Ts       int64  `json:"ts"`
	Throttle int    `json:"throttle"`
	Steer    int    `json:"steer"`
	TTLMS    int64  `json:"ttl_ms"`
}

func expired(ts, ttl int64, now time.Time) bool {
	if ttl <= 0 || ts <= 0 {
		return false
	}
	age := now.UnixMilli() - ts
	if age < 0 {
		return false
	}
	return age > ttl+clockSkewMS
}

func clampAxis(v int) int {
	if v > 100 {
		return 100
	}
	if v < -100 {
		return -100
	}
	return v
}
