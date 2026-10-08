// Package decide asks a router's decision model which tier a call needs,
// when the classifier is unsure (project.md §6.2): the model reads the
// tiers' descriptions and examples and an excerpt of the call, and answers
// a tier, held to a schema where it takes one. A hard timeout falls back to
// the default tier; answers are cached per router, family and message.
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
}

// New returns a Decider sending calls with transport (safehttp's).
func New(transport http.RoundTripper) *Decider {
	// Redirects aren't followed: a provider's key goes to its own endpoint
	// only (net/http keeps X-Api-Key on a redirect to another host).
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &Decider{client: client, now: time.Now,
		cache: memcache.New[[32]byte, string](CacheTTL, time.Second, 100_000)}
}

// Decide implements resolver.Decider: the tier the model picks ("" when it
// picks none), within the question's timeout. A cached answer costs
// nothing and returns no usage.
func (d *Decider) Decide(ctx context.Context, q resolver.Question) (string, wire.Usage, error) {
	key := sha256.Sum256([]byte(q.Project.String() + "\x00" + q.Router + "\x00" + q.Family + "\x00" + q.Instructions + "\x00" + q.Request))
	var usage wire.Usage
	tier, err := d.cache.Get(key, d.now(), func() (string, error) {
		t, u, err := d.ask(ctx, q)
		usage = u
		if err != nil {
			return "", memcache.Uncached{Err: err}
		}
		return t, nil
	})
	return tier, usage, err
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
	var req map[string]any
	if f.Anthropic() {
		req = map[string]any{"model": t.Model, "max_tokens": 64, "temperature": 0, "system": system,
			"messages": []map[string]string{{"role": "user", "content": user}}}
		if q.Schema {
			req["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema}}
		}
	} else {
		req = map[string]any{"model": t.Model, "temperature": 0, "messages": []map[string]string{
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
