package responsecache_test

import (
	"context"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/features/responsecache"
)

func TestKey(t *testing.T) {
	key := func(body string) string {
		t.Helper()
		k, ok := responsecache.Key("prj:default", wire.OpenAIChat, []byte(body))
		if !ok {
			t.Fatalf("no key for %s", body)
		}
		return k
	}
	a := key(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"temperature":0}`)
	// Field order, the end user and metadata don't change the answer.
	if b := key(`{"temperature":0,"user":"u-7","metadata":{"x":"y"},"messages":[{"role":"user","content":"hi"}],"model":"gpt-5"}`); b != a {
		t.Fatal("the same request, another key")
	}
	for _, other := range []string{
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi!"}],"temperature":0}`,
		`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}],"temperature":0}`,
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"temperature":0.0000000001}`,
	} {
		if key(other) == a {
			t.Fatalf("another request, the same key: %s", other)
		}
	}
	if k, _ := responsecache.Key("prj:other", wire.OpenAIChat, []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"temperature":0}`)); k == a {
		t.Fatal("another scope, the same key")
	}
	if _, ok := responsecache.Key("s", wire.OpenAIChat, []byte(`not json`)); ok {
		t.Fatal("a key for a broken body")
	}
}

func TestGetPut(t *testing.T) {
	c := responsecache.New(testkit.Valkey(t))
	ctx := context.Background()
	e := &responsecache.Entry{Status: 200, ContentType: "application/json", Body: []byte(`{"a":1}`), Provider: "openai",
		Model: "gpt-5", Usage: wire.Usage{Usage: llm.Usage{Input: 10, Output: 2}}}
	c.Put(ctx, "prj:k", e, time.Minute)
	got, ok := c.Get(ctx, "prj:k")
	if !ok || string(got.Body) != `{"a":1}` || got.Usage.Input != 10 || got.Model != "gpt-5" {
		t.Fatalf("got %+v, %v", got, ok)
	}
	if _, ok := c.Get(ctx, "prj:missing"); ok {
		t.Fatal("a missing key")
	}
	big := &responsecache.Entry{Status: 200, Body: make([]byte, responsecache.MaxEntry+1)}
	c.Put(ctx, "prj:big", big, time.Minute)
	if _, ok := c.Get(ctx, "prj:big"); ok {
		t.Fatal("an answer over the limit was cached")
	}
}

// A route's cached answers go when the route changes, a project's when it
// asks; others stay.
func TestForget(t *testing.T) {
	c := responsecache.New(testkit.Valkey(t))
	ctx := context.Background()
	e := &responsecache.Entry{Status: 200, ContentType: "application/json", Body: []byte(`{}`)}
	for _, k := range []string{"p1:chat:a", "p1:chat:b", "p1:batch:a", "p2:chat:a"} {
		c.Put(ctx, k, e, time.Minute)
	}
	if n, err := c.Forget(ctx, "p1:chat"); err != nil || n != 2 {
		t.Fatalf("forgot %d %v", n, err)
	}
	for k, kept := range map[string]bool{"p1:chat:a": false, "p1:chat:b": false, "p1:batch:a": true, "p2:chat:a": true} {
		if _, hit := c.Get(ctx, k); hit != kept {
			t.Errorf("%s: kept %v, want %v", k, hit, kept)
		}
	}
	if n, _ := c.Forget(ctx, "p1"); n != 1 {
		t.Fatalf("a project's: %d", n)
	}
}
