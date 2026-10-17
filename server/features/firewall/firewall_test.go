package firewall

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/rules"
)

const awsKey = "AKIAIOSFODNN7EXAMPLE"

func engine(t testing.TB, mutate func(*Settings)) *Engine {
	t.Helper()
	env, err := NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	s := Defaults()
	if mutate != nil {
		mutate(&s)
	}
	if err := s.Check(env); err != nil {
		t.Fatal(err)
	}
	e, err := Compile(s, env)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func chat(content string) []byte {
	b, _ := json.Marshal(map[string]any{"model": "gpt-5", "messages": []map[string]any{
		{"role": "system", "content": "You help with deploys."}, {"role": "user", "content": content}}})
	return b
}

func TestSecretsAreBlockedByDefault(t *testing.T) {
	e := engine(t, nil)
	o, err := e.Inspect(chat("Use key " + awsKey + " to deploy"))
	if err != nil || !o.Blocked || o.BlockedBy != ClassSecret || o.Body != nil || len(o.Hits) != 1 ||
		o.Hits[0].Detector != "aws_access_key" || o.Hits[0].Location != "messages" || o.Action() != Block {
		t.Fatalf("%+v %v", o, err)
	}
	if msg := o.BlockMessage(); !strings.Contains(msg, "aws_access_key") || strings.Contains(msg, awsKey) {
		t.Fatalf("message %q", msg)
	}
	// Identifiers and data aren't text: a key-shaped model name, an image.
	clean := []byte(`{"model":"` + awsKey + `","messages":[{"role":"user","content":[{"type":"image_url",` +
		`"image_url":{"url":"data:image/png;base64,` + awsKey + `AAAA"}}]}]}`)
	if o, _ := e.Inspect(clean); o.Blocked || len(o.Hits) != 0 {
		t.Fatalf("data scanned: %+v", o)
	}
	// PII is off by default.
	if o, _ := e.Inspect(chat("Mail ada@example.com")); len(o.Hits) != 0 || o.Body != nil {
		t.Fatalf("pii off: %+v", o)
	}
}

func TestPIIRedactedOrTokenized(t *testing.T) {
	body := chat("Write to ada@example.com and call +44 20 7946 0958 about \"the plan\"")
	o, err := engine(t, func(s *Settings) { s.PII = Redact }).Inspect(body)
	if err != nil || o.Blocked || o.Body == nil || len(o.Hits) != 2 || o.Action() != Redact {
		t.Fatalf("%+v %v", o, err)
	}
	if got := string(o.Body); !strings.Contains(got, "[REDACTED:email]") || strings.Contains(got, "ada@") ||
		!strings.Contains(got, `"You help with deploys."`) || !strings.Contains(got, `\"the plan\"`) || !json.Valid(o.Body) {
		t.Fatalf("redacted %s", got)
	}

	e := engine(t, func(s *Settings) { s.PII = Tokenize })
	o, _ = e.Inspect(body)
	if got := string(o.Body); !strings.Contains(got, "⟦email_1⟧") || !strings.Contains(got, "⟦phone_2⟧") || o.Tokens.Len() != 2 {
		t.Fatalf("tokenized %s", got)
	}
	// The answer gets the values back, whole...
	r := e.Response(o)
	answer := []byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"Sent to ⟦email_1⟧; called ⟦phone_2⟧."}}]}`)
	if got := string(r.Body(answer)); !strings.Contains(got, "Sent to ada@example.com; called +44 20 7946 0958.") {
		t.Fatalf("restored %s", got)
	}
	// ...or streamed, a token cut between two events.
	r = e.Response(o)
	var text strings.Builder
	for _, piece := range []string{"Sent to ⟦em", "ail_1⟧", " now."} {
		ev, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": piece}}}})
		if out := r.Event(ev); out != nil {
			ev = out
		}
		var c struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
		}
		_ = json.Unmarshal(ev, &c)
		text.WriteString(c.Choices[0].Delta.Content)
	}
	if text.String() != "Sent to ada@example.com now." {
		t.Fatalf("streamed %q", text.String())
	}
}

func TestResponsesAreScanned(t *testing.T) {
	e := engine(t, nil)
	r := e.Response(&Outcome{})
	r.Body([]byte(`{"content":[{"type":"text","text":"Here is a key: ` + awsKey + `"}]}`))
	if hits := r.Hits(); len(hits) != 1 || hits[0].Location != "response" || hits[0].Action != Alert {
		t.Fatalf("hits %+v", hits)
	}
	if r := engine(t, func(s *Settings) { s.ScanResponses = false }).Response(&Outcome{}); r != nil {
		t.Fatal("nothing to do, but a filter")
	}
}

func TestPolicies(t *testing.T) {
	e := engine(t, func(s *Settings) {
		s.PII = Alert
		s.Policies = []Policy{
			{Name: "trusted", When: `"trusted" in request.tags`, Action: PolicyAllow},
			{Name: "no pii to deepseek", When: `classes.pii > 0 && target.kind == "deepseek"`, Action: PolicyBlock},
			{Name: "cards stay home", When: `findings.credit_card > 0`, Action: PolicyRouteTo, Model: "local/llama"},
		}
	})
	o, _ := e.Inspect(chat("Mail ada@example.com"))
	e.Decide(o, rules.Request{}, Call{Target: Target{Kind: "deepseek"}})
	if !o.Blocked || o.BlockedBy != "policy:no pii to deepseek" || !strings.Contains(o.BlockMessage(), "no pii to deepseek") {
		t.Fatalf("%+v", o)
	}
	o, _ = e.Inspect(chat("Use " + awsKey))
	e.Decide(o, rules.Request{Tags: []string{"trusted"}}, Call{Target: Target{Kind: "openai"}})
	if o.Blocked || o.Policy != "trusted" {
		t.Fatalf("allowed: %+v", o)
	}
	o, _ = e.Inspect(chat("Card 4111 1111 1111 1111"))
	e.Decide(o, rules.Request{}, Call{Target: Target{Kind: "openai"}})
	if o.RouteTo != "local/llama" {
		t.Fatalf("routed: %+v", o)
	}

	// Who calls, where the findings are, the plan.
	e = engine(t, func(s *Settings) {
		s.PII = Alert
		s.Policies = []Policy{{Name: "ci keys keep pii out of tools", Action: PolicyBlock,
			When: `principal.key == "key_ci" && locations.tools > 0 && org.plan != "business"`}}
	})
	body := []byte(`{"model":"m","messages":[],"tools":[{"name":"mail","description":"writes to ada@example.com"}]}`)
	o, _ = e.Inspect(body)
	if e.Decide(o, rules.Request{}, Call{Key: "key_ci", Plan: "team"}); !o.Blocked {
		t.Fatalf("not blocked: %+v", o)
	}
	o, _ = e.Inspect(body)
	if e.Decide(o, rules.Request{}, Call{Key: "key_ci", Plan: "business"}); o.Blocked {
		t.Fatalf("blocked on business: %+v", o)
	}
}

func TestCheck(t *testing.T) {
	env, _ := NewEnv()
	for name, s := range map[string]Settings{
		"tokenized secrets": {Secrets: Tokenize},
		"an action":         {PII: "hide"},
		"a detector":        {PIIDetectors: []string{"aws_access_key"}},
		"a pattern":         {Custom: map[string]string{"badge": "(["}},
		"a built-in name":   {Custom: map[string]string{"email": "x"}},
		"a fail mode":       {FailMode: "maybe"},
		"route_to nowhere":  {Policies: []Policy{{Name: "p", When: "true", Action: PolicyRouteTo}}},
	} {
		var ie *InvalidError
		if err := s.Check(env); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
	s := Settings{Policies: []Policy{{Name: "p", When: "request.nope", Action: PolicyBlock}}}
	var ie *InvalidError
	if err := s.Check(env); !errors.As(err, &ie) || len(ie.Problems) == 0 || ie.Policy != 0 {
		t.Fatalf("a policy that doesn't compile: %v", err)
	}
}

func TestFailModes(t *testing.T) {
	bad := []byte(`{"messages":[{"content":"unterminated}]}`)
	if o, err := engine(t, nil).Inspect(bad); !errors.Is(err, ErrScan) || !o.Blocked || o.BlockedBy != "scan" {
		t.Fatalf("secrets_closed: %+v %v", o, err)
	}
	if o, err := engine(t, func(s *Settings) { s.FailMode = FailOpen }).Inspect(bad); !errors.Is(err, ErrScan) || o.Blocked || !o.Failed {
		t.Fatalf("open: %+v %v", o, err)
	}
	if o, _ := engine(t, func(s *Settings) { s.Secrets = Alert }).Inspect(bad); o.Blocked {
		t.Fatalf("secrets only alerted: %+v", o)
	}
}

func TestCustomPatternsAndExcerpts(t *testing.T) {
	e := engine(t, func(s *Settings) {
		s.Custom = map[string]string{"badge": `BADGE-\d{6}`}
		s.CustomAction = Redact
		s.Excerpts = true
	})
	o, _ := e.Inspect(chat("Badge BADGE-123456 belongs to ops; key " + awsKey))
	var badge, key *Hit
	for i := range o.Hits {
		switch o.Hits[i].Detector {
		case "badge":
			badge = &o.Hits[i]
		case "aws_access_key":
			key = &o.Hits[i]
		}
	}
	if badge == nil || badge.Class != ClassCustom || key == nil || !strings.Contains(key.Excerpt, "[REDACTED:aws_access_key]") ||
		strings.Contains(key.Excerpt, awsKey) || strings.Contains(key.Excerpt, "BADGE-123456") {
		t.Fatalf("hits %+v", o.Hits)
	}
}

func BenchmarkInspect(b *testing.B) {
	e := engine(b, func(s *Settings) { s.PII = Redact })
	body := chat(strings.Repeat("Extract the invoice number, total and due date from this text. ", 100))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.Inspect(body); err != nil {
			b.Fatal(err)
		}
	}
}

// One streamed chunk, as answers are scanned by default.
func BenchmarkResponseEvent(b *testing.B) {
	r := engine(b, nil).Response(&Outcome{})
	ev := []byte(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1790000000,"model":"gpt-5-mini",` +
		`"choices":[{"index":0,"delta":{"content":"tok42 "},"finish_reason":null,"logprobs":null}]}`)
	b.ReportAllocs()
	for b.Loop() {
		r.Event(ev)
	}
}

func TestLimits(t *testing.T) {
	env, _ := NewEnv()
	for name, s := range map[string]Settings{
		"a long pattern":   {Custom: map[string]string{"x": strings.Repeat("a", MaxPattern+1)}},
		"an empty pattern": {Custom: map[string]string{"x": ""}},
		"a long condition": {Policies: []Policy{{Name: "p", When: "true || " + strings.Repeat("true || ", 600) + "true", Action: PolicyBlock}}},
	} {
		if err := s.Check(env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// An excerpt masks everything in it, whatever the classes' actions and
// wherever the window cuts.
func TestExcerptsMaskEverything(t *testing.T) {
	e := engine(t, func(s *Settings) { s.Excerpts = true }) // PII off
	text := "key " + awsKey + " for ada@example.com, then" + strings.Repeat(" ", 12) + "AKIAI44QH8DHBEXAMPLE"
	o, _ := e.Inspect(chat(text))
	var ex string
	for _, h := range o.Hits {
		if h.Detector == "aws_access_key" {
			ex = h.Excerpt
		}
	}
	// The window ends two bytes into the second key: those are masked too.
	if ex == "" || strings.Contains(ex, "ada@") || strings.Contains(ex, "EXAMPLE") || strings.HasSuffix(ex, "AK") ||
		!strings.Contains(ex, "[REDACTED:email]") || !strings.HasSuffix(ex, "[REDACTED:aws_access_key]") {
		t.Fatalf("excerpt %q", ex)
	}
}

// TestCodeIsNotASecret: a coding agent's call holds code that names
// secrets (a struct field, an environment lookup) and Claude Code's session
// id in metadata.user_id; none of it is a secret. A literal given to the
// same names still is.
func TestCodeIsNotASecret(t *testing.T) {
	e := engine(t, nil)
	code := "func NewCharge(req Request) Charge {\n\treturn Charge{Token: req.Token, Auth: s.cfg.APIKey}\n}\n" +
		"var apiKey = os.Getenv(\"BILLING_API_KEY\")\nsecret_key = settings[name]"
	body, _ := json.Marshal(map[string]any{"model": "claude-sonnet-5-5", "max_tokens": 100,
		"metadata": map[string]any{"user_id": `{"device_id":"7f3a","account_uuid":"","session_id":"0b9c2be7-6e1d-4c55-9a1f-3c2be77c8e10"}`},
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": code}}}}})
	if o, err := e.Inspect(body); err != nil || o.Blocked || len(o.Hits) != 0 {
		t.Fatalf("code read as secrets: %+v %v", o, err)
	}
	for _, literal := range []string{`password: "Summer.2024!"`, "token=abcabcabcabc", "session_id=0b9c2be7-6e1d-4c55",
		"api_key=pk.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbb"} {
		if o, _ := e.Inspect(chat(literal)); !o.Blocked || o.Hits[0].Detector != "secret_assignment" {
			t.Errorf("%s: %+v", literal, o)
		}
	}
	// Elsewhere a user_id is text like any other.
	tool := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1",` +
		`"name":"login","input":{"user_id":"token=abcabcabcabc"}}]}]}`)
	if o, _ := e.Inspect(tool); !o.Blocked {
		t.Errorf("a user_id outside metadata hid a secret: %+v", o)
	}
}
