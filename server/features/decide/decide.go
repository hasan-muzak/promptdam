// Package decide asks a router's decision model which tier a call needs,
// when the classifier is unsure (project.md §6.2): the model reads the
// tiers' descriptions and examples and an excerpt of the call, and answers
// a tier. A chat model answers in words, held to a schema where it takes
// one; a decision model (OpenRouter's Decisions API) gives each tier a
// probability, and the call gets the cheapest tier likely Enough. A hard timeout falls back to
// the default tier: the router's, else one measured on the model's latest
// answers (latency.go). Answers are cached per project, router, family, the
// question and the tiers as the model read them, so an edited router never
// reuses an answer about its old tiers; identical questions asked at once
// share one call.
package decide

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/memcache"

	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/translate"
)

// CacheTTL is how long a decision is reused for the same router, family
// and message.
const CacheTTL = 10 * time.Minute

// Bounds on what the decision model reads.
const (
	maxInstructions = 1_000
	maxRequest      = 4_000
	maxExample      = 200
	maxExamples     = 3
)

// Decider asks decision models; it implements resolver.Decider.
type Decider struct {
	client *http.Client
	cache  *memcache.Cache[[32]byte, cached]
	now    func() time.Time

	lat latencies

	mu       sync.Mutex
	inflight map[[32]byte]*flight
}

// cached is an answer kept for the same question: its tier and how likely
// the tier is enough (0 when the model gave no probabilities).
type cached struct {
	tier string
	p    float64
}

// flight is a question being asked: callers asking the same wait for it.
type flight struct {
	done    chan struct{}
	answer  cached
	outcome string
}

// answer is what a decision model answered and what that cost: the usage,
// and the cost itself when the provider reports it (micros).
type answer struct {
	cached
	usage  wire.Usage
	micros int64
}

// New returns a Decider sending calls with transport (safehttp's).
func New(transport http.RoundTripper) *Decider {
	// Redirects aren't followed: a provider's key goes to its own endpoint
	// only (net/http keeps X-Api-Key on a redirect to another host).
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &Decider{client: client, now: time.Now, inflight: map[[32]byte]*flight{}, lat: latencies{byID: map[string]*ring{}},
		cache: memcache.New[[32]byte, cached](CacheTTL, time.Second, 100_000)}
}

// Decide implements resolver.Decider: the tier the model picks ("" when it
// picks none), within the question's timeout (or the model's auto one),
// and how the question went. Only the call that asked pays: a cached
// answer, or one shared with a question asked at the same time, returns no
// usage. An answer naming no tier isn't cached.
func (d *Decider) Decide(ctx context.Context, q resolver.Question) resolver.Decision {
	out := resolver.Decision{Timeout: q.Timeout}
	if out.Timeout <= 0 {
		out.Timeout = d.lat.timeout(q.Target.ID)
	}
	key := keyOf(q)
	if a, ok := d.cache.Peek(key, d.now()); ok {
		out.Tier, out.Confidence, out.Outcome = a.tier, a.p, resolver.DecisionCached
		return out
	}
	begin := time.Now()
	d.mu.Lock()
	if f, ok := d.inflight[key]; ok {
		d.mu.Unlock()
		wait := time.NewTimer(out.Timeout)
		defer wait.Stop()
		select {
		case <-f.done:
			out.Tier, out.Confidence, out.Outcome = f.answer.tier, f.answer.p, f.outcome
			if out.Outcome == resolver.DecisionAnswered {
				out.Outcome = resolver.DecisionCached // another call's answer
			}
		case <-wait.C:
			out.Outcome = resolver.DecisionTimeout
		case <-ctx.Done():
			out.Outcome = resolver.DecisionError
		}
		out.Took = time.Since(begin)
		return out
	}
	f := &flight{done: make(chan struct{})}
	d.inflight[key] = f
	d.mu.Unlock()

	a, err := d.ask(ctx, q, out.Timeout)
	out.Tier, out.Confidence, out.Usage, out.Micros, out.Took = a.tier, a.p, a.usage, a.micros, time.Since(begin)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		// It took the whole wait at least: counted so, a wait too short
		// for the model grows.
		out.Outcome = resolver.DecisionTimeout
		d.lat.add(q.Target.ID, out.Timeout)
	case err != nil:
		out.Outcome = resolver.DecisionError
	case a.tier == "":
		out.Outcome = resolver.DecisionUnreadable
		d.lat.add(q.Target.ID, out.Took)
	default:
		out.Outcome = resolver.DecisionAnswered
		d.lat.add(q.Target.ID, out.Took)
		d.cache.Set(key, a.cached, d.now())
	}
	f.answer, f.outcome = a.cached, out.Outcome
	d.mu.Lock()
	delete(d.inflight, key)
	d.mu.Unlock()
	close(f.done)
	return out
}

// keyOf keys a question by what the model reads, its tiers included.
func keyOf(q resolver.Question) [32]byte {
	h := sha256.New()
	for _, s := range []string{q.Project.String(), q.Router, q.Family, q.Instructions, q.Request} {
		_, _ = io.WriteString(h, s)
		_, _ = h.Write([]byte{0})
	}
	for _, t := range q.Tiers {
		_, _ = io.WriteString(h, t.Name)
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, t.Description)
		_, _ = h.Write([]byte{0})
		for _, e := range t.Examples {
			_, _ = io.WriteString(h, e)
			_, _ = h.Write([]byte{1})
		}
	}
	var k [32]byte
	copy(k[:], h.Sum(nil))
	return k
}

func (d *Decider) ask(ctx context.Context, q resolver.Question, timeout time.Duration) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if q.Target.Decisions {
		return d.askDecisions(ctx, q)
	}
	t := q.Target
	f := wire.OpenAIChat
	if t.Format != 0 {
		f = t.Format
	}
	names := tierNames(q)
	schema := map[string]any{"type": "object", "properties": map[string]any{"tier": map[string]any{"type": "string", "enum": names}},
		"required": []string{"tier"}, "additionalProperties": false}
	system, user := Prompt(q)
	// No temperature: current models refuse one (Anthropic's since Opus
	// 4.7, OpenAI's reasoning models), and answers are cached anyway.
	var req map[string]any
	if f.Anthropic() {
		req = map[string]any{"model": t.Model, "max_tokens": 64, "system": system,
			"messages": []map[string]string{{"role": "user", "content": user}}}
		config := map[string]any{}
		switch {
		case t.ThinkingOff != "":
			// A model that thinks unless told not to would spend the
			// answer's few tokens thinking, and the timeout waiting.
			req["thinking"] = map[string]string{"type": t.ThinkingOff}
		case t.AlwaysThinks:
			req["max_tokens"] = 1024
			config["effort"] = "low"
		}
		// No schema: Anthropic's structured outputs held Haiku 5.5's
		// answer back by 0.4 s at the median and up to 2.2 s (2026-10-14,
		// against 1.1 s at most without); the answer the prompt asks for
		// reads as well.
		if len(config) > 0 {
			req["output_config"] = config
		}
	} else {
		req = map[string]any{"model": t.Model, "messages": []map[string]string{
			{"role": "system", "content": system}, {"role": "user", "content": user}}}
		// A model that reasons before it answers gets room to, at a low
		// effort (below), or its reasoning spends the answer's tokens.
		limit := 64
		if t.Reasoning {
			limit = 1024
		}
		if t.Kind == "openai" {
			req["max_completion_tokens"] = max(limit, 256)
		} else {
			req["max_tokens"] = limit
		}
		if q.Schema {
			req["response_format"] = map[string]any{"type": "json_schema",
				"json_schema": map[string]any{"name": "tier", "schema": schema, "strict": true}}
		}
	}
	payload, err := encode(req)
	if err != nil {
		return answer{}, err
	}
	if !f.Anthropic() && t.Reasoning {
		if low, ok, err := translate.SetEffort(payload, wire.OpenAIChat, t.Kind, "low"); err == nil && ok {
			payload = low
		}
	}
	path := "/chat/completions"
	if f.Anthropic() {
		path = "/messages"
	}
	body, err := d.post(ctx, t, strings.TrimRight(t.BaseURL, "/")+path, payload, f.Anthropic())
	if err != nil {
		return answer{}, err
	}
	meter := wire.NewMeter(f)
	meter.Body(body)
	usage, _ := meter.Usage()
	return answer{cached: cached{tier: pick(f, body, names)}, usage: usage}, nil
}

// Enough is how likely the tier a decision model's answer leads to must be
// to answer the call as well as the strongest tier: the call gets the
// cheapest tier whose probability, with the cheaper tiers', reaches it. A
// model sure of a cheap tier gets it; one torn between tiers leads to the
// stronger.
const Enough = 0.8

// askDecisions asks a decision model, through OpenRouter's Decisions API,
// one choice: the tier, among the tiers' names with their descriptions and
// examples as the criteria, about a state of the call's instructions and
// request. The answer gives each tier a probability and says what it cost.
func (d *Decider) askDecisions(ctx context.Context, q resolver.Question) (answer, error) {
	t := q.Target
	if t.Kind != "openrouter" {
		return answer{}, fmt.Errorf("decide: %s is a decision model, served by OpenRouter's Decisions API: use it on an OpenRouter provider", t.Model)
	}
	names := tierNames(q)
	criteria := make(map[string]string, len(q.Tiers))
	for _, tier := range q.Tiers {
		c := tier.Description
		if c == "" {
			c = "The " + tier.Name + " tier."
		}
		for i, e := range tier.Examples {
			if i == maxExamples {
				break
			}
			c += " Example: " + clip(e, maxExample)
		}
		criteria[tier.Name] = c
	}
	state := map[string]string{"request": clip(q.Request, maxRequest)}
	if q.Instructions != "" {
		state["instructions"] = clip(q.Instructions, maxInstructions)
	}
	payload, err := encode(map[string]any{"model": t.Model, "state": state, "questions": map[string]any{
		"tier": map[string]any{"type": "choice", "criteria": criteria, "instructions": "Which tier of AI models should answer the request? " +
			"The tiers, from cheapest to strongest: " + strings.Join(names, ", ") + ". Pick the cheapest tier that will answer the request " +
			"as well as the strongest would; when unsure, a stronger one."}}})
	if err != nil {
		return answer{}, err
	}
	body, err := d.post(ctx, t, decisionsURL(t.BaseURL), payload, false)
	if err != nil {
		return answer{}, err
	}
	var res struct {
		Answers map[string]struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
		Usage struct {
			Input  uint64  `json:"input_tokens"`
			Output uint64  `json:"output_tokens"`
			Cost   float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return answer{}, fmt.Errorf("decide: %s's answer: %w", t.Provider, err)
	}
	a := answer{usage: wire.Usage{Usage: llm.Usage{Input: res.Usage.Input, Output: res.Usage.Output}},
		micros: int64(math.Round(max(res.Usage.Cost, 0) * 1e6))}
	ans := res.Answers["tier"]
	a.tier, a.p = enough(names, ans.Probabilities, ans.Choice)
	return a, nil
}

// enough is the cheapest tier whose probability, with the cheaper tiers',
// reaches Enough, and that probability; the model's choice when it gave
// no probabilities ("" when it names no tier).
func enough(names []string, probs map[string]float64, choice string) (string, float64) {
	if len(probs) == 0 {
		for _, n := range names {
			if strings.EqualFold(n, strings.TrimSpace(choice)) {
				return n, 0
			}
		}
		return "", 0
	}
	sum := 0.0
	for _, n := range names {
		sum += probs[n]
		if sum >= Enough {
			return n, min(sum, 1)
		}
	}
	return names[len(names)-1], min(sum, 1) // probabilities short of it: the strongest
}

// decisionsURL is OpenRouter's Decisions API beside a provider's base URL
// (https://openrouter.ai/api/v1: https://openrouter.ai/api/alpha/decisions).
func decisionsURL(base string) string {
	return strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1") + "/alpha/decisions"
}

func tierNames(q resolver.Question) []string {
	names := make([]string, len(q.Tiers))
	for i, tier := range q.Tiers {
		names[i] = tier.Name
	}
	return names
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// post sends a question to a decision model at url and reads its answer.
func (d *Decider) post(ctx context.Context, t gateway.Target, url string, payload []byte, anthropic bool) ([]byte, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept-Encoding", "identity")
	if anthropic {
		hreq.Header.Set("Anthropic-Version", "2023-06-01")
	}
	for k, v := range t.Header {
		hreq.Header[k] = v
	}
	res, err := d.client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("decide: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("decide: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("decide: %s answered %d", t.Provider, res.StatusCode)
	}
	return body, nil
}

// Prompt is what the decision model reads: the tiers, cheapest first, and
// the call.
func Prompt(q resolver.Question) (system, user string) {
	var s strings.Builder
	s.WriteString("You route requests to the right tier of AI models. The tiers, from cheapest to strongest:\n")
	for _, t := range q.Tiers {
		s.WriteString("- " + t.Name)
		if t.Description != "" {
			s.WriteString(": " + t.Description)
		}
		s.WriteByte('\n')
		for i, e := range t.Examples {
			if i == maxExamples {
				break
			}
			s.WriteString("  Example: " + clip(e, maxExample) + "\n")
		}
	}
	s.WriteString("Pick the cheapest tier that will answer the request as well as the strongest would. " +
		"When unsure, pick a stronger tier.\nAnswer with a JSON object only: {\"tier\": \"<name>\"}.")
	var u strings.Builder
	if q.Instructions != "" {
		u.WriteString("<instructions>\n" + clip(q.Instructions, maxInstructions) + "\n</instructions>\n")
	}
	u.WriteString("<request>\n" + clip(q.Request, maxRequest) + "\n</request>")
	return s.String(), u.String()
}

var tierRE = regexp.MustCompile(`"tier"\s*:\s*"([^"]+)"`)

// pick reads the tier from the model's answer: one of names, else "".
func pick(f wire.Format, body []byte, names []string) string {
	var text string
	if f.Anthropic() {
		var m struct {
			Content []struct {
				Type, Text string
			} `json:"content"`
		}
		if json.Unmarshal(body, &m) == nil {
			for _, b := range m.Content {
				if b.Type == "text" {
					text += b.Text
				}
			}
		}
	} else {
		var c struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &c) == nil && len(c.Choices) > 0 {
			text = c.Choices[0].Message.Content
		}
	}
	var v struct {
		Tier string `json:"tier"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &v) != nil || v.Tier == "" {
		if m := tierRE.FindStringSubmatch(text); m != nil {
			v.Tier = m[1]
		}
	}
	for _, n := range names {
		if strings.EqualFold(n, strings.TrimSpace(v.Tier)) {
			return n
		}
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
