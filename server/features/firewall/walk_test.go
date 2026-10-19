package firewall

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"runtime"
	"strconv"
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

// One read and one splice write what rewriting value by value writes, byte
// for byte, whatever the document holds.
func TestSpliceIsRewrite(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pieces := []string{"a", "x", "é", "⟦", `\"`, `\\`, `\n`, `\u00e9`, " ", "日本"}
	str := func() string {
		var b strings.Builder
		b.WriteByte('"')
		for range rng.IntN(6) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		b.WriteByte('"')
		return b.String()
	}
	var value func(depth int) string
	value = func(depth int) string {
		switch k := rng.IntN(5); {
		case depth > 3 || k == 0:
			return str()
		case k == 1:
			return strconv.Itoa(rng.IntN(1000))
		case k == 2:
			var parts []string
			for range rng.IntN(4) {
				parts = append(parts, value(depth+1))
			}
			return "[" + strings.Join(parts, ",") + "]"
		default:
			var parts []string
			for range rng.IntN(4) {
				parts = append(parts, str()+":"+value(depth+1))
			}
			return "{" + strings.Join(parts, ", ") + "}"
		}
	}
	upper := func(s string) (string, bool) {
		if strings.Contains(s, "x") {
			return strings.ToUpper(s), true
		}
		return "", false
	}
	for range 2000 {
		doc := []byte(value(0))
		want, _, err := rewrite(doc, func(_ *Walker, s string) (string, bool) { return upper(s) })
		ls, err2 := leaves(doc)
		if (err == nil) != (err2 == nil) {
			t.Fatalf("%s: %v %v", doc, err, err2)
		}
		if err != nil {
			continue
		}
		repl := map[int]string{}
		for i, l := range ls {
			if r, ok := upper(l.S); ok {
				repl[i] = r
			}
		}
		if got := splice(doc, ls, repl); !bytes.Equal(got, want) {
			t.Fatalf("%s:\n%s\n%s", doc, got, want)
		}
	}
}
