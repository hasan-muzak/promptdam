package cacheprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/kv"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/cacheprofile"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// Profiles kept, the newest serving: the rest to compare against.
const keep = 5

// reload is how often a process reads the newest profiles, besides when a
// learner announces new ones.
const reload = 15 * time.Minute

// Config configures a Store.
type Config struct {
	Ent *pdent.Client
	KV  *kv.Store
	Now func() time.Time
}

// Store holds the newest profiles, as every process plans calls by them
// (like the price list: features/prices).
type Store struct {
	cfg Config
	cur atomic.Pointer[Doc]

	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a Store with no profiles until Load finds some: every call is
// planned from its target's price list until then.
func New(cfg Config) *Store {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Store{cfg: cfg}
	s.cur.Store(&Doc{})
	return s
}

// Current is the newest profiles.
func (s *Store) Current() *Doc { return s.cur.Load() }

// Plan implements resolver.CachePlanner.
func (s *Store) Plan(kind string, anthropic bool, model, def, conversation string, inTurn bool) gateway.CachePlan {
	return s.Current().Plan(kind, anthropic, model, def, conversation, inTurn, nil)
}

// Load reads the newest profiles.
func (s *Store) Load(ctx context.Context) error {
	row, err := s.cfg.Ent.CacheProfile.Query().Order(pdent.Desc(cacheprofile.FieldCreatedAt)).First(ctx)
	if pdent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cacheprofile: reading the newest profiles: %w", err)
	}
	var d Doc
	if err := json.Unmarshal(row.Doc, &d); err != nil {
		return fmt.Errorf("cacheprofile: the newest profiles don't read: %w", err)
	}
	s.cur.Store(&d)
	return nil
}

// Save stores new profiles, keeps the few before them, and tells every
// process.
func (s *Store) Save(ctx context.Context, d *Doc) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := s.cfg.Ent.CacheProfile.Create().SetDoc(data).SetModels(len(d.Models)).Exec(ctx); err != nil {
		return fmt.Errorf("cacheprofile: storing the profiles: %w", err)
	}
	ids, err := s.cfg.Ent.CacheProfile.Query().Order(pdent.Desc(cacheprofile.FieldCreatedAt)).Offset(keep).IDs(ctx)
	if err == nil && len(ids) > 0 {
		_, err = s.cfg.Ent.CacheProfile.Delete().Where(cacheprofile.IDIn(ids...)).Exec(ctx)
	}
	if err != nil {
		slog.Warn("cacheprofile: pruning old profiles", "err", err)
	}
	s.cur.Store(d)
	c := s.cfg.KV.Client
	if err := c.Do(ctx, c.B().Publish().Channel(channel(s.cfg.KV)).Message("new").Build()).Error(); err != nil {
		slog.Warn("cacheprofile: announcing new profiles; processes read them within minutes", "err", err)
	}
	return nil
}

func channel(store *kv.Store) string { return store.Key("cache-profiles-changed") }

// Lifecycle loads the newest profiles and keeps them current with a
// process.
func (s *Store) Lifecycle() muzak.Lifecycle {
	return muzak.NewLifecycle("cacheprofiles", s.Start, s.Stop)
}

// Start loads the newest profiles, then reloads them when a learner
// announces new ones and every few minutes, until Stop.
func (s *Store) Start(ctx context.Context) error {
	if err := s.Load(ctx); err != nil {
		slog.Warn("cacheprofile: loading the newest profiles; planning calls from their price lists for now", "err", err)
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
				slog.Warn("cacheprofile: reloading", "err", err)
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
