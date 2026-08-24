package main

import (
	"testing"
	"time"
)

func TestSystemSamplerStatus(t *testing.T) {
	s := newSystemSampler()
	r := NewRelay(&Store{db: &DB{}}, nil)
	status := s.Status("", r, nowForTest())
	if _, ok := status["interfaces"]; !ok {
		t.Fatal("missing interfaces")
	}
	if status["cpu_cores"].(int) < 1 {
		t.Fatal("invalid CPU core count")
	}
}

func nowForTest() (t time.Time) { return time.Now().Add(-time.Second) }
