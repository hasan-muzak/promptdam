package payloads_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/ent/payloadsegment"
	"github.com/hasan-muzak/promptdam/server/features/analytics"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/payloads"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

const awsKey = "AKIAIOSFODNN7EXAMPLE"

func echo(_ string, body []byte) string {
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	if len(req.Messages) == 0 {
		return ""
	}
	return "Noted: " + req.Messages[len(req.Messages)-1].Content
}

func capture() *payloads.Capture {
	return payloads.Compile(payloads.Settings{Enabled: true, SampleRate: 1, Answers: true}, nil)
}

func TestBuild(t *testing.T) {
	big := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 2<<20) + `"}]}`
	p := payloads.Build(payloads.Entry{Capture: capture(), Format: wire.OpenAIChat, Status: 200, Request: []byte(big),
		Answer: []byte(`{"choices":[{"message":{"content":"` + strings.Repeat("é", 200_000) + `","tool_calls":[{"function":{"name":"mail","arguments":"{\"to\":\"ada@example.com\"}"}}]}}]}`)})
	if p.Request != nil || len(p.Truncated) != 2 || p.Truncated[0] != "request" || p.Truncated[1] != "answer" ||
		len(p.Answer.Text) > payloads.MaxAnswer || !strings.Contains(string(p.Answer.Tools[0].Args), "[REDACTED:email]") {
		t.Fatalf("built %v %d %s", p.Truncated, len(p.Answer.Text), p.Answer.Tools)
	}
	// A failed call keeps its error, masked.
	p = payloads.Build(payloads.Entry{Capture: capture(), Format: wire.OpenAIChat, Status: 400, Request: []byte(`{"model":"m"}`),
		Answer: []byte(`{"error":{"message":"bad key ` + awsKey + `"}}`)})
	if p.Answer != nil || !strings.Contains(string(p.Error), "[REDACTED:aws_access_key]") {
		t.Fatalf("failed %+v %s", p, p.Error)
	}
}

// Calls through the gateway are kept, masked, read back by their row's
// reference, and go when their retention ends or their project does.
func TestKeptCalls(t *testing.T) {
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Payloads: true,
		Upstream: fakeupstream.Options{Reply: echo, StreamReply: true}})
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-sonnet-5-5"})
	w.Serves("main", "gpt-5-mini")
	fw := firewall.Defaults()
	fw.Secrets = firewall.Alert // let the key through, to see it masked when kept
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, fw); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Payloads.Put(w.Scoped(), w.Org, w.Project, payloads.Settings{Enabled: true, SampleRate: 1, Answers: true}); err != nil {
		t.Fatal(err)
	}
	said := "deploy with " + awsKey + " and mail ada@example.com"
	var traces []string
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gpt-5-mini","messages":[{"role":"user","content":"` + said + `"}]}`},
		{"/v1/messages", `{"model":"claude-sonnet-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"` + said + `"}]}`},
	} {
		status, h, body := w.Call(c.path, c.body)
		if status != 200 {
			t.Fatalf("%s: %d %s", c.path, status, body)
		}
		traces = append(traces, h.Get(brand.HeaderTrace))
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	store := w.App.PayloadStore
	store.Flush(ctx, time.Time{})

	for i, trace := range traces {
		row, err := analytics.New(w.C.Events).Request(ctx, w.Project, trace)
		if err != nil || row.PayloadRef == "" {
			t.Fatalf("%s: row %+v %v", trace, row, err)
		}
		p, err := store.Read(ctx, w.Org, w.Project, row.PayloadRef)
		if err != nil {
			t.Fatal(err)
		}
		req, text := string(p.Request), p.Answer.Text
		if strings.Contains(req, awsKey) || strings.Contains(req, "ada@") || !strings.Contains(req, "[REDACTED:aws_access_key]") ||
			!strings.Contains(req, "[REDACTED:email]") || strings.Contains(text, "ada@") || !strings.Contains(text, "Noted: deploy") ||
			!strings.Contains(text, "[REDACTED:email]") || p.Status != 200 || p.Stream != (i == 1) {
			t.Fatalf("%d: request %s, answer %q", i, req, text)
		}
		// Another project can't read it.
		if _, err := store.Read(ctx, w.Org, uuid.New(), row.PayloadRef); !errors.Is(err, payloads.ErrGone) {
			t.Fatalf("another project read it: %v", err)
		}
	}

	// One segment, expiring as Free keeps logs: 7 days.
	segs := w.App.Ent.PayloadSegment.Query().Where(payloadsegment.ProjectID(w.Project)).AllX(w.Scoped())
	if len(segs) != 1 || segs[0].Payloads != 2 || time.Until(segs[0].ExpiresAt) < 6*24*time.Hour ||
		time.Until(segs[0].ExpiresAt) > 8*24*time.Hour || !strings.HasSuffix(segs[0].Key, "."+w.Project.String()) {
		t.Fatalf("segments %+v", segs)
	}
	row, _ := analytics.New(w.C.Events).Request(ctx, w.Project, traces[0])

	// Past its retention, it goes.
	w.App.Ent.PayloadSegment.UpdateOne(segs[0]).SetExpiresAt(time.Now().Add(-time.Minute)).ExecX(w.Scoped())
	if n, err := store.Expire(ctx); err != nil || n != 1 {
		t.Fatalf("expired %d %v", n, err)
	}
	if _, err := store.Read(ctx, w.Org, w.Project, row.PayloadRef); !errors.Is(err, payloads.ErrGone) {
		t.Fatalf("read after expiry: %v", err)
	}

	// A project's purge takes its objects, not its sibling's.
	sibling := uuid.New()
	for _, p := range []uuid.UUID{w.Project, sibling} {
		store.Add(ctx, payloads.Entry{Org: w.Org, Project: p, RetentionDays: 30, At: time.Now(), Capture: capture(),
			Format: wire.OpenAIChat, Status: 200, Request: []byte(`{"model":"m"}`)})
	}
	store.Flush(ctx, time.Time{})
	if n, err := store.PurgeProjects(ctx, w.Org, []uuid.UUID{w.Project}); err != nil || n != 1 {
		t.Fatalf("purged %d %v", n, err)
	}
	left, err := w.App.Ent.PayloadSegment.Query().All(entx.SkipTenant(ctx))
	if err != nil || len(left) != 2 {
		t.Fatalf("rows %d %v", len(left), err)
	}
	for _, s := range left {
		_, err := w.C.Blob.Get(ctx, s.Key)
		if purged := s.ProjectID == w.Project; purged != (err != nil) {
			t.Fatalf("%s: purged %v, read %v", s.Key, purged, err)
		}
	}
}
