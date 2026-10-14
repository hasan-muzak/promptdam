// Package decide asks a router's decision model which tier a call needs,
// when the classifier is unsure (project.md §6.2): the model reads the
// tiers' descriptions and examples and an excerpt of the call, and answers
// a tier, held to a schema where it takes one. A hard timeout falls back to
// the default tier. Answers are cached per project, router, family, the
// question and the tiers as the model read them, so an edited router never
// reuses an answer about its old tiers; identical questions asked at once
// share one call.
package decide

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/memcache"

	"github.com/hasan-muzak/promptdam/server/features/resolver"
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
	cache  *memcache.Cache[[32]byte, string]
	now    func() time.Time

	mu       sync.Mutex
	inflight map[[32]byte]*flight
}

// flight is a question being asked: callers asking the same wait for it.
type flight struct {
	done chan struct{}
	tier string
	err  error
}

// New returns a Decider sending calls with transport (safehttp's).
func New(transport http.RoundTripper) *Decider {
	// Redirects aren't followed: a provider's key goes to its own endpoint
	// only (net/http keeps X-Api-Key on a redirect to another host).
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &Decider{client: client, now: time.Now, inflight: map[[32]byte]*flight{},
		cache: memcache.New[[32]byte, string](CacheTTL, time.Second, 100_000)}
}

// Decide implements resolver.Decider: the tier the model picks ("" when it
// picks none), within the question's timeout. Only the call that asked
// pays: a cached answer, or one shared with a question asked at the same
// time, returns no usage. An answer naming no tier isn't cached.
func (d *Decider) Decide(ctx context.Context, q resolver.Question) (string, wire.Usage, error) {
	key := keyOf(q)
	if tier, ok := d.cache.Peek(key, d.now()); ok {
		return tier, wire.Usage{}, nil
	}
	d.mu.Lock()
	if f, ok := d.inflight[key]; ok {
		d.mu.Unlock()
		wait, cancel := context.WithTimeout(ctx, q.Timeout)
		defer cancel()
		select {
		case <-f.done:
			return f.tier, wire.Usage{}, f.err
		case <-wait.Done():
			return "", wire.Usage{}, wait.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	d.inflight[key] = f
	d.mu.Unlock()

	tier, usage, err := d.ask(ctx, q)
	if err == nil && tier != "" {
		d.cache.Set(key, tier, d.now())
	}
	f.tier, f.err = tier, err
	d.mu.Lock()
	delete(d.inflight, key)
	d.mu.Unlock()
	close(f.done)
	return tier, usage, err
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

func (d *Decider) ask(ctx context.Context, q resolver.Question) (string, wire.Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, q.Timeout)
	defer cancel()
	t := q.Target
	f := wire.OpenAIChat
	if t.Format != 0 {
		f = t.Format
	}
	names := make([]string, len(q.Tiers))
	for i, tier := range q.Tiers {
		names[i] = tier.Name
	}
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
		if t.Kind == "openai" {
			req["max_completion_tokens"] = 256
		} else {
			req["max_tokens"] = 64
		}
		if q.Schema {
			req["response_format"] = map[string]any{"type": "json_schema",
				"json_schema": map[string]any{"name": "tier", "schema": schema, "strict": true}}
		}
	}
	var payload bytes.Buffer
	enc := json.NewEncoder(&payload)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(req); err != nil {
		return "", wire.Usage{}, err
	}
	path := "/chat/completions"
	if f.Anthropic() {
		path = "/messages"
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+path, &payload)
	if err != nil {
		return "", wire.Usage{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept-Encoding", "identity")
	if f.Anthropic() {
		hreq.Header.Set("Anthropic-Version", "2023-06-01")
	}
	for k, v := range t.Header {
		hreq.Header[k] = v
	}
	res, err := d.client.Do(hreq)
	if err != nil {
		return "", wire.Usage{}, fmt.Errorf("decide: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", wire.Usage{}, fmt.Errorf("decide: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return "", wire.Usage{}, fmt.Errorf("decide: %s answered %d", t.Provider, res.StatusCode)
	}
	meter := wire.NewMeter(f)
	meter.Body(body)
	usage, _ := meter.Usage()
	return pick(f, body, names), usage, nil
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
