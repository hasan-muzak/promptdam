package cacheprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/hasan-muzak/promptdam/server/features/cacheprofile"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
)

// Saved profiles serve at once, another process reads the newest, and
// only the last few are kept.
func TestStore(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	ctx := context.Background()
	s := w.App.CacheProfiles
	for i := range 7 {
		d := &cacheprofile.Doc{Learned: time.Unix(int64(i), 0).UTC(), Models: map[string]cacheprofile.Profile{
			cacheprofile.Key("openrouter", "m"): {Use: promptcache.Markers, Evidence: int64(i)},
		}}
		if err := s.Save(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Current().Models[cacheprofile.Key("openrouter", "m")]; got.Evidence != 6 {
		t.Fatalf("current %+v", got)
	}
	other := cacheprofile.New(cacheprofile.Config{Ent: w.App.Ent, KV: w.C.KV})
	if err := other.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := other.Current().Models[cacheprofile.Key("openrouter", "m")]; got.Evidence != 6 {
		t.Fatalf("another process read %+v", got)
	}
	if n := w.App.Ent.CacheProfile.Query().CountX(ctx); n != 5 {
		t.Fatalf("%d kept", n)
	}
	if p := other.Plan("openrouter", false, "m", promptcache.Auto, "c", true); p.Strategy != promptcache.Markers {
		t.Fatalf("plan %+v", p)
	}
}

// A strategy a provider refused is off for its model at once in the
// process that heard, then in every process that loads, until it ages out
// (and is dropped).
func TestRefused(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{})
	ctx := context.Background()
	plan := func(s *cacheprofile.Store) string {
		return s.Plan("openrouter", false, "m", promptcache.Markers, "c", true).Strategy
	}
	s := w.App.CacheProfiles
	s.Refused("openrouter", "m", promptcache.Markers)
	s.Refused("openrouter", "m", promptcache.Markers)
	if got := plan(s); got != promptcache.Auto {
		t.Fatalf("this process: %q", got)
	}
	if got := s.Plan("openrouter", false, "other", promptcache.Markers, "c", true).Strategy; got != promptcache.Markers {
		t.Fatalf("another model: %q", got)
	}

	now := time.Now()
	other := cacheprofile.New(cacheprofile.Config{Ent: w.App.Ent, KV: w.C.KV, Now: func() time.Time { return now }})
	for i := 0; plan(other) != promptcache.Auto; i++ {
		if i == 200 {
			t.Fatal("another process never heard")
		}
		time.Sleep(10 * time.Millisecond)
		if err := other.Load(ctx); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(8 * 24 * time.Hour)
	if err := other.Load(ctx); err != nil {
		t.Fatal(err)
	}
	third := cacheprofile.New(cacheprofile.Config{Ent: w.App.Ent, KV: w.C.KV})
	if err := third.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if plan(other) != promptcache.Markers || plan(third) != promptcache.Markers {
		t.Fatalf("aged out: %q, %q", plan(other), plan(third))
	}
}
