package livecheck_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/events"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// maxClaudeCodeSpend stops TestClaudeCode: a dollar or two is what its
// four sessions cost.
const maxClaudeCodeSpend = 3.00 // dollars

// TestClaudeCode runs Claude Code, Anthropic's coding agent, headless on
// a small Go repository, as a developer would: two asks, each a tool loop
// (read, edit, run the tests). The same session runs four times on the
// owner's key (PROMPTDAM_TEST_ANTHROPIC_KEY): straight to Anthropic, then
// through the gateway with the firewall off, at its defaults (secrets
// blocked) and redacting secrets. The repository holds a payment key
// in config.go, as real ones do. Each run is checked (tests pass, the key
// moved to the environment, no placeholder written into the code), and the
// request log is compared with what Claude Code says it spent. An upstream
// recorder counts the requests that carried the key out of the gateway.
//
// Run: task demo:claudecode (the `claude` CLI on PATH, the stack up). It
// stops past maxClaudeCodeSpend.
func TestClaudeCode(t *testing.T) {
	key := os.Getenv("PROMPTDAM_TEST_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("no PROMPTDAM_TEST_ANTHROPIC_KEY (.env)")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("no claude CLI on PATH")
	}
	ctx := context.Background()
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
	w.Grant(catalog.PromptCaching)
	w.Serves("main", "fake-model") // the World's fake provider answers the firewall's probes
	if err := w.App.Prices.Sync(ctx, true); err != nil {
		t.Fatalf("syncing prices: %v", err)
	}
	up := newRecorder(t, "https://api.anthropic.com", secret)
	if _, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, providers.Input{Name: "anthropic", Kind: "anthropic",
		BaseURL: up.srv.URL + "/v1", APIKey: &key, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
		t.Fatal(err)
	}
	w.Eventually(func() bool {
		status, _, _ := w.Call("/v1/messages", `{"model":"claude-haiku-5-5","max_tokens":5,"messages":[{"role":"user","content":"Say ok."}]}`)
		return status == 200
	})

	off, defaults, redact := firewall.Defaults(), firewall.Defaults(), firewall.Defaults()
	off.Enabled = false
	redact.Secrets = firewall.Redact
	defaults.Excerpts, redact.Excerpts = true, true
	scenarios := []ccScenario{
		{Name: "direct", Path: "Claude Code → Anthropic"},
		{Name: "plain", Path: "via Promptdam, firewall off", Firewall: &off},
		{Name: "block", Path: "via Promptdam, firewall defaults (secrets blocked)", Firewall: &defaults},
		{Name: "redact", Path: "via Promptdam, secrets redacted", Firewall: &redact},
	}
	var spent float64
	for i := range scenarios {
		sc := &scenarios[i]
		if spent > maxClaudeCodeSpend {
			t.Fatalf("spent $%.4f by Claude Code's reckoning, past $%.2f", spent, maxClaudeCodeSpend)
		}
		base, apiKey := "", key
		if sc.Firewall != nil {
			settleFirewall(t, w, *sc.Firewall)
			base, apiKey = w.Gateway.URL, w.Raw
		}
		before, records := up.carried.Load(), len(w.Records())
		calls, used := up.seen()
		sc.From = time.Now()
		runScenario(t, bin, sc, base, apiKey)
		sc.To = time.Now()
		sc.Carried = up.carried.Load() - before
		sc.Hits = hitsOf(w.Records()[records:])
		calls2, used2 := up.seen()
		sc.Upstream = ccUsage{Input: used2.Input - used.Input, Read: used2.Read - used.Read, Write: used2.Write - used.Write,
			Output: used2.Output - used.Output}
		sc.UpstreamCalls = calls2 - calls
		spent += sc.said().Cost
	}

	if err := w.Log.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	out := &strings.Builder{}
	fmt.Fprintf(out, "\n| Scenario | Works | Claude Code says | Ledger | Calls | Models | Cache read | Wall | Gateway adds to the first byte (median) | Firewall | Turns (human, conversations) | Key sent upstream |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for i := range scenarios {
		sc := &scenarios[i]
		sc.Ledger = ledgerOf(t, w, sc)
		l, wall := sc.Ledger, time.Duration(0)
		for _, r := range sc.Runs {
			wall += r.Wall
		}
		ledger, calls, cache, added, fw, turns, carried := "-", "-", "-", "-", "-", "-", "-"
		if sc.Firewall != nil {
			ledger = fmt.Sprintf("$%.4f", l.Cost)
			calls = fmt.Sprintf("%d (%s)", l.Calls, l.Statuses)
			cache = fmt.Sprintf("%.0f%%", pct(l.Read, l.Input))
			added = fmt.Sprintf("%d ms", l.Added)
			fw = l.Firewall
			turns = fmt.Sprintf("%d, %d", l.HumanTurns, l.Conversations)
			carried = fmt.Sprintf("%d of %d", sc.Carried, l.Calls)
		}
		models := strings.Join(sc.models(), ", ")
		fmt.Fprintf(out, "| %s | %s | $%.4f | %s | %s | %s | %s | %.0fs | %s | %s | %s | %s |\n", sc.Name, sc.verdict(), sc.said().Cost, ledger, calls,
			models, cache, wall.Seconds(), added, fw, turns, carried)
	}
	fmt.Fprintf(out, "\n| Scenario | Counted by | Input (cache read, written) | Output |\n|---|---|---|---|\n")
	for i := range scenarios {
		sc := &scenarios[i]
		u := sc.said()
		fmt.Fprintf(out, "| %s | Claude Code | %d (%d, %d) | %d |\n", sc.Name, u.Input, u.Read, u.Write, u.Output)
		if sc.Firewall != nil {
			l := sc.Ledger
			fmt.Fprintf(out, "| %s | ledger, %d calls | %d (%d, %d) | %d |\n", sc.Name, l.Calls, l.Input, l.Read, l.Write, l.Output)
			u := sc.Upstream
			fmt.Fprintf(out, "| %s | Anthropic, %d calls | %d (%d, %d) | %d |\n", sc.Name, sc.UpstreamCalls, u.Input, u.Read, u.Write, u.Output)
		}
	}
	if m, ok := w.App.Prices.Current().Lookup("anthropic", "claude-sonnet-5-5"); ok {
		fmt.Fprintf(out, "\nThe ledger's Sonnet 5.5 per million: input $%.2f, cache read $%.2f, write $%.2f (1h $%.2f), output $%.2f\n",
			m.Input, m.CachedInput, m.CacheWrite, m.CacheWrite1h, m.Output)
	}
	for _, sc := range scenarios {
		for _, r := range sc.Runs {
			fmt.Fprintf(out, "\n%s, ask %d (%d steps, %s): %q", sc.Name, r.Ask, r.Result.Turns, r.Result.Subtype, clip(r.Result.Text, 300))
			for _, d := range r.Result.Denied {
				fmt.Fprintf(out, "\n  denied %s %v", d.Tool, d.Input)
			}
			if r.Errors != "" {
				fmt.Fprintf(out, "\n  API errors: %s", r.Errors)
			}
		}
		for _, h := range sc.Hits {
			fmt.Fprintf(out, "\n%s, firewall: %s", sc.Name, h)
		}
		for _, p := range sc.Problems {
			fmt.Fprintf(out, "\n%s: %s", sc.Name, p)
		}
	}
	t.Log(out.String())
	if spent > maxClaudeCodeSpend {
		t.Fatalf("spent $%.4f, past $%.2f", spent, maxClaudeCodeSpend)
	}
	for _, sc := range scenarios[:2] { // with nothing filtered, both must do the job
		if len(sc.Problems) > 0 {
			t.Errorf("%s: %s", sc.Name, strings.Join(sc.Problems, "; "))
		}
	}
}

// secret is the payment key the repository holds; made at run time so no
// scanner of this file mistakes it for a real one.
var secret = func() string {
	sum := sha256.Sum256([]byte("promptdam claude code livecheck"))
	return "sk_" + "live_" + hex.EncodeToString(sum[:16])
}()

// asks are the developer's two messages.
var asks = []string{
	"The Go tests fail (go test ./...). Find the bug and fix it.",
	"Move the hard-coded API key in config.go to the BILLING_API_KEY environment variable. Keep the tests passing.",
}

// fixture is the repository, a small billing package with a bug that
// fails both tests.
func fixture() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/billing\n\ngo 1.27\n",
		"billing.go": `// Package billing prices orders and charges them.
package billing

// Item is a line of an order.
type Item struct {
	Name  string
	Cents int64
	Qty   int
}

// Total is what items cost in cents, with tax at ratePercent added.
func Total(items []Item, ratePercent int64) int64 {
	var sum int64
	for i := 1; i < len(items); i++ {
		sum += items[i].Cents * int64(items[i].Qty)
	}
	return sum + sum*ratePercent/100
}

// Request is a customer's request to pay.
type Request struct {
	Customer string
	Token    string
}

// Charge is a payment sent to the provider.
type Charge struct {
	Customer string
	Token    string
	Cents    int64
	Auth     string
}

// NewCharge prepares the charge of an order.
func NewCharge(req Request, items []Item) Charge {
	return Charge{Customer: req.Customer, Token: req.Token, Cents: Total(items, 20), Auth: "Bearer " + apiKey}
}
`,
		"billing_test.go": `package billing

import "testing"

func TestTotal(t *testing.T) {
	items := []Item{{"pen", 250, 2}, {"pad", 400, 1}}
	if got := Total(items, 20); got != 1080 {
		t.Fatalf("Total = %d, want 1080", got)
	}
}

func TestNewCharge(t *testing.T) {
	c := NewCharge(Request{Customer: "c1", Token: "tok_1"}, []Item{{"pen", 100, 1}})
	if c.Cents != 120 || c.Customer != "c1" {
		t.Fatalf("charge %+v", c)
	}
}
`,
		"config.go": "package billing\n\n// apiKey is the payment provider's key.\nconst apiKey = \"" + secret + "\"\n",
	}
}

type ccScenario struct {
	Name, Path string
	// Firewall is the project's firewall; nil runs straight to Anthropic.
	Firewall *firewall.Settings
	From, To time.Time
	Runs     []ccRun
	Problems []string
	// Carried counts the upstream requests that held the key.
	Carried int64
	// Hits are the firewall's findings, where they were and around what.
	Hits []string
	// Upstream is what Anthropic answered through the gateway.
	Upstream      ccUsage
	UpstreamCalls int64
	Ledger        ccLedger
}

// said is the session's tokens and cost by Claude Code's reckoning: a
// resumed session reports its whole self, so the last ask's report.
func (sc *ccScenario) said() ccUsage {
	var u ccUsage
	if len(sc.Runs) == 0 {
		return u
	}
	for _, m := range sc.Runs[len(sc.Runs)-1].Result.Models {
		u.Input += m.Input + m.Read + m.Write
		u.Read, u.Write, u.Output, u.Cost = u.Read+m.Read, u.Write+m.Write, u.Output+m.Output, u.Cost+m.Cost
	}
	return u
}

func (sc *ccScenario) verdict() string {
	if len(sc.Problems) == 0 {
		return "yes"
	}
	return "no"
}

func (sc *ccScenario) models() []string {
	seen := map[string]bool{}
	for _, r := range sc.Runs {
		for m := range r.Result.Models {
			seen[m] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

type ccRun struct {
	Ask    int
	Result ccResult
	Wall   time.Duration
	// Errors are the API errors in Claude Code's debug log.
	Errors string
}

// ccResult is Claude Code's --output-format json.
type ccResult struct {
	Subtype string             `json:"subtype"`
	IsError bool               `json:"is_error"`
	Turns   int                `json:"num_turns"`
	Text    string             `json:"result"`
	Session string             `json:"session_id"`
	Cost    float64            `json:"total_cost_usd"`
	Models  map[string]ccUsage `json:"modelUsage"`
	Denied  []struct {
		Tool  string         `json:"tool_name"`
		Input map[string]any `json:"tool_input"`
	} `json:"permission_denials"`
}

// ccUsage is a model's tokens and cost in Claude Code's own reckoning.
type ccUsage struct {
	Input  int64   `json:"inputTokens"`
	Output int64   `json:"outputTokens"`
	Read   int64   `json:"cacheReadInputTokens"`
	Write  int64   `json:"cacheCreationInputTokens"`
	Cost   float64 `json:"costUSD"`
}

// runScenario runs both asks in one Claude Code session on a fresh copy
// of the repository, then checks what it left.
func runScenario(t *testing.T, bin string, sc *ccScenario, base, apiKey string) {
	t.Helper()
	dir, config := t.TempDir(), t.TempDir()
	for name, body := range fixture() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"CLAUDE_CONFIG_DIR=" + config, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1",
		"GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=", "ANTHROPIC_API_KEY=" + apiKey}
	if base != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+base)
	}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ANTHROPIC_") && !strings.HasPrefix(kv, "CLAUDE") && !strings.HasPrefix(kv, "PROMPTDAM_") &&
			!strings.HasPrefix(kv, "KEEL_") && !strings.HasPrefix(kv, "GO") {
			env = append(env, kv)
		}
	}
	session := newUUID(t)
	for i, ask := range asks {
		args := []string{"-p", "--bare", "--model", "sonnet", "--output-format", "json", "--max-budget-usd", "0.75",
			"--permission-mode", "dontAsk", "--debug-file", filepath.Join(config, fmt.Sprintf("debug-%d.log", i))}
		if i == 0 {
			args = append(args, "--session-id", session)
		} else {
			args = append(args, "--resume", session)
		}
		args = append(args, "--allowedTools", "Read", "Edit", "Write", "Glob", "Grep", "Bash(ls *)", "Bash(find *)",
			"Bash(grep *)", "Bash(cat *)", "Bash(head *)", "Bash(tail *)", "Bash(wc *)", "Bash(git *)", "Bash(go *)", "Bash(gofmt *)")
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env, cmd.Stdin = dir, env, strings.NewReader(ask)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		start := time.Now()
		runErr := cmd.Run()
		cancel()
		r := ccRun{Ask: i + 1, Wall: time.Since(start), Errors: apiErrors(filepath.Join(config, fmt.Sprintf("debug-%d.log", i)))}
		if err := json.Unmarshal(stdout.Bytes(), &r.Result); err != nil {
			sc.Problems = append(sc.Problems, fmt.Sprintf("ask %d: no result (%v): %s %s", i+1, runErr, clip(stdout.String(), 300),
				clip(stderr.String(), 300)))
		} else if r.Result.IsError {
			sc.Problems = append(sc.Problems, fmt.Sprintf("ask %d ended with an error (%s)", i+1, r.Result.Subtype))
		}
		sc.Runs = append(sc.Runs, r)
	}

	test := exec.CommandContext(t.Context(), "go", "test", "./...")
	test.Dir, test.Env = dir, env
	if out, err := test.CombinedOutput(); err != nil {
		sc.Problems = append(sc.Problems, "go test fails: "+clip(string(out), 200))
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		name := filepath.Base(f)
		switch {
		case bytes.Contains(b, []byte(secret)):
			sc.Problems = append(sc.Problems, name+" still holds the key")
		case bytes.Contains(b, []byte("REDACTED")), bytes.Contains(b, []byte("⟦")):
			sc.Problems = append(sc.Problems, name+" has a firewall placeholder written into it")
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.go")); !bytes.Contains(b, []byte("BILLING_API_KEY")) {
		if b2, _ := os.ReadFile(filepath.Join(dir, "billing.go")); !bytes.Contains(b2, []byte("BILLING_API_KEY")) {
			sc.Problems = append(sc.Problems, "the key isn't read from BILLING_API_KEY")
		}
	}
}

// settleFirewall saves the project's firewall and waits until the gateway
// applies it, probing the free fake model with the key in the message.
func settleFirewall(t *testing.T, w *gatewaytest.World, s firewall.Settings) {
	t.Helper()
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, s); err != nil {
		t.Fatal(err)
	}
	want := ""
	switch {
	case !s.Enabled:
	case s.Secrets == firewall.Block:
		want = firewall.Block
	default:
		want = s.Secrets
	}
	probe := `{"model":"fake-model","max_tokens":5,"messages":[{"role":"user","content":"const apiKey = \"` + secret + `\""}]}`
	w.Eventually(func() bool {
		status, _, body := w.Call("/v1/messages", probe)
		if want == firewall.Block {
			return status != 200 && strings.Contains(body, "firewall")
		}
		recs := w.Records()
		if status != 200 || len(recs) == 0 {
			return false
		}
		got := ""
		if m, ok := recs[len(recs)-1].Meta.(*resolver.Meta); ok && m.Firewall != nil {
			got = m.Firewall.Action()
		}
		return got == want
	})
}

// hitsOf lists the firewall's distinct findings in recs.
func hitsOf(recs []gateway.Record) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range recs {
		m, ok := r.Meta.(*resolver.Meta)
		if !ok || m.Firewall == nil {
			continue
		}
		for _, h := range m.Firewall.Hits {
			s := fmt.Sprintf("%s %s in %s: %q", h.Action, h.Detector, h.Location, clip(h.Excerpt, 160))
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// ccLedger is a scenario's calls in the request log.
type ccLedger struct {
	Calls, HumanTurns, Conversations int
	Input, Read, Write, Output       int64
	Cost                             float64
	Added                            int64
	Statuses, Firewall               string
}

func ledgerOf(t *testing.T, w *gatewaytest.World, sc *ccScenario) ccLedger {
	t.Helper()
	var rows []struct {
		Kind     string   `ch:"kind"`
		Status   uint16   `ch:"status"`
		Input    uint32   `ch:"input_tokens"`
		Read     uint32   `ch:"cache_read_tokens"`
		Cost     int64    `ch:"cost_micros"`
		Error    string   `ch:"error_code"`
		Human    uint16   `ch:"human_turns"`
		Conv     string   `ch:"conversation"`
		TTFB     uint32   `ch:"ttfb_ms"`
		Output   uint32   `ch:"output_tokens"`
		Write    uint32   `ch:"cache_write_tokens"`
		Upstream uint32   `ch:"upstream_ms"`
		Action   string   `ch:"firewall_action"`
		Kinds    []string `ch:"firewall_kinds"`
	}
	if err := w.C.Events.Select(context.Background(), &rows, events.Project(w.Project), `SELECT kind, status, input_tokens,
		cache_read_tokens, cost_micros, error_code, human_turns, conversation, ttfb_ms, upstream_ms,
		output_tokens, cache_write_tokens, firewall_action,
		firewall_kinds FROM requests WHERE {tenant} AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)
		ORDER BY ts`, sc.From.UnixMilli(), sc.To.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	var l ccLedger
	statuses, actions, convs := map[string]int{}, map[string]int{}, map[string]bool{}
	var added []int64
	for _, r := range rows {
		l.Calls++
		l.Input += int64(r.Input)
		l.Read += int64(r.Read)
		l.Cost += usd(r.Cost)
		l.HumanTurns = max(l.HumanTurns, int(r.Human))
		if r.Conv != "" {
			convs[r.Conv] = true
		}
		s := fmt.Sprint(r.Status)
		if r.Error != "" {
			s += " " + r.Error
		}
		statuses[s]++
		if r.Action != "" {
			actions[r.Action+" "+strings.Join(r.Kinds, "+")]++
		}
		l.Write += int64(r.Write)
		l.Output += int64(r.Output)
		if r.Upstream > 0 && r.TTFB >= r.Upstream {
			added = append(added, int64(r.TTFB-r.Upstream))
		}
	}
	l.Conversations = len(convs)
	l.Statuses, l.Firewall = counts(statuses), counts(actions)
	if l.Firewall == "" {
		l.Firewall = "nothing"
	}
	if len(added) > 0 {
		slices.Sort(added)
		l.Added = added[len(added)/2]
	}
	return l
}

// counts writes a tally as "a ×3, b ×1".
func counts(m map[string]int) string {
	var parts []string
	for k, n := range m {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, n))
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}

// apiErrors are the lines of a Claude Code debug log that report a
// failed API call.
func apiErrors(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var out []string
	for line := range strings.Lines(string(b)) {
		l := strings.ToLower(line)
		if strings.Contains(l, "api error") || strings.Contains(l, "status 4") || strings.Contains(l, "status 5") ||
			strings.Contains(l, "firewall") {
			out = append(out, clip(strings.TrimSpace(line), 240))
		}
	}
	if len(out) > 5 {
		out = append(out[:5], fmt.Sprintf("(%d more)", len(out)-5))
	}
	return strings.Join(out, " / ")
}

// recorder is a reverse proxy in front of a provider that counts the
// requests whose body holds needle.
type recorder struct {
	srv     *httptest.Server
	carried atomic.Int64
	mu      sync.Mutex
	// calls and usage are what Anthropic itself answered.
	calls int64
	usage ccUsage
}

// tally adds the usage in an Anthropic answer, streamed or not.
func (rec *recorder) tally(body []byte) {
	type usage struct {
		Input  int64 `json:"input_tokens"`
		Read   int64 `json:"cache_read_input_tokens"`
		Write  int64 `json:"cache_creation_input_tokens"`
		Output int64 `json:"output_tokens"`
	}
	var u usage
	for line := range bytes.Lines(body) {
		line = bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data: "))
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Usage *usage `json:"usage"`
			} `json:"message"`
			Usage *usage `json:"usage"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "message_start" && ev.Message.Usage != nil:
			u = *ev.Message.Usage
		case ev.Type == "message_delta" && ev.Usage != nil:
			u.Output = ev.Usage.Output
		case ev.Type == "message" && ev.Usage != nil:
			u = *ev.Usage
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.calls++
	rec.usage.Input += u.Input + u.Read + u.Write
	rec.usage.Read, rec.usage.Write, rec.usage.Output = rec.usage.Read+u.Read, rec.usage.Write+u.Write, rec.usage.Output+u.Output
}

// seen is what Anthropic has answered so far.
func (rec *recorder) seen() (int64, ccUsage) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.calls, rec.usage
}

// teeWriter keeps a copy of an answer while it streams.
type teeWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (t *teeWriter) Write(b []byte) (int, error) {
	t.buf.Write(b)
	return t.ResponseWriter.Write(b)
}

func (t *teeWriter) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func newRecorder(t *testing.T, target, needle string) *recorder {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(u)
		r.Out.Host = u.Host
	}, FlushInterval: -1}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(needle)) {
			rec.carried.Add(1)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		tee := &teeWriter{ResponseWriter: rw}
		proxy.ServeHTTP(tee, r)
		if strings.HasSuffix(r.URL.Path, "/messages") {
			rec.tally(tee.buf.Bytes())
		}
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		t.Fatal(err)
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
