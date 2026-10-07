// Package responsecache answers identical requests from Valkey instead of
// calling the provider again: an exact cache, opt-in per route, for
// non-streamed calls. Requests are identical when their bodies are equal
// once fields that don't change the answer (user, metadata, store, stream
// options) are set aside and keys are sorted.
package responsecache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/kv"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// MaxEntry bounds a cached answer: bigger ones aren't cached.
const MaxEntry = 1 << 20

// Entry is a cached answer.
type Entry struct {
	Status      int        `json:"status"`
	ContentType string     `json:"content_type"`
	Body        []byte     `json:"body"`
	Provider    string     `json:"provider"`
	Kind        string     `json:"kind"`
	Model       string     `json:"model"`
	Usage       wire.Usage `json:"usage"`
}

// Cache keeps answers in Valkey.
type Cache struct {
	kv *kv.Store
}

// New returns a Cache.
func New(store *kv.Store) *Cache { return &Cache{kv: store} }

// ignored are request fields that don't change the answer.
var ignored = []string{"user", "metadata", "store", "stream_options", "service_tier"}

// Key is the cache key of a request in a scope (a project and route,
// which the key names so purges find it). ok is false for a body that
// can't be read.
func Key(scope string, f wire.Format, body []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // numbers keep their exact text
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return "", false
	}
	for _, k := range ignored {
		delete(m, k)
	}
	canonical, err := json.Marshal(m) // maps marshal with sorted keys
	if err != nil {
		return "", false
	}
	h := sha256.New()
	h.Write([]byte(f.String()))
	h.Write([]byte{0})
	h.Write(canonical)
	return scope + ":" + hex.EncodeToString(h.Sum(nil)), true
}

func (c *Cache) key(k string) string { return c.kv.Key("rcache", k) }

// Get returns the cached answer for key.
func (c *Cache) Get(ctx context.Context, key string) (*Entry, bool) {
	raw, err := c.kv.Client.Do(ctx, c.kv.Client.B().Get().Key(c.key(key)).Build()).AsBytes()
	if err != nil {
		return nil, false
	}
	var e Entry
	if json.Unmarshal(raw, &e) != nil {
		return nil, false
	}
	return &e, true
}

// Put caches an answer for ttl, unless it is too big.
func (c *Cache) Put(ctx context.Context, key string, e *Entry, ttl time.Duration) {
	if len(e.Body) > MaxEntry || ttl <= 0 {
		return
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	cmd := c.kv.Client.B().Set().Key(c.key(key)).Value(string(raw)).Px(ttl).Build()
	if err := c.kv.Client.Do(ctx, cmd).Error(); err != nil {
		slog.Warn("responsecache: storing an answer", "err", err)
	}
}
