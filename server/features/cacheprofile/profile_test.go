package cacheprofile_test

import (
	"fmt"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/cacheprofile"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
)

// A call is planned by its model's profile, else the price list's default;
// strategies its kind doesn't take, or its provider refused, are never
// used; the backends to avoid go along.
func TestPlan(t *testing.T) {
	d := &cacheprofile.Doc{Models: map[string]cacheprofile.Profile{
		cacheprofile.Key("openrouter", "google/gemini-3.8-flash"): {Use: promptcache.Markers, Avoid: []string{"google-vertex"}},
		cacheprofile.Key("openai", "gpt-5"):                       {Use: promptcache.Markers},
		cacheprofile.Key("custom", "llama"):                       {Use: promptcache.Markers},
	}}
	markers := []string{promptcache.Markers}
	tests := []struct {
		name, kind, model, def string
		off                    []string
		want                   string
		avoid                  int
	}{
		{"learned", "openrouter", "google/gemini-3.8-flash", promptcache.Auto, nil, promptcache.Markers, 1},
		{"not learned yet: the price list's", "openrouter", "deepseek/deepseek-v4.1-flash", promptcache.Auto, nil, promptcache.Auto, 0},
		{"a strategy its kind doesn't take", "openai", "gpt-5", promptcache.Auto, nil, promptcache.Auto, 0},
		{"refused by its provider", "custom", "llama", promptcache.Markers, markers, promptcache.Auto, 0},
		{"refused, its backends still avoided", "openrouter", "google/gemini-3.8-flash", promptcache.Auto, markers, promptcache.Auto, 1},
	}
	for _, tt := range tests {
		p := d.Plan(tt.kind, false, tt.model, tt.def, "conv", true, tt.off)
		if p.Strategy != tt.want || len(p.Avoid) != tt.avoid || p.Trial {
			t.Errorf("%s: %+v", tt.name, p)
		}
	}
	var nilDoc *cacheprofile.Doc
	if p := nilDoc.Plan("openrouter", false, "m", promptcache.Markers, "", false, nil); p.Strategy != promptcache.Markers {
		t.Fatalf("no profiles: %+v", p)
	}
	// A conversation is kept on one backend only where several serve the
	// model.
	several := &cacheprofile.Doc{Models: map[string]cacheprofile.Profile{
		cacheprofile.Key("openrouter", "a"): {Use: promptcache.Auto, Backends: 3},
		cacheprofile.Key("openrouter", "b"): {Use: promptcache.Auto, Backends: 1},
	}}
	if !several.Plan("openrouter", false, "a", promptcache.Auto, "conv", true, nil).Session ||
		several.Plan("openrouter", false, "b", promptcache.Auto, "conv", true, nil).Session ||
		several.Plan("openrouter", false, "unknown", promptcache.Auto, "conv", true, nil).Session {
		t.Fatal("sessions")
	}
}

// A share of a model's conversations tries the trial strategy: at most
// TrialShare percent, the same arm for a conversation on every call, and
// only for calls in a conversation.
func TestTrialArms(t *testing.T) {
	d := &cacheprofile.Doc{Models: map[string]cacheprofile.Profile{
		cacheprofile.Key("openrouter", "m"): {Use: promptcache.Auto, Trial: promptcache.Markers},
	}}
	trials := 0
	for i := range 10_000 {
		conv := fmt.Sprintf("conversation-%d", i)
		p := d.Plan("openrouter", false, "m", promptcache.Auto, conv, true, nil)
		if p.Trial {
			trials++
			if p.Strategy != promptcache.Markers {
				t.Fatalf("a trial with %+v", p)
			}
		}
		if again := d.Plan("openrouter", false, "m", promptcache.Auto, conv, true, nil); again.Strategy != p.Strategy || again.Trial != p.Trial {
			t.Fatalf("a conversation changed arms: %+v then %+v", p, again)
		}
	}
	if share := float64(trials) / 10_000; share > float64(cacheprofile.TrialShare)/100*1.1 || share < float64(cacheprofile.TrialShare)/100*0.9 {
		t.Fatalf("trial share %.3f", share)
	}
	for i := range 1000 {
		if p := d.Plan("openrouter", false, "m", promptcache.Auto, fmt.Sprintf("c%d", i), false, nil); p.Trial {
			t.Fatal("a call outside a conversation was a trial")
		}
	}
}
