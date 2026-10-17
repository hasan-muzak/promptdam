// Package bench measures what the gateway adds to an LLM call: the same
// calls go to a fake provider directly and through the gateway, and the
// latencies are compared. `task promptdam:bench` runs it.
package bench

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/safehttp"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// Budget is the gateway's overhead budget at p99 (project.md §6.1).
const Budget = 40 * time.Millisecond

type stack struct {
	upstream, gateway *httptest.Server
}

func newStack(tb testing.TB, opts fakeupstream.Options) *stack {
	up := httptest.NewServer(fakeupstream.New(opts))
	tb.Cleanup(up.Close)
	gw := httptest.NewServer(gateway.New(gateway.Config{
		Resolver:  gateway.Static{{Provider: "openai", BaseURL: up.URL + "/v1"}},
		Transport: safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: true}),
	}))
	tb.Cleanup(gw.Close)
	return &stack{upstream: up, gateway: gw}
}

var client = &http.Client{Transport: &http.Transport{MaxIdleConns: 1024, MaxIdleConnsPerHost: 1024}}

// timing is one call's time to first byte and total time.
type timing struct{ ttfb, total time.Duration }

// A target is where calls go: a provider, or a gateway with its key.
type target struct{ url, key string }

func do(ctx context.Context, to target, body string) (timing, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to.url, strings.NewReader(body))
	if err != nil {
		return timing{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if to.key != "" {
		req.Header.Set("Authorization", "Bearer "+to.key)
	}
	res, err := client.Do(req)
	if err != nil {
		return timing{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var one [1]byte
	if _, err := io.ReadFull(res.Body, one[:]); err != nil {
		return timing{}, err
	}
	t := timing{ttfb: time.Since(start)}
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		return timing{}, err
	}
	t.total = time.Since(start)
	if res.StatusCode != http.StatusOK {
		return t, fmt.Errorf("status %d", res.StatusCode)
	}
	return t, nil
}

// run makes n calls to each target, concurrency at a time, alternating
// between them so both see the same machine (GC, other load) at the same
// time, and returns each target's timings.
func run(tb testing.TB, targets []target, body string, n, concurrency int) [][]timing {
	out := make([][]timing, len(targets))
	for i := range out {
		out[i] = make([]timing, n)
	}
	var wg sync.WaitGroup
	next := make(chan int)
	errs := make(chan error, n*len(targets))
	for range concurrency {
		wg.Go(func() {
			for i := range next {
				u := i % len(targets)
				t, err := do(context.Background(), targets[u], body)
				if err != nil {
					errs <- err
				}
				out[u][i/len(targets)] = t
			}
		})
	}
	for i := range n * len(targets) {
		next <- i
	}
	close(next)
	wg.Wait()
	close(errs)
	for err := range errs {
		tb.Fatal(err)
	}
	return out
}

func percentile(ds []time.Duration, p float64) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*p))]
}

func split(ts []timing) (ttfb, total []time.Duration) {
	for _, t := range ts {
		ttfb, total = append(ttfb, t.ttfb), append(total, t.total)
	}
	return ttfb, total
}

// overheadCases are the calls the budget is measured on.
var overheadCases = []struct{ name, path, body string }{
	{"chat stream", "/v1/chat/completions", `{"model":"gpt-5-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	{"chat", "/v1/chat/completions", `{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`},
	{"messages stream", "/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
}

// A provider that answers after 20 ms and streams 50 chunks. The chunks
// come back to back: timers under load are too jittery to space them
// without drowning what is measured.
var overheadUpstream = fakeupstream.Options{TTFB: 20 * time.Millisecond, Chunks: 50}

// TestOverheadBudget is the S0 exit: the p99 of streamed calls through the
// gateway is within Budget of the same calls made directly. It measures
// the gateway alone (a static route: its floor) and the gateway as it
// serves customers: the key checked, the project's snapshot, quota and
// rate counters on Valkey, the firewall at its defaults, the router and
// the request log written to ClickHouse. Timing tests need a quiet
// machine, so it runs only when asked.
func TestOverheadBudget(t *testing.T) {
	if os.Getenv("PROMPTDAM_BENCH") == "" {
		t.Skip("set PROMPTDAM_BENCH=1 to measure (task promptdam:bench)")
	}
	t.Run("static", func(t *testing.T) {
		s := newStack(t, overheadUpstream)
		for _, c := range overheadCases {
			measure(t, c.name, target{url: s.upstream.URL + c.path}, target{url: s.gateway.URL + c.path}, c.body)
		}
	})
	t.Run("full path", func(t *testing.T) {
		w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Upstream: overheadUpstream})
		w.Provider("claude", providers.FormatAnthropic, []string{"claude-haiku-4-5"})
		w.Serves("main", "gpt-5-mini")
		for _, c := range overheadCases {
			measure(t, c.name, target{url: w.Upstream.URL + c.path}, target{url: w.Gateway.URL + c.path, key: w.Raw}, c.body)
		}
	})
}

// measure makes the same calls directly and through the gateway and fails
// if the gateway adds more than Budget at p99.
func measure(t *testing.T, name string, direct, via target, body string) {
	t.Helper()
	const n, concurrency = 3000, 32
	targets := []target{direct, via}
	run(t, targets, body, concurrency, concurrency) // warm both paths' connections
	res := run(t, targets, body, n, concurrency)
	directTTFB, directTotal := split(res[0])
	viaTTFB, viaTotal := split(res[1])
	for _, p := range []float64{0.5, 0.9, 0.99} {
		t.Logf("%-16s p%-2.0f  ttfb direct %8s via %8s (+%s)   total direct %8s via %8s (+%s)", name, p*100,
			percentile(directTTFB, p).Round(10*time.Microsecond), percentile(viaTTFB, p).Round(10*time.Microsecond),
			(percentile(viaTTFB, p) - percentile(directTTFB, p)).Round(10*time.Microsecond),
			percentile(directTotal, p).Round(10*time.Microsecond), percentile(viaTotal, p).Round(10*time.Microsecond),
			(percentile(viaTotal, p) - percentile(directTotal, p)).Round(10*time.Microsecond))
	}
	if over := percentile(viaTotal, 0.99) - percentile(directTotal, 0.99); over > Budget {
		t.Errorf("%s: p99 overhead %s is over the %s budget", name, over, Budget)
	}
}

// BenchmarkGatewayChatStream is the gateway's own cost per streamed call,
// with a provider that answers at once (compare BenchmarkDirectChatStream).
func BenchmarkGatewayChatStream(b *testing.B) { benchmark(b, true) }

// BenchmarkDirectChatStream is the same calls without the gateway.
func BenchmarkDirectChatStream(b *testing.B) { benchmark(b, false) }

func benchmark(b *testing.B, viaGateway bool) {
	s := newStack(b, fakeupstream.Options{Chunks: 50})
	to := target{url: s.upstream.URL + "/v1/chat/completions"}
	if viaGateway {
		to.url = s.gateway.URL + "/v1/chat/completions"
	}
	body := `{"model":"gpt-5-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := do(context.Background(), to, body); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
