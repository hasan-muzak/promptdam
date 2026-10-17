// Command load sends a steady stream of LLM calls to a gateway and,
// mixed in, the same calls straight to the provider behind it (the fake,
// bench/cmd/fake-upstream), so what the gateway adds is measured at the
// same moment, under the same load. It is open-loop: calls leave on a
// schedule whether or not earlier ones answered, and each is timed from
// when it was due, so a gateway that falls behind shows it instead of
// slowing the load down. While it runs it samples the gateways' expvar
// (PROMPTDAM_GATEWAY_DEBUG_ADDR): goroutines, memory, dropped log rows.
//
//	go run ./bench/cmd/load -target http://localhost:8280 -key pd_sk_… -rate 1000 -duration 2m
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	target := flag.String("target", "http://localhost:8282", "the gateway (or the proxy in front of its replicas)")
	direct := flag.String("direct", "http://localhost:8299", "the provider behind it, called directly for the comparison")
	key := flag.String("key", os.Getenv("PROMPTDAM_LOAD_KEY"), "a gateway key (pd_sk_…; default $PROMPTDAM_LOAD_KEY)")
	rate := flag.Float64("rate", 1000, "calls per second (both paths together)")
	duration := flag.Duration("duration", time.Minute, "how long to send")
	mix := flag.String("mix", "chat-stream=60,agent=20,chat=15,agent-large=5", "shapes and their weights")
	share := flag.Float64("direct-share", 0.2, "the part of calls sent straight to the provider")
	debug := flag.String("debug", "", "the gateways' debug addresses to sample, comma-separated (127.0.0.1:6060,…)")
	flag.Parse()
	if *key == "" {
		fmt.Fprintln(os.Stderr, "load: -key (or PROMPTDAM_LOAD_KEY) is needed")
		os.Exit(2)
	}
	picks, err := weights(*mix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	r := &run{key: *key, target: strings.TrimRight(*target, "/"), direct: strings.TrimRight(*direct, "/"),
		client: client(), stats: map[string]*stat{}, windows: map[int]map[bool][]time.Duration{}}
	samples := sample(ctx, *debug)
	r.send(ctx, *rate, *duration, picks, *share)
	r.wait.Wait()
	var out strings.Builder
	r.report(&out, *rate, *duration)
	samples.report(&out)
	fmt.Print(out.String())
}

// client is one keep-alive pool for every call, sized for the open streams
// of a thousand calls a second.
func client() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{
		MaxIdleConns: 8192, MaxIdleConnsPerHost: 4096, IdleConnTimeout: 90 * time.Second, DisableCompression: true,
		ForceAttemptHTTP2: false,
	}}
}

// weights reads "a=60,b=40" into a list to pick from.
func weights(mix string) ([]shape, error) {
	var out []shape
	for part := range strings.SplitSeq(mix, ",") {
		name, w, _ := strings.Cut(strings.TrimSpace(part), "=")
		s, ok := shapes[name]
		n, err := strconv.Atoi(w)
		if !ok || err != nil || n < 0 {
			return nil, fmt.Errorf("-mix: %q isn't shape=weight (shapes: chat-stream, chat, agent, agent-large)", part)
		}
		for range n {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-mix: no weight")
	}
	return out, nil
}

type run struct {
	key, target, direct string
	client              *http.Client
	wait                sync.WaitGroup
	sent                atomic.Int64
	// late is how far behind schedule the sender fell, at most: past a few
	// milliseconds the load machine, not the gateway, is the limit.
	late atomic.Int64

	mu    sync.Mutex
	stats map[string]*stat // by shape and path ("agent via gateway")
	start time.Time
	// windows are first-byte times by ten seconds of the run and path: a
	// stall shows as one bad window, steady load as even ones.
	windows map[int]map[bool][]time.Duration
}

// stat is one shape's calls on one path.
type stat struct {
	ttfb, total []time.Duration
	statuses    map[string]int
	bytes       int64
}

// send starts calls on schedule until the duration is over.
func (r *run) send(ctx context.Context, rate float64, d time.Duration, picks []shape, share float64) {
	every := time.Duration(float64(time.Second) / rate)
	start := time.Now()
	r.start = start
	for i := int64(0); ; i++ {
		due := start.Add(time.Duration(i) * every)
		if due.Sub(start) >= d || ctx.Err() != nil {
			return
		}
		if wait := time.Until(due); wait > 0 {
			time.Sleep(wait)
		} else if -wait > time.Duration(r.late.Load()) {
			r.late.Store(int64(-wait))
		}
		s := picks[rand.IntN(len(picks))]
		viaGateway := rand.Float64() >= share
		r.wait.Go(func() { r.call(s, i, due, viaGateway) })
	}
}

// call sends one call and times it from when it was due: the first byte
// of the answer, then its end.
func (r *run) call(s shape, i int64, due time.Time, viaGateway bool) {
	r.sent.Add(1)
	base, path := r.direct, " direct"
	if viaGateway {
		base, path = r.target, " via gateway"
	}
	// Not the run's context: calls in flight when it is interrupted finish
	// and are counted.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+s.path, bytes.NewReader(s.body(i)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	var ttfb, total time.Duration
	var n int64
	status := "error"
	res, err := r.client.Do(req)
	if err == nil {
		status = strconv.Itoa(res.StatusCode)
		br := bufio.NewReaderSize(res.Body, 32<<10)
		if _, perr := br.Peek(1); perr == nil {
			ttfb = time.Since(due)
		}
		n, err = io.Copy(io.Discard, br)
		_ = res.Body.Close()
		total = time.Since(due)
		if err != nil {
			status = "cut"
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.stats[s.name+path]
	if st == nil {
		st = &stat{statuses: map[string]int{}}
		r.stats[s.name+path] = st
	}
	st.statuses[status]++
	st.bytes += n
	if status == "200" {
		st.ttfb, st.total = append(st.ttfb, ttfb), append(st.total, total)
		w := int(due.Sub(r.start) / (10 * time.Second))
		if r.windows[w] == nil {
			r.windows[w] = map[bool][]time.Duration{}
		}
		r.windows[w][viaGateway] = append(r.windows[w][viaGateway], ttfb)
	}
}

// report prints each shape's timings on both paths and what the gateway
// added: the gap between their percentiles.
func (r *run) report(w *strings.Builder, rate float64, d time.Duration) {
	fmt.Fprintf(w, "\nSent %d calls in %s (%.0f/s asked, %.0f/s sent); the sender fell %s behind at most.\n\n",
		r.sent.Load(), d, rate, float64(r.sent.Load())/d.Seconds(), time.Duration(r.late.Load()).Round(time.Millisecond))
	fmt.Fprintln(w, "| Shape | Path | Calls | Statuses | First byte p50 / p90 / p99 / max | Whole p50 / p99 |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|")
	names := slices.Sorted(maps.Keys(r.stats))
	for _, k := range names {
		st := r.stats[k]
		slices.Sort(st.ttfb)
		slices.Sort(st.total)
		shape, path, _ := strings.Cut(k, " ")
		calls := 0
		for _, c := range st.statuses {
			calls += c
		}
		fmt.Fprintf(w, "| %s | %s | %d | %s | %s / %s / %s / %s | %s / %s |\n", shape, path, calls, tally(st.statuses),
			pct(st.ttfb, 50), pct(st.ttfb, 90), pct(st.ttfb, 99), pct(st.ttfb, 100), pct(st.total, 50), pct(st.total, 99))
	}
	through := 0
	for k, st := range r.stats {
		if strings.HasSuffix(k, " via gateway") {
			for _, c := range st.statuses {
				through += c
			}
		}
	}
	fmt.Fprintf(w, "\nThrough the gateway: %d calls.\n", through)
	fmt.Fprintln(w, "\nFirst byte by ten seconds (every shape), direct and via the gateway:")
	fmt.Fprintln(w, "\n| Seconds | Direct p50 / p99 | Via gateway p50 / p99 | Added p99 |")
	fmt.Fprintln(w, "|---|---|---|---|")
	for _, i := range slices.Sorted(maps.Keys(r.windows)) {
		g, d := r.windows[i][true], r.windows[i][false]
		slices.Sort(g)
		slices.Sort(d)
		fmt.Fprintf(w, "| %d-%d | %s / %s | %s / %s | %s |\n", i*10, i*10+10, pct(d, 50), pct(d, 99), pct(g, 50), pct(g, 99), gap(g, d, 99))
	}
	fmt.Fprintln(w, "\nWhat the gateway added (via gateway minus direct, same moment, same load):")
	fmt.Fprintln(w, "\n| Shape | First byte p50 | First byte p99 | Whole p50 | Whole p99 |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, name := range slices.Sorted(maps.Keys(shapes)) {
		g, d := r.stats[name+" via gateway"], r.stats[name+" direct"]
		if g == nil || d == nil || len(g.ttfb) == 0 || len(d.ttfb) == 0 {
			continue
		}
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", name, gap(g.ttfb, d.ttfb, 50), gap(g.ttfb, d.ttfb, 99),
			gap(g.total, d.total, 50), gap(g.total, d.total, 99))
	}
}

func tally(m map[string]int) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// at is the p-th percentile of sorted durations.
func at(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	i := int(float64(len(d)-1) * p / 100)
	return d[i]
}

func pct(d []time.Duration, p float64) string {
	if len(d) == 0 {
		return "-"
	}
	return ms(at(d, p))
}

func gap(g, d []time.Duration, p float64) string {
	diff := at(g, p) - at(d, p)
	sign := "+"
	if diff < 0 {
		sign, diff = "-", -diff
	}
	return sign + ms(diff)
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64) + " ms"
}

// samples are the gateways' expvar readings during the run.
type samples struct {
	mu   sync.Mutex
	rows map[string][]reading
}

type reading struct {
	at         time.Time
	goroutines int64
	heap, sys  uint64
	dropped    int64
}

// sample reads each debug address's expvar every five seconds until ctx
// ends or the process exits.
func sample(ctx context.Context, addrs string) *samples {
	s := &samples{rows: map[string][]reading{}}
	if addrs == "" {
		return s
	}
	c := &http.Client{Timeout: 2 * time.Second}
	for addr := range strings.SplitSeq(addrs, ",") {
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				if v, ok := readVars(ctx, c, addr); ok {
					s.mu.Lock()
					s.rows[addr] = append(s.rows[addr], v)
					s.mu.Unlock()
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	return s
}

func readVars(ctx context.Context, c *http.Client, addr string) (reading, bool) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/debug/vars", nil)
	res, err := c.Do(req)
	if err != nil {
		return reading{}, false
	}
	defer func() { _ = res.Body.Close() }()
	var v struct {
		Goroutines int64 `json:"goroutines"`
		Dropped    int64 `json:"requestlog_dropped"`
		Mem        struct {
			HeapInuse uint64 `json:"HeapInuse"`
			Sys       uint64 `json:"Sys"`
		} `json:"memstats"`
	}
	if json.NewDecoder(res.Body).Decode(&v) != nil {
		return reading{}, false
	}
	return reading{at: time.Now(), goroutines: v.Goroutines, heap: v.Mem.HeapInuse, sys: v.Mem.Sys, dropped: v.Dropped}, true
}

// report prints, per gateway, its goroutines and memory at the start, at
// most and at the end: a leak grows to the end instead of levelling off.
func (s *samples) report(w *strings.Builder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return
	}
	fmt.Fprintln(w, "\n| Gateway | Goroutines first / max / last | Heap in use first / max / last | Memory from the OS, last | Log rows dropped |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, addr := range slices.Sorted(maps.Keys(s.rows)) {
		rs := s.rows[addr]
		first, last := rs[0], rs[len(rs)-1]
		var maxG int64
		var maxH uint64
		for _, r := range rs {
			maxG, maxH = max(maxG, r.goroutines), max(maxH, r.heap)
		}
		fmt.Fprintf(w, "| %s | %d / %d / %d | %s / %s / %s | %s | %d |\n", addr, first.goroutines, maxG, last.goroutines,
			mib(first.heap), mib(maxH), mib(last.heap), mib(last.sys), last.dropped)
	}
}

func mib(b uint64) string { return strconv.FormatUint(b>>20, 10) + " MiB" }
