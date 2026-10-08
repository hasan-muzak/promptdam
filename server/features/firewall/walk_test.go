package firewall

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// A document nested past maxDepth is refused before it costs memory: a
// provider's answer of 16 MiB of brackets is left unread.
func TestRewriteStopsAtMaxDepth(t *testing.T) {
	deep := []byte(strings.Repeat("[", 16<<20))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, err := rewrite(deep, func(*Walker, string) (string, bool) { return "", false })
	runtime.ReadMemStats(&after)
	if !errors.Is(err, errMalformed) {
		t.Fatalf("deep: %v", err)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
		t.Fatalf("allocated %d MiB", grown>>20)
	}
	// Up to the limit, documents are read.
	ok := []byte(strings.Repeat("[", maxDepth) + `"x"` + strings.Repeat("]", maxDepth))
	if _, _, err := rewrite(ok, func(*Walker, string) (string, bool) { return "", false }); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
}
