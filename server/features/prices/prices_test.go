package prices_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/hasan-muzak/keel/pkg/llm"
	"github.com/hasan-muzak/keel/pkg/safehttp"
	"github.com/hasan-muzak/keel/pkg/testkit"

	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/prices"
	"github.com/hasan-muzak/keel/products/promptdam/server/migrations"
)

// list is an OpenRouter models list of n OpenAI models at $1/$2 per 1M,
// the first at the given input price per token.
func list(n int, first string) string {
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		in := "0.000001"
		if i == 0 {
			in = first
		}
		fmt.Fprintf(&b, `{"id":"openai/model-%d","context_length":1000,"pricing":{"prompt":"%s","completion":"0.000002"}}`, i, in)
	}
	b.WriteString("]}")
	return b.String()
}

func TestSync(t *testing.T) {
	ctx := context.Background()
	d := testkit.Postgres(t, migrations.Postgres())
	client := pdent.NewClient(pdent.Driver(entsql.OpenDB(dialect.Postgres, d.SQL)))
	store := testkit.Valkey(t)

	var body atomic.Value
	body.Store(list(60, "0.000001"))
	fetches := atomic.Int32{}
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/down" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fetches.Add(1)
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer src.Close()
	// The second list (embeddings) is down: syncs go on without it.
	cfg := prices.Config{Ent: client, KV: store, URLs: []string{src.URL, src.URL + "/down"},
		HTTP: safehttp.NewClient(safehttp.Options{AllowLoopback: true})}
	a, b := prices.New(cfg), prices.New(cfg)

	// Until a list is synced, the fallback prices calls.
	if a.Current() != llm.Default() && a.Current().Note != llm.Default().Note {
		t.Fatal("no fallback")
	}
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Stop(ctx) }()

	if err := a.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	m, ok := a.Current().Lookup("openai", "model-0")
	if !ok || m.Input != 1 || m.Output != 2 {
		t.Fatalf("synced %+v, %v", m, ok)
	}
	// Another process hears of it.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := b.Current().Lookup("openai", "model-0"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the other process never loaded the new list")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A fresh list isn't fetched again unless forced.
	if err := a.Sync(ctx, false); err != nil || fetches.Load() != 1 {
		t.Fatalf("a fresh list was fetched again: %d fetches, %v", fetches.Load(), err)
	}

	// A price moving more than tenfold keeps its previous value.
	body.Store(list(60, "0.00002"))
	if err := a.Sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if m, _ := a.Current().Lookup("openai", "model-0"); m.Input != 1 {
		t.Fatalf("a twentyfold jump went through: %+v", m)
	}
	// Held twice: under OpenRouter's ID and under OpenAI's name.
	var held int
	if err := d.SQL.QueryRow(`SELECT held FROM price_lists ORDER BY created_at DESC LIMIT 1`).Scan(&held); err != nil || held != 2 {
		t.Fatalf("held %d, %v", held, err)
	}

	// A list much shorter than the last is refused.
	body.Store(list(55, "0.000001"))
	if err := a.Sync(ctx, true); err != nil {
		t.Fatal(err) // 55 of 60: fine
	}
	body.Store(list(20, "0.000001"))
	if err := a.Sync(ctx, true); !errors.Is(err, prices.ErrSuspicious) {
		t.Fatalf("a list of 20 models: %v", err)
	}
	if len(a.Current().Models) != 110 {
		t.Fatalf("the refused list replaced the current one: %d models", len(a.Current().Models))
	}

	// Only the newest lists are kept.
	for range 6 {
		body.Store(list(60, "0.000001"))
		if err := a.Sync(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := d.SQL.QueryRow(`SELECT count(*) FROM price_lists`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("%d lists kept, %v", n, err)
	}

	// A broken answer leaves everything as it was.
	body.Store("<html>maintenance</html>")
	if err := a.Sync(ctx, true); err == nil {
		t.Fatal("a page was accepted as a list")
	}
}
