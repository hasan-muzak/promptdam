package cacheprofile_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

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

// The learner pools every project's calls of the week to providers' public
// APIs (never a project's own endpoint): Gemini on OpenRouter is marked
// and kept off the backend that never cached it (its slug read from
// OpenRouter's list of the model's backends, then kept a day), and each
// model counts the projects it was learned from.
func TestLearner(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	ctx := context.Background()
	var fetched atomic.Int32
	endpoints := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		if r.URL.Path != "/models/google/gemini-3.8-flash/endpoints" {
			http.NotFound(rw, r)
			return
		}
		_, _ = io.WriteString(rw, `{"data":{"id":"google/gemini-3.8-flash","endpoints":[
			{"name":"Google | google/gemini-3.8-flash","provider_name":"Google","tag":"google-vertex/global"},
			{"name":"Google | google/gemini-3.8-flash","provider_name":"Google","tag":"google-vertex/us"},
			{"name":"Google AI Studio | google/gemini-3.8-flash","provider_name":"Google AI Studio","tag":"google-ai-studio"}]}}`)
	}))
	t.Cleanup(endpoints.Close)

	batch, err := w.C.Events.Unscoped().PrepareBatch(ctx, `INSERT INTO cache_health_hourly (project_id, hour, kind,
		target_model, backend, cache_strategy, cache_trial, pooled, calls, expected_calls, hit_calls, expected_tokens,
		expected_read_tokens, input_tokens, input_micros, retention_days)`)
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	pooled := true
	add := func(project uuid.UUID, at time.Time, kind, model, backend, strategy string, expects, hits uint64) {
		if err := batch.Append(project, at, kind, model, backend, strategy, false, pooled, expects+5, expects, hits, expects*2000,
			hits*2000, (expects+5)*2000, int64(expects*300), uint16(30)); err != nil {
			t.Fatal(err)
		}
	}
	projects := []uuid.UUID{w.Project, uuid.New(), uuid.New()}
	for _, p := range projects {
		add(p, hour, "openrouter", "google/gemini-3.8-flash", "Google AI Studio", "markers", 140, 135)
		add(p, hour, "openrouter", "google/gemini-3.8-flash", "Google", "markers", 50, 0)
	}
	add(w.Project, hour.Add(-8*24*time.Hour), "openrouter", "old/model", "X", "auto", 900, 0) // outside the week
	add(w.Project, hour, "anthropic", "claude-opus-5-5", "", "markers", 10, 9)
	// A project's own endpoint, claiming the backend that caches never does.
	pooled = false
	add(uuid.New(), hour, "openrouter", "google/gemini-3.8-flash", "Google AI Studio", "markers", 5000, 0)
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	l := cacheprofile.NewLearner(cacheprofile.LearnerConfig{Store: w.App.CacheProfiles, Events: w.C.Events, Prices: w.App.Prices,
		HTTP: endpoints.Client(), EndpointsURL: endpoints.URL + "/models/%s/endpoints"})
	if err := l.Learn(ctx); err != nil {
		t.Fatal(err)
	}
	d := w.App.CacheProfiles.Current()
	g := d.Models[cacheprofile.Key("openrouter", "google/gemini-3.8-flash")]
	if g.Use != promptcache.Markers || g.Trial != "" || !slices.Equal(g.Avoid, []string{"google-vertex"}) || g.Projects != 3 ||
		g.Evidence != 570 {
		t.Fatalf("gemini %+v", g)
	}
	if a := d.Models[cacheprofile.Key("anthropic", "claude-opus-5-5")]; a.Use != promptcache.Markers || a.Projects != 1 ||
		a.ReadShare != 0.9 {
		t.Fatalf("claude %+v", a)
	}
	if _, ok := d.Models[cacheprofile.Key("openrouter", "old/model")]; ok || len(d.Models) != 2 {
		t.Fatalf("models %v", d.Models)
	}
	if err := l.Learn(ctx); err != nil {
		t.Fatal(err)
	}
	if fetched.Load() != 1 || w.App.Ent.CacheProfile.Query().CountX(ctx) != 2 {
		t.Fatalf("%d endpoint lists read", fetched.Load())
	}
}
