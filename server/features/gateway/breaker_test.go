package gateway

import (
	"testing"
	"time"
)

func TestBreakerOpensAndProbes(t *testing.T) {
	bs := newBreakers()
	now := time.Now()
	for range breakerFailures - 1 {
		bs.record("p", false, now)
	}
	if !bs.allow("p", now) {
		t.Fatal("open before enough failures")
	}
	bs.record("p", false, now)
	if bs.allow("p", now.Add(breakerOpen-time.Second)) {
		t.Fatal("closed while open")
	}
	later := now.Add(breakerOpen + time.Second)
	if !bs.allow("p", later) {
		t.Fatal("no probe after the open period")
	}
	if bs.allow("p", later) {
		t.Fatal("a second call while the probe runs")
	}
	// A failed probe opens it again; a good one closes it.
	bs.record("p", false, later)
	if bs.allow("p", later.Add(time.Second)) {
		t.Fatal("a failed probe left it closed")
	}
	again := later.Add(breakerOpen + time.Second)
	if !bs.allow("p", again) {
		t.Fatal("no second probe")
	}
	bs.record("p", true, again)
	for range 3 {
		if !bs.allow("p", again) {
			t.Fatal("a good probe didn't close it")
		}
	}
	if !bs.allow("", now) {
		t.Fatal("targets without an ID have no circuit")
	}
}
