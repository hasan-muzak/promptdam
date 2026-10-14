package decide

import (
	"testing"
	"time"
)

// The auto timeout waits 2 s until a model is measured, then a quarter
// more than its slow answers take, within 200 ms and 3 s; a wait too short
// for the model grows, and one past its slow answers shrinks back.
func TestAutoTimeout(t *testing.T) {
	l := latencies{byID: map[string]*ring{}}
	if got := l.timeout("haiku"); got != autoFirst {
		t.Fatalf("unmeasured: %s", got)
	}
	for i := range autoKnown - 1 {
		l.add("haiku", time.Duration(700+10*i)*time.Millisecond)
	}
	if got := l.timeout("haiku"); got != autoFirst {
		t.Fatalf("19 answers: %s", got)
	}
	l.add("haiku", 1600*time.Millisecond) // the slowest of 20 is the 99th percentile
	if got := l.timeout("haiku"); got != 2*time.Second {
		t.Fatalf("measured: %s, want 1.25 × 1.6 s", got)
	}

	// A fast model gets the floor, a slow one the ceiling.
	for range autoKnown {
		l.add("groq", 60*time.Millisecond)
		l.add("slow", 5*time.Second)
	}
	if got := l.timeout("groq"); got != autoMin {
		t.Fatalf("fast: %s", got)
	}
	if got := l.timeout("slow"); got != autoMax {
		t.Fatalf("slow: %s", got)
	}

	// Questions timing out at 1 s count as taking 1 s: the wait grows.
	for range autoKnown {
		l.add("stalls", time.Second)
	}
	if got := l.timeout("stalls"); got != 1250*time.Millisecond {
		t.Fatalf("after timeouts: %s", got)
	}
	// Once the slow ones are older than the window, it shrinks back.
	for range autoSamples {
		l.add("stalls", 400*time.Millisecond)
	}
	if got := l.timeout("stalls"); got != 500*time.Millisecond {
		t.Fatalf("after fast answers: %s", got)
	}
}

// A gateway tracks a bounded number of models.
func TestAutoTimeoutIsBounded(t *testing.T) {
	l := latencies{byID: map[string]*ring{}}
	for i := range maxTracked + 1 {
		l.add(string(rune(i)), time.Millisecond)
	}
	if len(l.byID) > maxTracked {
		t.Fatalf("%d models tracked", len(l.byID))
	}
}
