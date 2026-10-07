// Package snapshot holds what the gateway needs to serve a project's calls:
// its providers (with their keys opened), its keys' settings, its routes
// and its blocking budgets, in memory. The gateway never reads Postgres on
// a call: a snapshot comes from memory, else from Valkey (where provider
// keys stay sealed), else it is built from Postgres once and cached there.
//
// When a project's configuration changes, the control plane drops its
// Valkey copy and publishes the project's ID; every gateway drops its copy
// in memory and loads the new one on the next call. Copies in memory also
// expire after MaxAge, in case a notice is lost.
package snapshot

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
	"golang.org/x/sync/singleflight"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel/pkg/entx"
	"github.com/hasan-muzak/keel/pkg/kv"
	"github.com/hasan-muzak/keel/pkg/kvcache"
	"github.com/hasan-muzak/keel/pkg/memcache"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/budgets"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/keysettings"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/providers"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/routes"
)

// MaxAge is how long a gateway keeps a snapshot without hearing of a
// change: the most a lost change notice delays it.
const MaxAge = 30 * time.Second

// cacheTTL is how long Valkey keeps a snapshot nobody changed.
const cacheTTL = 10 * time.Minute

// Provider is a provider as cached in Valkey: its key sealed.
type Provider struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Format   string    `json:"format"`
	BaseURL  string    `json:"base_url"`
	Models   []string  `json:"models"`
	Priority int       `json:"priority"`
	Sealed   string    `json:"sealed"`
}

// Route is a route as the gateway holds it.
type Route struct {
	Name      string   `json:"name"`
	Fallbacks []string `json:"fallbacks"`
	Retries   int      `json:"retries"`
}

// Data is a project's configuration as cached in Valkey.
type Data struct {
	Org       uuid.UUID                       `json:"org"`
	Providers []Provider                      `json:"providers"`
	Keys      map[string]keysettings.Settings `json:"keys"`
	Routes    []Route                         `json:"routes"`
	Budgets   []budgets.Blocking              `json:"budgets"`
}

// Snapshot is a project's configuration as a gateway holds it in memory.
type Snapshot struct {
	Org, Project uuid.UUID
	// Providers are the enabled ones, by priority, keys opened.
	Providers []Opened
	Keys      map[uuid.UUID]keysettings.Settings
	Routes    map[string]Route
	Budgets   []budgets.Blocking
}

// Opened is a provider with its key's headers.
type Opened struct {
	Provider
	Header http.Header
}

// Sources build a snapshot from Postgres.
type Sources struct {
	Providers *providers.Service
	Keys      *keysettings.Service
	Routes    *routes.Service
	Budgets   *budgets.Service
}

// Build reads a project's configuration from Postgres.
func (s Sources) Build(ctx context.Context, org, project uuid.UUID) (Data, error) {
	ctx = entx.WithOrg(ctx, org)
	d := Data{Org: org, Keys: map[string]keysettings.Settings{}}
	ps, err := s.Providers.List(ctx, project)
	if err != nil {
		return Data{}, fmt.Errorf("snapshot: providers: %w", err)
	}
	for _, p := range ps {
		if p.Enabled {
			d.Providers = append(d.Providers, Provider{
				ID: p.ID, Name: p.Name, Kind: p.Kind, Format: string(p.APIFormat), BaseURL: p.BaseURL,
				Models: p.Models, Priority: p.Priority, Sealed: p.Secrets,
			})
		}
	}
	keys, err := s.Keys.All(ctx, project)
	if err != nil {
		return Data{}, fmt.Errorf("snapshot: keys: %w", err)
	}
	for id, k := range keys {
		d.Keys[id.String()] = k
	}
	rs, err := s.Routes.List(ctx, project)
	if err != nil {
		return Data{}, fmt.Errorf("snapshot: routes: %w", err)
	}
	for _, r := range rs {
		d.Routes = append(d.Routes, Route{Name: r.Name, Fallbacks: r.Fallbacks, Retries: r.Retries})
	}
	if d.Budgets, err = s.Budgets.Blockings(ctx, project); err != nil {
		return Data{}, fmt.Errorf("snapshot: budgets: %w", err)
	}
	return d, nil
}

// channel carries the IDs of projects whose configuration changed.
func channel(store *kv.Store) string { return store.Key("config-changed") }

// Invalidator tells gateways a project's configuration changed.
type Invalidator struct {
	kv    *kv.Store
	cache *kvcache.Cache[Data]
}

// NewInvalidator returns an Invalidator on the gateways' Valkey.
func NewInvalidator(store *kv.Store) *Invalidator {
	return &Invalidator{kv: store, cache: kvcache.New[Data](store, "snapshot", cacheTTL)}
}

// Changed drops the project's cached snapshot and tells every gateway.
// Call it after the change commits.
func (i *Invalidator) Changed(ctx context.Context, project uuid.UUID) {
	i.cache.Forget(ctx, project.String())
	c := i.kv.Client
	if err := c.Do(ctx, c.B().Publish().Channel(channel(i.kv)).Message(project.String()).Build()).Error(); err != nil {
		slog.Warn("snapshot: announcing a change; gateways pick it up within the snapshot's max age",
			"project", project, "err", err)
	}
}

// Store serves snapshots to a gateway.
type Store struct {
	kv      *kv.Store
	cache   *kvcache.Cache[Data]
	local   *memcache.Cache[uuid.UUID, *Snapshot]
	sources Sources
	group   singleflight.Group
	now     func() time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

// NewStore returns a Store reading from sources on a miss.
func NewStore(store *kv.Store, sources Sources) *Store {
	return &Store{
		kv: store, sources: sources, now: time.Now,
		cache: kvcache.New[Data](store, "snapshot", cacheTTL),
		local: memcache.New[uuid.UUID, *Snapshot](MaxAge, time.Second, 100_000),
	}
}

// Get returns a project's snapshot.
func (s *Store) Get(ctx context.Context, org, project uuid.UUID) (*Snapshot, error) {
	return s.local.Get(project, s.now(), func() (*Snapshot, error) {
		v, err, _ := s.group.Do(project.String(), func() (any, error) {
			// Shared by every call waiting on it: one client leaving must
			// not fail the others' load.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			d, err := s.cache.Get(ctx, project.String(), func(ctx context.Context) (Data, error) {
				return s.sources.Build(ctx, org, project)
			})
			if err != nil {
				return nil, err
			}
			return s.open(project, d)
		})
		if err != nil {
			// A store being down says nothing about the project: try again
			// on the next call.
			return nil, memcache.Uncached{Err: err}
		}
		return v.(*Snapshot), nil
	})
}

// open opens the providers' keys of a cached snapshot.
func (s *Store) open(project uuid.UUID, d Data) (*Snapshot, error) {
	snap := &Snapshot{
		Org: d.Org, Project: project, Keys: make(map[uuid.UUID]keysettings.Settings, len(d.Keys)),
		Routes: make(map[string]Route, len(d.Routes)), Budgets: d.Budgets,
	}
	for _, p := range d.Providers {
		h, err := s.sources.Providers.HeaderFor(d.Org, p.Format, p.Sealed)
		if err != nil {
			// One provider's key not opening (a rotated master key gone)
			// leaves the others serving.
			slog.Error("snapshot: a provider's key can't be opened; it is skipped", "provider", p.ID, "err", err)
			continue
		}
		p.Sealed = ""
		snap.Providers = append(snap.Providers, Opened{Provider: p, Header: h})
	}
	for id, k := range d.Keys {
		if u, err := uuid.Parse(id); err == nil {
			snap.Keys[u] = k
		}
	}
	for _, r := range d.Routes {
		snap.Routes[r.Name] = r
	}
	return snap, nil
}

// Forget drops a project's snapshot from memory.
func (s *Store) Forget(project uuid.UUID) { s.local.Forget(project) }

// Lifecycle listens for changes with the gateway.
func (s *Store) Lifecycle() muzak.Lifecycle { return muzak.NewLifecycle("snapshots", s.Start, s.Stop) }

// Start listens for change notices until Stop, subscribing again after a
// lost connection (and dropping every snapshot then: notices may have been
// missed).
func (s *Store) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		c := s.kv.Client
		for ctx.Err() == nil {
			err := c.Receive(ctx, c.B().Subscribe().Channel(channel(s.kv)).Build(), func(m valkey.PubSubMessage) {
				if id, err := uuid.Parse(m.Message); err == nil {
					s.local.Forget(id)
				}
			})
			s.local.Clear()
			if ctx.Err() != nil {
				return
			}
			slog.Warn("snapshot: change notices interrupted; listening again", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	return nil
}

// Stop stops listening.
func (s *Store) Stop(context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	<-s.done
	return nil
}
