// Package prices keeps the model price list Promptdam costs calls with. It
// is reference data, never a bill: customers pay their providers. A job
// syncs it daily from OpenRouter's public model lists (text, embedding and
// decision models); every process holds the newest in memory and reloads
// it when a sync announces a new one.
//
// A sync can't make prices wrong at once: a list much shorter than the
// last is refused, and a model whose price moved more than tenfold keeps
// its previous price until someone looks.
package prices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/riverqueue/river"
	"github.com/valkey-io/valkey-go"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/kv"
	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/safehttp"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/pricelist"
)

// Guards against a bad list.
const (
	// minModels is the fewest models a list may price.
	minModels = 50
	// maxShrink refuses a list pricing less than this share of the last.
	maxShrink = 0.5
	// maxJump holds a model's price that moved more than this factor.
	maxJump = 10
	// keep is how many lists are kept.
	keep = 5
	// every is how often the job looks; it syncs when the newest list is
	// older than stale.
	every = time.Hour
	stale = 23 * time.Hour
	// reload is how often a process reads the newest list anyway, in case
	// an announcement was lost.
	reload = 15 * time.Minute
)

// Prices is what costs calls: the current catalog.
type Prices interface {
	Current() *llm.Catalog
}

// Static is a fixed catalog (tests, a configured file).
type Static struct{ Catalog *llm.Catalog }

// Current implements Prices.
func (s Static) Current() *llm.Catalog { return s.Catalog }

// Config configures a Store.
type Config struct {
	Ent *pdent.Client
	KV  *kv.Store
	// Fallback prices calls until a list has been synced (the embedded
	// catalog, or a configured file).
	Fallback *llm.Catalog
	// URLs are the lists' sources: the first is required, the others are
	// added when they answer (default OpenRouter's text, embedding and
	// decision models).
	URLs []string
	// HTTP fetches it (safehttp.NewClient).
	HTTP *http.Client
	Now  func() time.Time
}

// Store holds the current price list.
type Store struct {
	cfg Config
	cur atomic.Pointer[llm.Catalog]

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Store serving the fallback until Load finds a list.
func New(cfg Config) *Store {
	if len(cfg.URLs) == 0 {
		cfg.URLs = []string{llm.OpenRouterModelsURL, llm.OpenRouterEmbeddingsURL, llm.OpenRouterDecisionsURL}
	}
	if cfg.HTTP == nil {
		cfg.HTTP = safehttp.NewClient(safehttp.Options{Timeout: time.Minute, MaxResponseBytes: 32 << 20})
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Fallback == nil {
		cfg.Fallback = llm.Default()
	}
	s := &Store{cfg: cfg}
	s.cur.Store(cfg.Fallback)
	return s
}

// Current implements Prices.
func (s *Store) Current() *llm.Catalog { return s.cur.Load() }

// Load reads the newest synced list; without one, the fallback stays.
func (s *Store) Load(ctx context.Context) error {
	pl, err := s.cfg.Ent.PriceList.Query().Order(pdent.Desc(pricelist.FieldCreatedAt)).First(ctx)
	if pdent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("prices: reading the newest list: %w", err)
	}
	var c llm.Catalog
	if err := json.Unmarshal(pl.Catalog, &c); err != nil {
		return fmt.Errorf("prices: the newest list doesn't read: %w", err)
	}
	s.cur.Store(&c)
	return nil
}

// Sync fetches the list, checks it against the newest one, stores it and
// tells every process. It does nothing while the newest is fresh, unless
// force.
func (s *Store) Sync(ctx context.Context, force bool) error {
	last, err := s.cfg.Ent.PriceList.Query().Order(pdent.Desc(pricelist.FieldCreatedAt)).First(ctx)
	if err != nil && !pdent.IsNotFound(err) {
		return err
	}
	if last != nil && !force && s.cfg.Now().Sub(last.CreatedAt) < stale {
		return nil
	}
	next, err := s.fetch(ctx)
	if err != nil {
		return err
	}
	held := 0
	if last != nil {
		var prev llm.Catalog
		if err := json.Unmarshal(last.Catalog, &prev); err == nil {
			if held, err = guard(&prev, next); err != nil {
				return err
			}
		}
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := s.cfg.Ent.PriceList.Create().SetSource(strings.Join(s.cfg.URLs, " ")).SetCatalog(data).SetModels(len(next.Models)).SetHeld(held).
		Exec(ctx); err != nil {
		return fmt.Errorf("prices: storing the list: %w", err)
	}
	if err := s.prune(ctx); err != nil {
		slog.Warn("prices: pruning old lists", "err", err)
	}
	s.cur.Store(next)
	c := s.cfg.KV.Client
	if err := c.Do(ctx, c.B().Publish().Channel(channel(s.cfg.KV)).Message("new").Build()).Error(); err != nil {
		slog.Warn("prices: announcing a new list; processes reload it within minutes", "err", err)
	}
	slog.Info("prices: synced", "models", len(next.Models), "held", held)
	return nil
}

// fetch reads every list into one catalog: the first must answer, the
// others are added when they do.
func (s *Store) fetch(ctx context.Context) (*llm.Catalog, error) {
	var out *llm.Catalog
	for i, url := range s.cfg.URLs {
		c, err := s.fetchOne(ctx, url)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			slog.Warn("prices: a list didn't answer; syncing without it", "url", url, "err", err)
			continue
		}
		if out == nil {
			out = c
			continue
		}
		seen := make(map[[2]string]bool, len(out.Models))
		for _, m := range out.Models {
			seen[[2]string{m.Provider, m.ID}] = true
		}
		for _, m := range c.Models {
			if !seen[[2]string{m.Provider, m.ID}] {
				out.Models = append(out.Models, m)
			}
		}
	}
	return out, nil
}

func (s *Store) fetchOne(ctx context.Context, url string) (*llm.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prices: fetching %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prices: %s answered %d", url, res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("prices: reading %s: %w", url, err)
	}
	return llm.FromOpenRouter(data)
}

// ErrSuspicious refuses a list that looks broken.
var ErrSuspicious = errors.New("prices: the new list looks wrong")

// guard refuses a list much shorter than prev, and keeps prev's price of
// every model whose price moved more than tenfold. It returns how many it
// kept.
func guard(prev, next *llm.Catalog) (int, error) {
	if len(next.Models) < minModels || float64(len(next.Models)) < maxShrink*float64(len(prev.Models)) {
		return 0, fmt.Errorf("%w: %d models, the last had %d", ErrSuspicious, len(next.Models), len(prev.Models))
	}
	old := make(map[[2]string]llm.Model, len(prev.Models))
	for _, m := range prev.Models {
		old[[2]string{m.Provider, m.ID}] = m
	}
	held := 0
	for i, m := range next.Models {
		o, ok := old[[2]string{m.Provider, m.ID}]
		if ok && (jumped(o.Input, m.Input) || jumped(o.Output, m.Output)) {
			slog.Warn("prices: a price moved more than tenfold; keeping the previous one until someone checks",
				"provider", m.Provider, "model", m.ID, "input", m.Input, "was", o.Input, "output", m.Output, "was_output", o.Output)
			next.Models[i] = o
			held++
		}
	}
	return held, nil
}

// jumped reports a price that moved more than tenfold, or to or from free
// (a model priced at nothing would cost every call nothing).
func jumped(was, now float64) bool {
	if was == 0 || now == 0 {
		return was != now
	}
	return now > was*maxJump || now < was/maxJump
}

func (s *Store) prune(ctx context.Context) error {
	ids, err := s.cfg.Ent.PriceList.Query().Order(pdent.Desc(pricelist.FieldCreatedAt)).Offset(keep).IDs(ctx)
	if err != nil || len(ids) == 0 {
		return err
	}
	_, err = s.cfg.Ent.PriceList.Delete().Where(pricelist.IDIn(ids...)).Exec(ctx)
	return err
}

func channel(store *kv.Store) string { return store.Key("prices-changed") }

// Lifecycle loads the newest list and keeps it current with a process.
func (s *Store) Lifecycle() muzak.Lifecycle { return muzak.NewLifecycle("prices", s.Start, s.Stop) }

// Start loads the newest list, then reloads it when a sync announces one
// and every few minutes, until Stop.
func (s *Store) Start(ctx context.Context) error {
	if err := s.Load(ctx); err != nil {
		slog.Warn("prices: loading the newest list; costing with the fallback for now", "err", err)
	}
	lctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	notices := make(chan struct{}, 1)
	go func() {
		c := s.cfg.KV.Client
		for lctx.Err() == nil {
			_ = c.Receive(lctx, c.B().Subscribe().Channel(channel(s.cfg.KV)).Build(), func(valkey.PubSubMessage) {
				select {
				case notices <- struct{}{}:
				default:
				}
			})
			select {
			case <-lctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	go func() {
		defer close(s.done)
		t := time.NewTicker(reload)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
			case <-notices:
			}
			ctx, cancel := context.WithTimeout(lctx, 30*time.Second)
			if err := s.Load(ctx); err != nil {
				slog.Warn("prices: reloading", "err", err)
			}
			cancel()
		}
	}()
	return nil
}

// Stop stops reloading.
func (s *Store) Stop(context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	<-s.done
	return nil
}

// SyncArgs is the job keeping the list current.
type SyncArgs struct{}

// Kind implements river.JobArgs.
func (SyncArgs) Kind() string { return "promptdam_prices_sync" }

type syncWorker struct {
	river.WorkerDefaults[SyncArgs]
	s *Store
}

func (w *syncWorker) Work(ctx context.Context, _ *river.Job[SyncArgs]) error {
	return w.s.Sync(ctx, false)
}

// RegisterWorkers looks every hour and syncs the list once it is a day old,
// so a new deployment has prices within the hour.
func RegisterWorkers(j *jobs.Jobs, s *Store) {
	jobs.Register(j, &syncWorker{s: s})
	j.Every("promptdam-prices-sync", every, func() river.JobArgs { return SyncArgs{} })
}
