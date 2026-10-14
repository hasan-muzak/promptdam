package conversation_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/features/conversation"
)

func turn(t *testing.T, body string) wire.Turn {
	t.Helper()
	r, err := wire.ParseRequest(wire.AnthropicMessages, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return r.Turn
}

var scope = conversation.Scope{Project: uuid.New(), APIKey: uuid.New(), Router: "auto", Family: "f1"}

// A conversation as an agent sends it: the first turn's question, then its
// tool loop, then a second question.
const (
	ask1  = `{"model":"m","messages":[{"role":"user","content":"fix the bug"}]}`
	loop1 = `{"model":"m","messages":[{"role":"user","content":"fix the bug"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"code"}]}]}`
	ask2 = `{"model":"m","messages":[{"role":"user","content":"fix the bug"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"code"}]},
		{"role":"assistant","content":"fixed"},{"role":"user","content":"now add a test"}]}`
)

func TestKeys(t *testing.T) {
	k1, ok := conversation.KeysOf(scope, "", turn(t, ask1))
	if !ok || k1.Continuation || !k1.Prev.IsZero() || k1.HumanTurns != 1 {
		t.Fatalf("the first question %+v", k1)
	}
	l1, _ := conversation.KeysOf(scope, "", turn(t, loop1))
	if l1.Turn != k1.Turn || !l1.Continuation || l1.Conversation != k1.Conversation {
		t.Fatalf("the tool loop left its turn: %+v vs %+v", l1, k1)
	}
	k2, _ := conversation.KeysOf(scope, "", turn(t, ask2))
	if k2.Turn == k1.Turn || k2.Prev != k1.Turn || k2.Continuation || k2.Conversation != k1.Conversation || k2.HumanTurns != 2 {
		t.Fatalf("the second question %+v", k2)
	}

	// Another key, end user, route, router or family: another conversation.
	for _, other := range []conversation.Scope{
		{Project: scope.Project, APIKey: uuid.New(), Router: "auto", Family: "f1"},
		{Project: scope.Project, APIKey: scope.APIKey, User: "u2", Router: "auto", Family: "f1"},
		{Project: scope.Project, APIKey: scope.APIKey, Route: "r2", Router: "auto", Family: "f1"},
		{Project: scope.Project, APIKey: scope.APIKey, Router: "other", Family: "f1"},
		{Project: scope.Project, APIKey: scope.APIKey, Router: "auto", Family: "f2"},
	} {
		if k, _ := conversation.KeysOf(other, "", turn(t, ask1)); k.Turn == k1.Turn || k.Conversation == k1.Conversation {
			t.Fatalf("scope %+v shares the turn", other)
		}
	}
	// A session names the conversation.
	s1, _ := conversation.KeysOf(scope, "session-1", turn(t, ask1))
	s2, _ := conversation.KeysOf(scope, "session-2", turn(t, ask1))
	if s1.Conversation == s2.Conversation || s1.Turn != k1.Turn {
		t.Fatal("sessions name conversations, not turns")
	}
	if _, ok := conversation.KeysOf(scope, "", turn(t, `{"model":"m","messages":[]}`)); ok {
		t.Fatal("a call without a question has a turn")
	}
	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", "cc-1")
	if conversation.Session(h) != "cc-1" {
		t.Fatal("Claude Code's session")
	}
}

// Two replicas share turns through Valkey: the first placement wins, and
// a turn's calls tell how long after the conversation's previous one each
// started.
func TestStore(t *testing.T) {
	ctx := context.Background()
	store := testkit.Valkey(t)
	a, b := conversation.New(store, conversation.Options{}), conversation.New(store, conversation.Options{})
	for _, s := range []*conversation.Store{a, b} {
		if err := s.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Stop(ctx) })
	}
	k1, _ := conversation.KeysOf(scope, "", turn(t, ask1))
	k2, _ := conversation.KeysOf(scope, "", turn(t, ask2))

	a.Place(scope.Project, k1.Turn, conversation.Placement{Tier: "fast", Source: "classifier", Decided: "fast", Version: "v1"})
	b.Place(scope.Project, k1.Turn, conversation.Placement{Tier: "strong", Source: "default", Decided: "strong", Version: "v1"})
	if r, ok := a.Get(ctx, scope.Project, k1.Turn); !ok || r.Tier != "fast" {
		t.Fatalf("a's own placement %+v", r)
	}
	// Once written, both replicas follow the first placement.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ra, _ := a.Fresh(ctx, scope.Project, k1.Turn)
		rb, _ := b.Fresh(ctx, scope.Project, k1.Turn)
		if ra.Tier != "" && ra.Tier == rb.Tier {
			if bm, _ := b.Get(ctx, scope.Project, k1.Turn); bm.Tier != ra.Tier {
				t.Fatalf("b's memory %q, Valkey %q", bm.Tier, ra.Tier)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the replicas disagree: %q and %q", ra.Tier, rb.Tier)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A replica that never saw the turn reads it from Valkey.
	c := conversation.New(store, conversation.Options{})
	if r, ok := c.Get(ctx, scope.Project, k1.Turn); !ok || r.Tier == "" {
		t.Fatalf("a third replica %+v %v", r, ok)
	}

	// The turn's calls: the first starts the chain; the next is 40 s
	// later; one that started earlier but finished later moves nothing back.
	t0 := time.Now().UnixMilli()
	if gap, state := a.Finish(ctx, scope.Project, k1.Turn, k1.Prev, conversation.Finished{Start: t0, Input: 100, CacheWrite: 90,
		Output: 10, Model: "claude-haiku-4-5", Provider: "anthropic", Kind: "anthropic", TTL: "5m"}); gap != 0 || state != conversation.ChainStart {
		t.Fatalf("first call %d %s", gap, state)
	}
	if gap, state := b.Finish(ctx, scope.Project, k1.Turn, k1.Prev, conversation.Finished{Start: t0 + 40_000, Input: 120,
		CacheRead: 90, CacheWrite: 25, Output: 20, Model: "claude-haiku-4-5", Provider: "anthropic", TTL: "5m"}); gap != 40_000 || state != conversation.ChainContinued {
		t.Fatalf("second call %d %s", gap, state)
	}
	if _, state := a.Finish(ctx, scope.Project, k1.Turn, k1.Prev, conversation.Finished{Start: t0 + 10_000, Input: 1}); state != conversation.ChainContinued {
		t.Fatalf("a late call %s", state)
	}
	r, _ := c.Fresh(ctx, scope.Project, k1.Turn)
	if r.Calls != 3 || r.Input != 221 || r.CacheRead != 90 || r.CacheWrite != 115 || r.Last != t0+40_000 || r.Cached != 115 ||
		r.Start != t0 || r.Model != "claude-haiku-4-5" {
		t.Fatalf("the turn %+v", r)
	}
	// The next turn's first call is timed from the previous turn's last.
	if gap, state := b.Finish(ctx, scope.Project, k2.Turn, k2.Prev, conversation.Finished{Start: t0 + 700_000}); gap != 660_000 ||
		state != conversation.ChainContinued {
		t.Fatalf("the next turn %d %s", gap, state)
	}
}

// Without Valkey, a replica's memory serves.
func TestStoreInMemory(t *testing.T) {
	ctx := context.Background()
	s := conversation.New(nil, conversation.Options{})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(ctx) }()
	k1, _ := conversation.KeysOf(scope, "", turn(t, ask1))
	s.Place(scope.Project, k1.Turn, conversation.Placement{Tier: "fast"})
	if r, ok := s.Get(ctx, scope.Project, k1.Turn); !ok || r.Tier != "fast" {
		t.Fatalf("%+v", r)
	}
	if _, state := s.Finish(ctx, scope.Project, k1.Turn, k1.Prev, conversation.Finished{Start: 1000}); state != conversation.ChainStart {
		t.Fatal(state)
	}
	if gap, state := s.Finish(ctx, scope.Project, k1.Turn, k1.Prev, conversation.Finished{Start: 3000}); gap != 2000 ||
		state != conversation.ChainContinued {
		t.Fatal(gap, state)
	}
}
