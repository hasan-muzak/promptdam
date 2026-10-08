package firewall_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/ent/webhookdelivery"
	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/events"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

const awsKey = "AKIAIOSFODNN7EXAMPLE"

// echo answers with the request's last message.
func echo(_ string, body []byte) string {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	if len(req.Messages) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(req.Messages[len(req.Messages)-1].Content, &text) != nil {
		var blocks []struct{ Text string }
		_ = json.Unmarshal(req.Messages[len(req.Messages)-1].Content, &blocks)
		for _, b := range blocks {
			text += b.Text
		}
	}
	return "You said: " + text
}

// world serves gpt-5-mini at "main" (OpenAI's format) and
// claude-sonnet-5-5 at "claude" (Anthropic's), with the firewall s (nil:
// the defaults).
func world(t *testing.T, s *firewall.Settings) *gatewaytest.World {
	t.Helper()
	w := gatewaytest.New(t, gatewaytest.Options{Upstream: fakeupstream.Options{Reply: echo, StreamReply: true}})
	w.Provider("claude", providers.FormatAnthropic, []string{"claude-sonnet-5-5"})
	w.Serves("main", "gpt-5-mini")
	if s != nil {
		if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, *s); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func request(path, model, content string, stream bool) string {
	b, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 100, "stream": stream,
		"messages": []any{map[string]any{"role": "user", "content": content}}})
	return string(b)
}

// answer is the text of an answer in path's format, whole or streamed.
func answer(t *testing.T, path, body string, stream bool) string {
	t.Helper()
	var out strings.Builder
	if !stream {
		var a struct {
			Choices []struct {
				Message struct{ Content string } `json:"message"`
			} `json:"choices"`
			Content []struct{ Text string } `json:"content"`
		}
		if err := json.Unmarshal([]byte(body), &a); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		for _, c := range a.Choices {
			out.WriteString(c.Message.Content)
		}
		for _, c := range a.Content {
			out.WriteString(c.Text)
		}
		return out.String()
	}
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
			Delta struct{ Text string } `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		for _, c := range ev.Choices {
			out.WriteString(c.Delta.Content)
		}
		out.WriteString(ev.Delta.Text)
	}
	return out.String()
}

func TestSecretsNeverLeave(t *testing.T) {
	w := world(t, nil)
	status, h, body := w.Call("/v1/chat/completions", request("", "gpt-5-mini", "deploy with "+awsKey, false))
	if status != http.StatusBadRequest || h.Get(brand.HeaderErrorCode) != "firewall_blocked" ||
		!strings.Contains(body, "aws_access_key") || strings.Contains(body, awsKey) || !strings.Contains(body, `"code":"firewall_blocked"`) {
		t.Fatalf("%d %s", status, body)
	}
	if status, _, body = w.Call("/v1/messages", request("", "claude-sonnet-5-5", "key "+awsKey, true)); status != http.StatusBadRequest ||
		!strings.Contains(body, `"type":"error"`) {
		t.Fatalf("messages: %d %s", status, body)
	}
	if n := len(w.Fake.Requests()); n != 0 {
		t.Fatalf("a provider got %d calls", n)
	}
	// Refused calls are the project's: logged, with what was found.
	recs := w.Records()
	m, ok := recs[len(recs)-1].Meta.(*resolver.Meta)
	if !ok || recs[len(recs)-1].ErrorCode != "firewall_blocked" || !m.Firewall.Blocked || m.Firewall.Kinds()[0] != "aws_access_key" {
		t.Fatalf("logged %+v", recs[len(recs)-1])
	}
	if status, _, body = w.Call("/v1/chat/completions", request("", "gpt-5-mini", "hello", false)); status != http.StatusOK ||
		answer(t, "", body, false) != "You said: hello" {
		t.Fatalf("a clean call: %d %s", status, body)
	}
}

func TestAnswersAreScanned(t *testing.T) {
	s := firewall.Defaults()
	s.Secrets = firewall.Alert
	w := world(t, &s)
	status, _, body := w.Call("/v1/chat/completions", request("", "gpt-5-mini", "the key is "+awsKey, true))
	if status != http.StatusOK || !strings.Contains(answer(t, "", body, true), awsKey) {
		t.Fatalf("%d %s", status, body)
	}
	recs := w.Records()
	m := recs[len(recs)-1].Meta.(*resolver.Meta)
	if hits := m.Answer.Hits(); len(hits) != 1 || hits[0].Detector != "aws_access_key" || hits[0].Location != "response" ||
		m.Firewall.Action() != firewall.Alert {
		t.Fatalf("answer hits %+v, request %+v", hits, m.Firewall)
	}
}

// Personal data reaches providers as placeholders, and clients get it
// back, in either format, whole or streamed, translated or not.
func TestPIITokenizedAndRestored(t *testing.T) {
	s := firewall.Defaults()
	s.PII = firewall.Tokenize
	w := world(t, &s)
	const said = "Mail ada@example.com, then call +44 20 7946 0958"
	for _, c := range []struct {
		name, path, model string
		stream            bool
	}{
		{"chat", "/v1/chat/completions", "gpt-5-mini", false},
		{"chat streamed", "/v1/chat/completions", "gpt-5-mini", true},
		{"messages", "/v1/messages", "claude-sonnet-5-5", false},
		{"messages streamed", "/v1/messages", "claude-sonnet-5-5", true},
		{"messages to a chat model, streamed", "/v1/messages", "gpt-5-mini", true},
		{"chat to a messages model", "/v1/chat/completions", "claude-sonnet-5-5", false},
		{"chat to a messages model, streamed", "/v1/chat/completions", "claude-sonnet-5-5", true},
	} {
		n := len(w.Fake.Requests())
		status, _, body := w.Call(c.path, request(c.path, c.model, said, c.stream))
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", c.name, status, body)
		}
		sent := string(w.Fake.Requests()[n].Body)
		if strings.Contains(sent, "ada@example.com") || strings.Contains(sent, "7946") ||
			!strings.Contains(sent, "Mail ⟦email_1⟧, then call ⟦phone_2⟧") {
			t.Fatalf("%s: the provider got %s", c.name, sent)
		}
		if got := answer(t, c.path, body, c.stream); got != "You said: "+said {
			t.Fatalf("%s: the client got %q", c.name, got)
		}
	}
}

func TestPolicies(t *testing.T) {
	s := firewall.Defaults()
	s.PII = firewall.Alert
	s.Policies = []firewall.Policy{
		{Name: "trusted", When: `"trusted" in request.tags`, Action: firewall.PolicyAllow},
		{Name: "cards stay home", When: `findings.credit_card > 0`, Action: firewall.PolicyRouteTo, Model: "claude/claude-sonnet-5-5"},
		{Name: "no pii to main", When: `classes.pii > 0 && target.provider == "main"`, Action: firewall.PolicyBlock},
	}
	w := world(t, &s)
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", Fallbacks: []string{"gpt-5-mini"}}); err != nil {
		t.Fatal(err)
	}
	if status, _, body := w.Call("/v1/chat/completions", request("", "gpt-5-mini", "mail ada@example.com", false)); status != http.StatusBadRequest ||
		!strings.Contains(body, "no pii to main") {
		t.Fatalf("blocked by policy: %d %s", status, body)
	}
	status, h, body := w.Call("/v1/chat/completions", request("", "gpt-5-mini", "card 4111 1111 1111 1111", false))
	if reqs := w.Fake.Requests(); status != http.StatusOK || h.Get(brand.HeaderProvider) != "claude" || len(reqs) != 1 ||
		reqs[0].Path != "/messages" {
		t.Fatalf("routed: %d %s %+v", status, body, reqs)
	}
	// The policy's model serves alone: the route's fallback is where the
	// policy keeps the call from.
	w.Fake.Fail(http.StatusInternalServerError)
	if status, _, _ := w.Call("/v1/chat/completions", request("", "gpt-5-mini", "card 4111 1111 1111 1111", false)); status != http.StatusInternalServerError ||
		len(w.Fake.Requests()) != 2 {
		t.Fatalf("fell back: %d, %d calls", status, len(w.Fake.Requests()))
	}
	// Allowed, a secret goes where the client sends it.
	if status, _, body := w.Call("/v1/chat/completions", request("", "gpt-5-mini", "key "+awsKey, false),
		brand.HeaderTags, "trusted"); status != http.StatusOK || !strings.Contains(answer(t, "", body, false), awsKey) {
		t.Fatalf("allowed: %d %s", status, body)
	}
}

// Calls that differ only in what the firewall tokenized share a cached
// answer, each getting its own values back.
func TestCachedAnswersGetTheirOwnValues(t *testing.T) {
	s := firewall.Defaults()
	s.PII = firewall.Tokenize
	w := world(t, &s)
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", CacheTTL: 60}); err != nil {
		t.Fatal(err)
	}
	if status, _, body := w.Call("/v1/chat/completions", request("", "gpt-5-mini", "mail ada@example.com", false)); status != http.StatusOK ||
		answer(t, "", body, false) != "You said: mail ada@example.com" {
		t.Fatalf("first: %d %s", status, body)
	}
	var body string
	w.Eventually(func() bool {
		var h http.Header
		_, h, body = w.Call("/v1/chat/completions", request("", "gpt-5-mini", "mail bob@example.org", false))
		return h.Get(brand.HeaderCache) == "hit"
	})
	if got := answer(t, "", body, false); got != "You said: mail bob@example.org" {
		t.Fatalf("cached: %q", got)
	}
}

// What the firewall did reaches the request log, with an event per
// finding: never the values.
func TestEventsAreLogged(t *testing.T) {
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Upstream: fakeupstream.Options{Reply: echo, StreamReply: true}})
	s := firewall.Defaults()
	s.PII, s.Excerpts = firewall.Tokenize, true
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, s); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for _, content := range []string{"deploy with " + awsKey, "mail ada@example.com today", "hello"} {
		w.Call("/v1/chat/completions", request("", "gpt-5-mini", content, true))
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Action    string   `ch:"firewall_action"`
		Kinds     []string `ch:"firewall_kinds"`
		ErrorCode string   `ch:"error_code"`
	}
	if err := w.C.Events.Select(ctx, &rows, events.Project(w.Project),
		`SELECT firewall_action, firewall_kinds, error_code FROM requests WHERE {tenant} ORDER BY ts`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Action != "block" || rows[0].Kinds[0] != "aws_access_key" || rows[0].ErrorCode != "firewall_blocked" ||
		rows[1].Action != "tokenize" || rows[1].Kinds[0] != "email" || rows[2].Action != "" || len(rows[2].Kinds) != 0 {
		t.Fatalf("requests %+v", rows)
	}

	f := firewall.EventFilter{From: start.Add(-time.Minute), To: time.Now().Add(time.Minute)}
	page, next, err := firewall.ListEvents(ctx, w.C.Events, w.Project, f, nil, 1)
	if err != nil || len(page) != 1 || next == nil || page[0].Detector != "email" || page[0].Action != "tokenize" ||
		page[0].Location != "messages" || !strings.Contains(page[0].Excerpt, "[REDACTED:email]") || strings.Contains(page[0].Excerpt, "ada@") {
		t.Fatalf("first page %+v %v", page, err)
	}
	page, next, err = firewall.ListEvents(ctx, w.C.Events, w.Project, f, next, 1)
	if err != nil || len(page) != 1 || next != nil || page[0].Detector != "aws_access_key" || !page[0].Blocked ||
		page[0].BlockedBy != "secret" || page[0].Provider != "" {
		t.Fatalf("second page %+v %v", page, err)
	}
	byTrace := f
	byTrace.Trace = page[0].Trace
	if one, _, _ := firewall.ListEvents(ctx, w.C.Events, w.Project, byTrace, nil, 10); len(one) != 1 || one[0].Detector != "aws_access_key" {
		t.Fatalf("by trace %+v", one)
	}
	f.Blocked = true
	if page, _, _ = firewall.ListEvents(ctx, w.C.Events, w.Project, f, nil, 10); len(page) != 1 {
		t.Fatalf("blocked %+v", page)
	}
	st, err := firewall.ReadStats(ctx, w.C.Events, w.Project, f.From, f.To)
	if err != nil || st.Calls != 2 || st.Blocked != 1 || len(st.Detectors) != 2 || len(st.Actions) != 2 || len(st.Days) != 1 {
		t.Fatalf("stats %+v %v", st, err)
	}
}

// Blocked calls are told: a webhook each, an alert an hour, a digest a
// week.
func TestWatcherTellsOfBlocks(t *testing.T) {
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Upstream: fakeupstream.Options{Reply: echo}})
	s := firewall.Defaults()
	s.PII = firewall.Tokenize
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, s); err != nil {
		t.Fatal(err)
	}
	all := entx.SkipTenant(ctx)
	w.C.DB.Ent.WebhookEndpoint.Create().SetOrgID(w.Org).SetURL("https://hooks.example.com/x").SetSecret("whsec_dGVzdA==").SaveX(all)
	if _, err := w.C.Notifier.AddChannel(ctx, w.Org, "email", "Ops", "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"key " + awsKey, "again " + awsKey, "mail ada@example.com"} {
		w.Call("/v1/chat/completions", request("", "gpt-5-mini", content, false))
	}
	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	watcher := firewall.NewWatcher(firewall.WatchConfig{Events: w.C.Events, KV: w.C.KV, Notifier: w.C.Notifier,
		Webhooks: w.C.Webhooks, AppURL: "https://app.example.com", Now: func() time.Time { return time.Now().Add(time.Minute) }})
	told := func() (hooks, alerts int) {
		t.Helper()
		hooks = w.C.DB.Ent.WebhookDelivery.Query().Where(webhookdelivery.EventType(firewall.EventBlocked.Name)).CountX(all)
		if err := w.C.DB.SQL.QueryRowContext(ctx, `SELECT count(*) FROM river_job WHERE kind = 'keel_deliver_alert'`).Scan(&alerts); err != nil {
			t.Fatal(err)
		}
		return hooks, alerts
	}
	for range 2 { // the second run finds nothing new
		if err := watcher.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if hooks, alerts := told(); hooks != 2 || alerts != 1 {
			t.Fatalf("%d webhooks, %d alerts", hooks, alerts)
		}
	}
	for range 2 { // one digest a week
		if err := watcher.Digest(ctx); err != nil {
			t.Fatal(err)
		}
		if _, alerts := told(); alerts != 2 {
			t.Fatalf("%d alerts with the digest", alerts)
		}
	}
}
