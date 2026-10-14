package classifier

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

// judged makes n judged calls of one family: counting and listing hold up
// on the cheap tier, proving and judging legality don't, whatever their
// length; a few verdicts go the other way (the judge errs).
func judged(n int, seed uint64) ([]Sample, []Sample) {
	rng := rand.New(rand.NewPCG(seed, 1))
	easy := []string{"How many times is the word %s used?", "List every %s in the text.", "Extract the %s from this invoice."}
	hard := []string{"Why is the %s clause unenforceable?", "Prove that the %s claim holds.", "Is this %s contract legal in Germany?"}
	nouns := []string{"revenue", "date", "total", "party", "warranty", "term", "price", "address"}
	var down, up []Sample
	for range n {
		ok := rng.IntN(2) == 0
		tmpl := easy[rng.IntN(len(easy))]
		if !ok {
			tmpl = hard[rng.IntN(len(hard))]
		}
		text := fmt.Sprintf(tmpl, nouns[rng.IntN(len(nouns))])
		for range rng.IntN(40) {
			text += " filler"
		}
		fs := Features(Input{Ask: text, System: "You read contracts.", Family: "f", Messages: 1, InputTokens: len(text) / 4})
		noise := rng.Float64() < 0.02
		down = append(down, Sample{Features: fs, Y: ok != noise})
		up = append(up, Sample{Features: fs, Y: !ok && rng.Float64() < 0.8})
	}
	return down, up
}

func TestTrainTier(t *testing.T) {
	down, up := judged(1200, 7)
	cheap := TrainTier("fast", -1, down, Options{Seed: 1})
	if cheap.Threshold <= 0 || cheap.Metrics.Precision < 0.95 || cheap.Metrics.Coverage < 0.3 || cheap.Metrics.AUC < 0.9 {
		t.Fatalf("cheap tier %+v", cheap.Metrics)
	}
	strong := TrainTier("expert", 1, up, Options{Seed: 1})
	if strong.Threshold <= 0 || strong.Metrics.Precision <= 0.5 {
		t.Fatalf("strong tier %+v", strong.Metrics)
	}

	m := Model{Default: "smart", Tiers: []TierModel{cheap, strong}}
	count := Features(Input{Ask: "How many times is the word warranty used?" + string(make([]byte, 0)), System: "You read contracts.",
		Family: "f", Messages: 1, InputTokens: 10})
	if p := m.Place(count); p.Tier != "fast" || p.P < float64(cheap.Threshold) {
		t.Fatalf("a counting question: %+v", p)
	}
	legal := Features(Input{Ask: "Is this warranty contract legal in Germany?", System: "You read contracts.", Family: "f",
		Messages: 1, InputTokens: 10})
	if p := m.Place(legal); p.Tier == "fast" {
		t.Fatalf("a legal question went to the cheap tier: %+v", p)
	}

	// Weights survive packing.
	var fresh TierModel
	fresh.Bias, fresh.Threshold, fresh.Weights = cheap.Bias, cheap.Threshold, cheap.Weights
	if math.Abs(fresh.Prob(count)-cheap.Prob(count)) > 1e-9 {
		t.Fatal("packed weights differ")
	}
}

func TestTooFewSamples(t *testing.T) {
	down, _ := judged(100, 3)
	if m := TrainTier("fast", -1, down, Options{}); m.Threshold != 0 || m.Weights != nil || m.Metrics.Samples != 100 {
		t.Fatalf("%+v", m)
	}
	// A model without a trained tier is unsure of every call.
	m := Model{Tiers: []TierModel{{Tier: "fast", Rank: -1}}}
	if p := m.Place([]uint32{1, 2}); p.Tier != "" || !p.Unsure {
		t.Fatalf("%+v", p)
	}
}

func TestWilson(t *testing.T) {
	for _, tt := range []struct {
		k, n   int
		lo, hi float64
	}{{0, 0, 0, 1}, {95, 100, 0.8882, 0.9785}, {200, 200, 0.9812, 1}, {0, 10, 0, 0.2775}} {
		lo, hi := Wilson(tt.k, tt.n)
		if math.Abs(lo-tt.lo) > 1e-3 || math.Abs(hi-tt.hi) > 1e-3 {
			t.Errorf("%d/%d: %.4f–%.4f", tt.k, tt.n, lo, hi)
		}
	}
}

func BenchmarkTrainTier(b *testing.B) {
	down, _ := judged(2000, 11)
	for b.Loop() {
		TrainTier("fast", -1, down, Options{Seed: 1})
	}
}
