package cacheprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/safehttp"

	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/promptcache"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// How profiles are learned.
const (
	// Window is how far back calls are weighed: a backend that started or
	// stopped caching shows within a week.
	Window = 7 * 24 * time.Hour
	// Evidence is the calls a candidate strategy is judged on: one with
	// fewer is tried, and a switch needs both judged.
	Evidence = 300
	// Switch is how much cheaper per input token a strategy must be to
	// take over: below 0.9 of the one in use, so noise doesn't flip it.
	Switch = 0.9
	// Healthy is the read share a strategy needs to replace one never
	// seen, and a backend's hit rate that makes another backend of the
	// model that never reads worth keeping calls off.
	Healthy = 0.5
	// A backend is avoided when it read the cache on under avoidBelow of
	// at least avoidEvidence calls that expected a read, while another of
	// the model's backends read on Healthy of at least goodEvidence.
	avoidEvidence = 100
	avoidBelow    = 0.05
	goodEvidence  = 50
	// MinProjects is how many projects a pooled figure needs before a
	// project is shown it: never one other project's traffic.
	MinProjects = 3
	// A backend serves a model when it answered at least serves of its
	// calls, and at least serveCalls of them.
	serves     = 0.02
	serveCalls = 3
	// A run reads at most endpointFetches models' backend lists, each kept
	// endpointsFor.
	endpointFetches = 20
	endpointsFor    = 24 * time.Hour
)

// Row is a model's calls with one strategy to one backend, pooled over
// the window (cache_health_hourly): calls to providers' own public APIs
// only (gateway.Target.Pooled), never a project's own endpoint, which
// could teach every project what it likes.
type Row struct {
	Kind, Model, Backend, Strategy string
	// Calls are all its calls.
	Calls int64
	// Expects are the calls that expected a cache read, Hits those that
	// read; Expected the tokens they should have read, Read those read.
	Expects, Hits, Expected, Read int64
	// Input is every call's input tokens, Priced their input's cost at
	// list prices (cache reads and writes included), in micro-dollars.
	Input, Priced int64
}

// price is what an input token cost.
func (r Row) price() float64 { return float64(r.Priced) / float64(r.Input) }

func (r *Row) add(o Row) {
	r.Calls += o.Calls
	r.Expects += o.Expects
	r.Hits += o.Hits
	r.Expected += o.Expected
	r.Read += o.Read
	r.Input += o.Input
	r.Priced += o.Priced
}

func (r Row) readShare() float64 {
	if r.Expected == 0 {
		return 0
	}
	return min(float64(r.Read)/float64(r.Expected), 1)
}

func (r Row) hitRate() float64 {
	if r.Expects == 0 {
		return 0
	}
	return float64(r.Hits) / float64(r.Expects)
}

// learned are a model's profile and the backends to avoid, by name until
// their slugs are known.
type learned struct {
	Profile
	kind, model string
	avoid       []string
}

// learn is every model's profile from its pooled calls (rows), the
// profiles before (prev, whose strategies are kept until another is
// judged cheaper) and each model's default strategy (def, from its price
// list), with the projects that contributed (by Key).
//
// A model's candidates are its kind's strategies (gateway.CacheDialect),
// those with a session only where several backends serve it. The one in
// use stays until another, judged on Evidence calls, costs under Switch of
// it per input token (write premiums count, so one that writes and never
// reads loses). While any candidate is unjudged, the least tried is the
// trial: a session's cost shows in conversations' first calls, which no
// read share sees, so trying stops only once every candidate is judged.
func learn(rows []Row, projects map[string]int, prev *Doc, def func(kind, model string) string) map[string]learned {
	type model struct {
		kind, name string
		strategy   map[string]*Row
		backends   map[string]map[string]*Row
	}
	models := map[string]*model{}
	for _, r := range rows {
		key := Key(r.Kind, r.Model)
		m := models[key]
		if m == nil {
			m = &model{kind: r.Kind, name: r.Model, strategy: map[string]*Row{}, backends: map[string]map[string]*Row{}}
			models[key] = m
		}
		if m.strategy[r.Strategy] == nil {
			m.strategy[r.Strategy], m.backends[r.Strategy] = &Row{}, map[string]*Row{}
		}
		m.strategy[r.Strategy].add(r)
		b := m.backends[r.Strategy][r.Backend]
		if b == nil {
			b = &Row{}
			m.backends[r.Strategy][r.Backend] = b
		}
		b.add(r)
	}
	out := make(map[string]learned, len(models))
	for key, m := range models {
		d := gateway.CacheDialectOf(m.kind, false)
		l := learned{Profile: Profile{Projects: projects[key]}, kind: m.kind, model: m.name}
		// Backends that never cache are judged by whether calls are
		// marked, with or without a session: a session doesn't change what
		// a backend caches.
		avoided := map[string][]string{}
		if d.Avoids {
			byBase := map[string]map[string]*Row{}
			for s, backends := range m.backends {
				base := promptcache.Base(s)
				if byBase[base] == nil {
					byBase[base] = map[string]*Row{}
				}
				for name, b := range backends {
					if byBase[base][name] == nil {
						byBase[base][name] = &Row{}
					}
					byBase[base][name].add(*b)
				}
			}
			var anywhere []string
			for base, backends := range byBase {
				avoided[base] = avoid(backends)
				anywhere = append(anywhere, avoided[base]...)
			}
			l.Backends = serving(m.backends, anywhere)
		}
		candidates := d.Strategies
		if l.Backends <= 1 {
			candidates = slices.DeleteFunc(slices.Clone(candidates), promptcache.Sessions)
		}
		use := ""
		if prev != nil {
			use = prev.Models[key].Use
		}
		if !slices.Contains(candidates, use) {
			use = def(m.kind, m.name)
			if l.Backends > 1 && slices.Contains(candidates, promptcache.WithSession(use)) {
				use = promptcache.WithSession(use)
			}
		}
		if !slices.Contains(candidates, use) {
			use = candidates[0]
		}
		stat := func(s string) Row {
			if r := m.strategy[s]; r != nil {
				return *r
			}
			return Row{}
		}
		judged := func(s string) bool { r := stat(s); return r.Calls >= Evidence && r.Priced > 0 && r.Input > 0 }
		// The cheapest judged candidate: a challenger, or, for a strategy
		// never seen (a price list's default changed, the profiles lost),
		// one that caches well.
		u := stat(use)
		best := ""
		for _, c := range candidates {
			if c == use || !judged(c) || u.Input == 0 && stat(c).readShare() < Healthy {
				continue
			}
			if best == "" || stat(c).price() < stat(best).price() {
				best = c
			}
		}
		if best != "" && (u.Input == 0 || judged(use) && stat(best).price() < Switch*u.price()) {
			use = best
		}
		u = stat(use)
		l.Use, l.ReadShare, l.Evidence = use, u.readShare(), u.Expects
		least := int64(-1)
		for _, c := range candidates {
			if c != use && !judged(c) && (least < 0 || stat(c).Calls < least) {
				l.Trial, least = c, stat(c).Calls
			}
		}
		l.avoid = avoided[promptcache.Base(use)]
		out[key] = l
	}
	return out
}

// avoid are a model's backends that never read its cache while another
// does: avoiding them costs no cache, only the backends' other merits.
func avoid(backends map[string]*Row) []string {
	good := false
	for name, b := range backends {
		if name != "" && b.Expects >= goodEvidence && b.hitRate() >= Healthy {
			good = true
			break
		}
	}
	if !good {
		return nil
	}
	var out []string
	for name, b := range backends {
		if name != "" && b.Expects >= avoidEvidence && b.hitRate() < avoidBelow {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// serving counts the backends that served a model (by strategy, then
// name), those avoided left out: a conversation is worth keeping on one
// only when there are several.
func serving(byStrategy map[string]map[string]*Row, avoided []string) int {
	calls := map[string]int64{}
	var all int64
	for _, backends := range byStrategy {
		for name, b := range backends {
			calls[name] += b.Calls
			all += b.Calls
		}
	}
	n := 0
	for name, c := range calls {
		if name != "" && !slices.Contains(avoided, name) && c >= serveCalls && float64(c) >= serves*float64(all) {
			n++
		}
	}
	return n
}

// LearnerConfig configures a Learner.
type LearnerConfig struct {
	Store  *Store
	Events *events.DB
	// Prices give a model its default strategy (DefaultCacheStrategy).
	Prices prices.Prices
	// HTTP reads routers of providers' backend lists (safehttp.NewClient);
	// EndpointsURL is OpenRouter's, %s the model's id.
	HTTP         *http.Client
	EndpointsURL string
	Now          func() time.Time
}

// Learner learns every model's profile from every project's calls.
type Learner struct{ cfg LearnerConfig }

// NewLearner returns a Learner.
func NewLearner(cfg LearnerConfig) *Learner {
	if cfg.HTTP == nil {
		cfg.HTTP = safehttp.NewClient(safehttp.Options{Timeout: 20 * time.Second, MaxResponseBytes: 4 << 20})
	}
	if cfg.EndpointsURL == "" {
		cfg.EndpointsURL = "https://openrouter.ai/api/v1/models/%s/endpoints"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Learner{cfg: cfg}
}

// Learn learns every model's profile and stores them for every process.
func (l *Learner) Learn(ctx context.Context) error {
	if l.cfg.Events == nil {
		return nil
	}
	if err := l.cfg.Store.Load(ctx); err != nil {
		return err
	}
	prev := l.cfg.Store.Current()
	rows, projects, err := l.pooled(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 && len(prev.Models) == 0 {
		return nil
	}
	now := l.cfg.Now().UTC()
	profiles := learn(rows, projects, prev, l.defaultStrategy)
	d := &Doc{Learned: now, Models: make(map[string]Profile, len(profiles)), Endpoints: map[string]Endpoints{}}
	// Backends are avoided by slug: a model's list of them is read when
	// it has one to avoid, and kept a day.
	fetches := 0
	for key, p := range profiles {
		if len(p.avoid) > 0 {
			e, ok := prev.Endpoints[p.model]
			if !ok || now.Sub(e.Fetched) > endpointsFor {
				if fetches < endpointFetches {
					fetches++
					if got, err := l.endpoints(ctx, p.model); err != nil {
						slog.Warn("cacheprofile: reading a model's backends", "kind", p.kind, "model", p.model, "err", err)
					} else {
						e = Endpoints{Fetched: now, Slugs: got}
					}
				}
			}
			if e.Slugs != nil {
				d.Endpoints[p.model] = e
			}
			for _, name := range p.avoid {
				if slug := e.Slugs[name]; slug != "" && !slices.Contains(p.Avoid, slug) {
					p.Avoid = append(p.Avoid, slug)
				}
			}
		}
		d.Models[key] = p.Profile
	}
	return l.cfg.Store.Save(ctx, d)
}

// pooled reads every project's calls of the window by model, backend and
// strategy, and how many projects called each model, in one pass (the
// model-wide grouping set comes back with an empty strategy, which a
// call's never is).
//
// This read crosses projects on purpose (decided 2026-10-14): only counts
// per kind, model, backend and strategy leave it, never a project's id or
// anything its calls said, and a figure is shown to a project only from
// MinProjects projects up. It is the only read that sums projects together
// (core/unscoped_test.go lists the reads across projects).
func (l *Learner) pooled(ctx context.Context) ([]Row, map[string]int, error) {
	var got []struct {
		Kind     string `ch:"kind"`
		Model    string `ch:"target_model"`
		Backend  string `ch:"backend"`
		Strategy string `ch:"cache_strategy"`
		Calls    uint64 `ch:"n"`
		Expects  uint64 `ch:"expects"`
		Hits     uint64 `ch:"hits"`
		Expected uint64 `ch:"expected"`
		Read     uint64 `ch:"was_read"`
		Input    uint64 `ch:"input"`
		Priced   int64  `ch:"priced"`
		Projects uint64 `ch:"projects"`
	}
	err := l.cfg.Events.Unscoped().Select(ctx, &got, `SELECT kind, target_model, backend, cache_strategy,
		sum(calls) AS n, sum(expected_calls) AS expects, sum(hit_calls) AS hits, sum(expected_tokens) AS expected,
		sum(expected_read_tokens) AS was_read, sum(input_tokens) AS input, sum(input_micros) AS priced,
		uniqExact(project_id) AS projects
		FROM cache_health_hourly WHERE pooled AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?))
		GROUP BY GROUPING SETS ((kind, target_model, backend, cache_strategy), (kind, target_model))`,
		l.cfg.Now().Add(-Window).UnixMilli())
	if err != nil {
		return nil, nil, fmt.Errorf("cacheprofile: reading the pooled calls: %w", err)
	}
	rows := make([]Row, 0, len(got))
	projects := map[string]int{}
	for _, g := range got {
		if g.Strategy == "" {
			projects[Key(g.Kind, g.Model)] = int(g.Projects)
			continue
		}
		rows = append(rows, Row{Kind: g.Kind, Model: g.Model, Backend: g.Backend, Strategy: g.Strategy, Calls: int64(g.Calls),
			Expects: int64(g.Expects), Hits: int64(g.Hits), Expected: int64(g.Expected), Read: int64(g.Read),
			Input: int64(g.Input), Priced: g.Priced})
	}
	return rows, projects, nil
}

// defaultStrategy is a model's strategy from its price list.
func (l *Learner) defaultStrategy(kind, model string) string {
	catalog := ""
	if k, ok := providers.LookupKind(kind); ok {
		catalog = k.Catalog
	}
	m, _ := l.cfg.Prices.Current().Lookup(catalog, model)
	return gateway.DefaultCacheStrategy(kind, false, m.CacheWritePriced)
}

// endpoints are a model's backends' slugs by name, from OpenRouter's list
// of the model's endpoints: "google-vertex" for "Google", a provider's
// slug without its region or variant, so all of them are avoided.
func (l *Learner) endpoints(ctx context.Context, model string) (map[string]string, error) {
	u := fmt.Sprintf(l.cfg.EndpointsURL, (&url.URL{Path: model}).EscapedPath())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := l.cfg.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil, fmt.Errorf("status %d", res.StatusCode)
	}
	var body struct {
		Data struct {
			Endpoints []struct {
				Provider string `json:"provider_name"`
				Tag      string `json:"tag"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range body.Data.Endpoints {
		slug, _, _ := strings.Cut(e.Tag, "/")
		if e.Provider != "" && slug != "" {
			out[e.Provider] = slug
		}
	}
	return out, nil
}

// LearnArgs learns every model's profile.
type LearnArgs struct{}

// Kind implements river.JobArgs.
func (LearnArgs) Kind() string { return "promptdam_cache_profiles" }

type learnWorker struct {
	river.WorkerDefaults[LearnArgs]
	l *Learner
}

func (w *learnWorker) Work(ctx context.Context, _ *river.Job[LearnArgs]) error { return w.l.Learn(ctx) }

// RegisterWorkers registers the learning job, hourly.
func RegisterWorkers(j *jobs.Jobs, l *Learner) {
	jobs.Register(j, &learnWorker{l: l})
	j.Every("promptdam-cache-profiles", time.Hour, func() river.JobArgs { return LearnArgs{} })
}
