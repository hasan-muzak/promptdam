package detector

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"

	"github.com/hasan-muzak/keel-core/pkg/kv"
	"github.com/hasan-muzak/keel-core/pkg/memcache"

	"github.com/hasan-muzak/promptdam/server/features/firewall"
)

// Options configure a Client.
type Options struct {
	// URL is the service's base URL (http://detector:8484), Token its
	// bearer token: the service receives raw personal data.
	URL, Token string
	// CacheKey keys the scan cache's hashes (vault-derived): a dump of
	// Valkey says nothing of the texts, and one project's cache is no
	// oracle of another's.
	CacheKey []byte
	// KV shares what was read between replicas (nil: memory alone).
	KV *kv.Store
	// MaxSync is how much text not read before a call may wait for
	// (default 32 KiB): beyond, the newest is read, the rest isn't.
	MaxSync int
	// HTTP is the client (default: its own transport, no redirects; the
	// gateway's shared one refuses private addresses, and the service is
	// one).
	HTTP *http.Client
}

// Segments: a text over segmentMax bytes is read in pieces cut where only
// what comes before decides (a newline, else a space, else a character),
// so a text that grows keeps its first pieces and their cache keys.
const (
	segmentMax   = 2048
	segmentMin   = 1024
	cacheTTL     = 24 * time.Hour
	memEntries   = 200_000
	readTimeout  = 8 * time.Millisecond
	failuresOpen = 5
	openFor      = 5 * time.Second
	maxResponse  = 16 << 20
)

// Client asks the detector service for spans, remembering what it read:
// in memory, then Valkey, then the service. It is safe for concurrent use.
type Client struct {
	o       Options
	mem     *memcache.Cache[[32]byte, []firewall.Span]
	version atomic.Value // the model's version, as the service last said
	latency latency

	failures  atomic.Int32
	openUntil atomic.Int64

	mu       sync.Mutex
	inflight map[[32]byte]*flight
}

type flight struct {
	done  chan struct{}
	spans []firewall.Span
	err   error
}

// New returns a Client; nil when the gateway has no detector (an empty
// URL): the firewall then reads with its patterns alone.
func New(o Options) *Client {
	if o.URL == "" {
		return nil
	}
	if o.MaxSync <= 0 {
		o.MaxSync = 32 << 10
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{
			Transport:     &http.Transport{MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	c := &Client{o: o, mem: memcache.New[[32]byte, []firewall.Span](cacheTTL, time.Second, memEntries),
		inflight: map[[32]byte]*flight{}}
	c.version.Store("")
	return c
}

// For is the firewall's Reader of a project's calls.
func (c *Client) For(project uuid.UUID) firewall.Reader {
	if c == nil {
		return nil
	}
	return reader{c: c, project: project}
}

type reader struct {
	c       *Client
	project uuid.UUID
}

// segment is a piece of a text: text[off:off+len(s)].
type segment struct {
	text, off int
	s         string
	key       [32]byte
}

// Spans reads the texts: each piece read before is remembered, the rest
// asked of the service, at most MaxSync of it, the newest first.
func (r reader) Spans(ctx context.Context, texts []string) ([][]firewall.Span, error) {
	c := r.c
	out := make([][]firewall.Span, len(texts))
	var segs []segment
	for i, t := range texts {
		for _, s := range segments(t) {
			segs = append(segs, segment{text: i, off: s[0], s: t[s[0]:s[1]]})
		}
	}
	version := c.version.Load().(string)
	found := make([][]firewall.Span, len(segs))
	var missing []int
	now := time.Now()
	for i := range segs {
		segs[i].key = c.key(r.project, version, segs[i].s)
		if spans, ok := c.mem.Peek(segs[i].key, now); ok {
			found[i] = spans
			continue
		}
		missing = append(missing, i)
	}
	if len(missing) > 0 {
		missing = c.fromKV(ctx, segs, missing, found)
	}
	partial := false
	if len(missing) > 0 {
		// The newest text first, as much as a call may wait for.
		budget := c.o.MaxSync
		var ask []int
		for k := len(missing) - 1; k >= 0; k-- {
			i := missing[k]
			if budget -= len(segs[i].s); budget < 0 {
				partial = true
				break
			}
			ask = append(ask, i)
		}
		slices.Reverse(ask)
		if err := c.ask(ctx, r.project, segs, ask, found); err != nil {
			return nil, err
		}
	}
	for i, s := range segs {
		for _, sp := range found[i] {
			sp.Start += s.off
			sp.End += s.off
			out[s.text] = append(out[s.text], sp)
		}
	}
	if partial {
		return out, firewall.ErrPartial
	}
	return out, nil
}

// key is a piece's cache key: keyed, and per project and model version.
func (c *Client) key(project uuid.UUID, version, s string) [32]byte {
	mac := hmac.New(sha256.New, c.o.CacheKey)
	mac.Write(project[:])
	mac.Write([]byte(version))
	mac.Write([]byte{0})
	mac.Write([]byte(s))
	var k [32]byte
	copy(k[:], mac.Sum(nil))
	return k
}

func (c *Client) kvKey(k [32]byte) string { return c.o.KV.Key("dspans", hex.EncodeToString(k[:])) }

// fromKV reads the missing pieces from Valkey (one read, a short wait) and
// returns those still missing.
func (c *Client) fromKV(ctx context.Context, segs []segment, missing []int, found [][]firewall.Span) []int {
	if c.o.KV == nil || time.Now().UnixMilli() < c.openUntil.Load() {
		return missing
	}
	keys := make([]string, len(missing))
	for k, i := range missing {
		keys[k] = c.kvKey(segs[i].key)
	}
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	vc := c.o.KV.Client
	vals, err := vc.Do(rctx, vc.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil || len(vals) != len(keys) {
		return missing
	}
	now := time.Now()
	var still []int
	for k, i := range missing {
		raw, err := vals[k].AsBytes()
		spans, ok := decode(raw)
		if err != nil || !ok {
			still = append(still, i)
			continue
		}
		found[i] = spans
		c.mem.Set(segs[i].key, spans, now)
	}
	return still
}

// ask reads the pieces ask holds from the service, sharing a piece another
// call is asking for already.
func (c *Client) ask(ctx context.Context, project uuid.UUID, segs []segment, ask []int, found [][]firewall.Span) error {
	if len(ask) == 0 {
		return nil
	}
	if time.Now().UnixMilli() < c.openUntil.Load() {
		return errors.New("detector: paused after failing calls")
	}
	var mine []int
	var waits []*flight
	var waitFor []int
	c.mu.Lock()
	for _, i := range ask {
		if f, ok := c.inflight[segs[i].key]; ok {
			waits, waitFor = append(waits, f), append(waitFor, i)
			continue
		}
		c.inflight[segs[i].key] = &flight{done: make(chan struct{})}
		mine = append(mine, i)
	}
	c.mu.Unlock()

	var err error
	if len(mine) > 0 {
		err = c.call(ctx, project, segs, mine, found)
		c.mu.Lock()
		for _, i := range mine {
			f := c.inflight[segs[i].key]
			delete(c.inflight, segs[i].key)
			f.spans, f.err = found[i], err
			close(f.done)
		}
		c.mu.Unlock()
	}
	for k, f := range waits {
		select {
		case <-f.done:
			if f.err != nil && err == nil {
				err = f.err
			}
			found[waitFor[k]] = f.spans
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// call asks the service for the pieces idx holds, with a timeout that grows
// with their size, and remembers the answers.
func (c *Client) call(ctx context.Context, project uuid.UUID, segs []segment, idx []int, found [][]firewall.Span) error {
	req := SpansRequest{Priority: PrioritySync}
	bytesAsked := 0
	for _, i := range idx {
		req.Texts = append(req.Texts, Text{ID: strconv.Itoa(i), Text: segs[i].s})
		bytesAsked += len(segs[i].s)
	}
	wait := c.latency.timeout(bytesAsked)
	if d, ok := ctx.Deadline(); ok && time.Until(d) < wait {
		wait = time.Until(d)
	}
	req.DeadlineMS = wait.Milliseconds()
	body, _ := json.Marshal(req)
	cctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	hr, err := http.NewRequestWithContext(cctx, http.MethodPost, strings.TrimRight(c.o.URL, "/")+"/v1/spans", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	if c.o.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+c.o.Token)
	}
	start := time.Now()
	res, err := c.o.HTTP.Do(hr)
	if err != nil {
		c.failed()
		if cctx.Err() != nil {
			c.latency.add(bytesAsked, wait) // a timeout counts as its whole wait
		}
		return fmt.Errorf("detector: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil || res.StatusCode != http.StatusOK {
		c.failed()
		return fmt.Errorf("detector: %d %s", res.StatusCode, bytes.TrimSpace(raw[:min(len(raw), 200)]))
	}
	var out SpansResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		c.failed()
		return fmt.Errorf("detector: unreadable answer: %w", err)
	}
	c.failures.Store(0)
	c.latency.add(bytesAsked, time.Since(start))
	if out.Model != c.version.Load().(string) {
		c.version.Store(out.Model) // later pieces are keyed by the version that read them
	}
	byID := make(map[string][]Span, len(out.Texts))
	for _, t := range out.Texts {
		byID[t.ID] = t.Spans
	}
	now := time.Now()
	var writes []kvWrite
	for _, i := range idx {
		spans := checked(segs[i].s, byID[strconv.Itoa(i)])
		found[i] = spans
		k := c.key(project, out.Model, segs[i].s)
		c.mem.Set(k, spans, now)
		writes = append(writes, kvWrite{key: k, val: encode(spans)})
	}
	if c.o.KV != nil {
		go c.store(writes)
	}
	return nil
}

type kvWrite struct {
	key [32]byte
	val []byte
}

// store writes what was read to Valkey, off the call's path.
func (c *Client) store(ws []kvWrite) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	vc := c.o.KV.Client
	cmds := make(valkey.Commands, 0, len(ws))
	for _, w := range ws {
		cmds = append(cmds, vc.B().Set().Key(c.kvKey(w.key)).Value(string(w.val)).Ex(cacheTTL).Build())
	}
	for _, r := range vc.DoMulti(ctx, cmds...) {
		if err := r.Error(); err != nil {
			slog.Debug("detector: the scan cache wasn't written", "err", err)
			return
		}
	}
}

func (c *Client) failed() {
	if c.failures.Add(1) >= failuresOpen {
		c.failures.Store(0)
		c.openUntil.Store(time.Now().Add(openFor).UnixMilli())
	}
}

// checked keeps the spans that are places in s, on its characters' edges.
func checked(s string, spans []Span) []firewall.Span {
	out := make([]firewall.Span, 0, len(spans))
	for _, sp := range spans {
		if sp.Start < 0 || sp.End > len(s) || sp.Start >= sp.End || !utf8.RuneStart(s[sp.Start]) ||
			sp.End < len(s) && !utf8.RuneStart(s[sp.End]) || len(sp.Label) == 0 || len(sp.Label) > 64 {
			continue
		}
		out = append(out, firewall.Span{Start: sp.Start, End: sp.End, Label: sp.Label, Score: sp.Score})
	}
	return out
}

// segments cuts s into pieces of at most segmentMax bytes, each cut after
// the last newline (else space, else character) past segmentMin: where to
// cut depends only on what comes before.
func segments(s string) [][2]int {
	var out [][2]int
	for start := 0; start < len(s); {
		if len(s)-start <= segmentMax {
			out = append(out, [2]int{start, len(s)})
			break
		}
		window := s[start : start+segmentMax]
		var cut int
		if i := strings.LastIndexByte(window, '\n'); i >= segmentMin {
			cut = i + 1
		} else if i := strings.LastIndexByte(window, ' '); i >= segmentMin {
			cut = i + 1
		} else {
			cut = segmentMax
			for cut > 0 && !utf8.RuneStart(s[start+cut]) {
				cut--
			}
		}
		out = append(out, [2]int{start, start + cut})
		start += cut
	}
	return out
}

// encode packs spans for Valkey: a count, then per span its start, end,
// score (0-255) and label.
func encode(spans []firewall.Span) []byte {
	b := binary.AppendUvarint(nil, uint64(len(spans)))
	for _, sp := range spans {
		b = binary.AppendUvarint(b, uint64(sp.Start))
		b = binary.AppendUvarint(b, uint64(sp.End))
		b = append(b, byte(min(255, max(0, sp.Score*255+0.5))), byte(len(sp.Label)))
		b = append(b, sp.Label...)
	}
	return b
}

func decode(b []byte) ([]firewall.Span, bool) {
	if len(b) == 0 {
		return nil, false
	}
	n, k := binary.Uvarint(b)
	if k <= 0 || n > 10_000 {
		return nil, false
	}
	b = b[k:]
	out := make([]firewall.Span, 0, n)
	for range n {
		start, k1 := binary.Uvarint(b)
		if k1 <= 0 {
			return nil, false
		}
		end, k2 := binary.Uvarint(b[k1:])
		if k2 <= 0 || len(b) < k1+k2+2 {
			return nil, false
		}
		b = b[k1+k2:]
		score, l := float64(b[0])/255, int(b[1])
		if len(b) < 2+l {
			return nil, false
		}
		out = append(out, firewall.Span{Start: int(start), End: int(end), Score: score, Label: string(b[2 : 2+l])})
		b = b[2+l:]
	}
	return out, true
}
