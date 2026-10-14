package conversation

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"

	"github.com/hasan-muzak/keel-core/pkg/kv"
	"github.com/hasan-muzak/keel-core/pkg/memcache"
)

// Record is what the gateway knows of a turn.
type Record struct {
	// Placement is the tier a router gave the turn ("" before one did).
	Placement
	// Start and Last are the turn's first and latest calls' starts, in
	// Unix milliseconds.
	Start, Last int64
	// Calls counts the turn's finished calls; Input, CacheRead, CacheWrite
	// and Output sum their tokens.
	Calls, Input, CacheRead, CacheWrite, Output int64
	// Cached is the latest call's cached prefix (its cache reads and
	// writes), on Model at Provider (of Kind), kept for TTL ("5m", "1h",
	// "" when unknown).
	Cached                     int64
	Model, Provider, Kind, TTL string
	// Target is the model the gateway asked for in the latest call (Model
	// is the provider's name for it, often dated), Backend the backend
	// that served it, Strategy its prompt-cache strategy, and LastInput its
	// input.
	Target, Backend, Strategy string
	LastInput                 int64
}

// Placement is a router's tier for a turn: the tier that serves it, the
// one the router chose and the step that chose it, how sure it was, the
// router's version it was placed with, and the reasoning effort applied.
type Placement struct {
	Tier, Source, Decided string
	Confidence            float64
	Version, Effort       string
}

// Placed reports a turn a router placed.
func (r Record) Placed() bool { return r.Tier != "" }

// Asked is the model the gateway asked for in the turn's latest call: a
// target's name, which the provider's (Model) may not be (OpenRouter
// answers with dated names). Records from before targets were kept give
// the provider's.
func (r Record) Asked() string {
	if r.Target != "" {
		return r.Target
	}
	return r.Model
}

// Finished is what a call that ended adds to its turn.
type Finished struct {
	// Start is the call's start, in Unix milliseconds.
	Start                                int64
	Input, CacheRead, CacheWrite, Output int64
	Model, Provider, Kind, TTL           string
	Target, Backend, Strategy            string
}

// Previous is what the store knew of the conversation's call before a
// finished one: the model it asked for, of which kind, the backend that
// served it, its prompt-cache strategy and lifetime, and its input.
type Previous struct {
	Target, Kind, Backend, Strategy, TTL string
	Input                                int64
}

// Chain states: what the store knew of the call before a finished one.
const (
	// ChainStart: no earlier call of the conversation is known.
	ChainStart = "start"
	// ChainContinued: the gap is from the conversation's previous call.
	ChainContinued = "continued"
	// ChainUnknown: the store couldn't be read.
	ChainUnknown = "unknown"
)

// Options configure a Store.
type Options struct {
	// TTL is how long a turn is kept after its last write (default 65
	// minutes: past the providers' longest cache).
	TTL time.Duration
	// Timeout bounds a read on a call's path (default 8 ms).
	Timeout time.Duration
	// MaxRecords bounds the turns each replica holds in memory (default
	// 100,000, about 25 MB).
	MaxRecords int
}

// Store keeps turns in this replica's memory and in Valkey, which every
// replica reads: a read on a call's path is memory first, then one Valkey
// read bounded by a short timeout; writes leave the call's path. Without
// Valkey (tests, a single process) memory alone serves.
type Store struct {
	kv   *kv.Store
	mem  *memcache.Cache[Key, Record]
	opts Options

	queue chan placeOp
	stop  chan struct{}
	done  chan struct{}

	// A Valkey that fails 5 reads in a row is left alone for 5 s: calls
	// don't wait on it, and fall back to memory.
	failures  atomic.Int32
	openUntil atomic.Int64
	dropped   atomic.Int64
}

type placeOp struct {
	project uuid.UUID
	key     Key
	p       Placement
}

// Writes queued for Valkey, flushed together.
const (
	queueSize    = 4096
	maxBatch     = 256
	flushEvery   = 10 * time.Millisecond
	readFailures = 5
	readPause    = 5 * time.Second
	writeTimeout = time.Second
)

// New returns a Store on Valkey (nil: memory alone).
func New(store *kv.Store, opts Options) *Store {
	if opts.TTL <= 0 {
		opts.TTL = 65 * time.Minute
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 8 * time.Millisecond
	}
	if opts.MaxRecords <= 0 {
		opts.MaxRecords = 100_000
	}
	return &Store{kv: store, mem: memcache.New[Key, Record](opts.TTL, time.Second, opts.MaxRecords), opts: opts,
		queue: make(chan placeOp, queueSize), stop: make(chan struct{}), done: make(chan struct{})}
}

// Start begins writing placements to Valkey.
func (s *Store) Start(context.Context) error {
	go s.write()
	return nil
}

// Stop writes the queued placements and stops.
func (s *Store) Stop(ctx context.Context) error {
	close(s.stop)
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) key(project uuid.UUID, k Key) string {
	return s.kv.Key("turn", project.String(), k.String())
}

// Get returns a turn, from memory or else Valkey; ok is false when neither
// has it (or Valkey couldn't be read in time).
func (s *Store) Get(ctx context.Context, project uuid.UUID, k Key) (Record, bool) {
	if k.IsZero() {
		return Record{}, false
	}
	if r, ok := s.mem.Peek(k, time.Now()); ok {
		return r, true
	}
	return s.load(ctx, project, k)
}

// Fresh returns a turn as Valkey has it, the calls other replicas finished
// included, else as this replica remembers it.
func (s *Store) Fresh(ctx context.Context, project uuid.UUID, k Key) (Record, bool) {
	if k.IsZero() {
		return Record{}, false
	}
	if r, ok := s.load(ctx, project, k); ok {
		return r, true
	}
	return s.mem.Peek(k, time.Now())
}

// load reads a turn from Valkey and remembers it.
func (s *Store) load(ctx context.Context, project uuid.UUID, k Key) (Record, bool) {
	if s.kv == nil || time.Now().UnixMilli() < s.openUntil.Load() {
		return Record{}, false
	}
	rctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	c := s.kv.Client
	fields, err := c.Do(rctx, c.B().Hgetall().Key(s.key(project, k)).Build()).AsStrMap()
	if err != nil {
		if s.failures.Add(1) >= readFailures {
			s.failures.Store(0)
			s.openUntil.Store(time.Now().Add(readPause).UnixMilli())
		}
		return Record{}, false
	}
	s.failures.Store(0)
	if len(fields) == 0 {
		return Record{}, false
	}
	r := decode(fields)
	s.mem.Set(k, r, time.Now())
	return r, true
}

// Place records the turn's placement: in memory at once, in Valkey off
// the call's path. The first placement of a turn wins: a replica that
// placed the same turn at the same time takes Valkey's.
func (s *Store) Place(project uuid.UUID, k Key, p Placement) {
	if k.IsZero() {
		return
	}
	now := time.Now()
	r, _ := s.mem.Peek(k, now)
	if r.Placed() {
		return
	}
	r.Placement = p
	s.mem.Set(k, r, now)
	if s.kv == nil {
		return
	}
	select {
	case s.queue <- placeOp{project: project, key: k, p: p}:
	default:
		if n := s.dropped.Add(1); n == 1 || n%1000 == 0 {
			slog.Warn("conversation: a placement was not written", "dropped", n)
		}
	}
}

// placeScript sets a turn's placement unless it has one, and returns the
// placement it keeps.
var placeScript = valkey.NewLuaScript(`
if redis.call('HEXISTS', KEYS[1], 't') == 0 then
  redis.call('HSET', KEYS[1], 't', ARGV[1], 'src', ARGV[2], 'dec', ARGV[3], 'conf', ARGV[4], 'ver', ARGV[5], 'eff', ARGV[6])
end
redis.call('PEXPIRE', KEYS[1], ARGV[7])
return redis.call('HMGET', KEYS[1], 't', 'src', 'dec', 'conf', 'ver', 'eff')`)

// write flushes queued placements to Valkey together.
func (s *Store) write() {
	defer close(s.done)
	if s.kv == nil {
		<-s.stop
		return
	}
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	batch := make([]placeOp, 0, maxBatch)
	for {
		select {
		case op := <-s.queue:
			if batch = append(batch, op); len(batch) == maxBatch {
				batch = s.flush(batch)
			}
		case <-tick.C:
			batch = s.flush(batch)
		case <-s.stop:
			for {
				select {
				case op := <-s.queue:
					if batch = append(batch, op); len(batch) == maxBatch {
						batch = s.flush(batch)
					}
				default:
					s.flush(batch)
					return
				}
			}
		}
	}
}

func (s *Store) flush(batch []placeOp) []placeOp {
	if len(batch) == 0 {
		return batch
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	execs := make([]valkey.LuaExec, len(batch))
	ttl := strconv.FormatInt(s.opts.TTL.Milliseconds(), 10)
	for i, op := range batch {
		execs[i] = valkey.LuaExec{Keys: []string{s.key(op.project, op.key)}, Args: []string{op.p.Tier, op.p.Source, op.p.Decided,
			strconv.FormatFloat(op.p.Confidence, 'f', 4, 64), op.p.Version, op.p.Effort, ttl}}
	}
	now := time.Now()
	for i, res := range placeScript.ExecMulti(ctx, s.kv.Client, execs...) {
		kept, err := res.AsStrSlice()
		if err != nil {
			if n := s.dropped.Add(1); n == 1 || n%1000 == 0 {
				slog.Warn("conversation: a placement was not written", "dropped", n, "err", err)
			}
			continue
		}
		// Another replica placed the turn first: this one follows it.
		if p := placementOf(kept); p.Tier != batch[i].p.Tier || p.Version != batch[i].p.Version || p.Effort != batch[i].p.Effort {
			r, _ := s.mem.Peek(batch[i].key, now)
			r.Placement = p
			s.mem.Set(batch[i].key, r, now)
		}
	}
	return batch[:0]
}

// finishScript adds a finished call to its turn and returns the start of
// the conversation's call before it: the turn's latest start, or for a
// turn's first call the previous turn's. Starts only move forward, so calls
// finishing out of order (or on two replicas) can't move them back.
var finishScript = valkey.NewLuaScript(`
local start = tonumber(ARGV[1])
local last = tonumber(redis.call('HGET', KEYS[1], 'ls') or '0')
local before = last
local from = KEYS[1]
if last == 0 and KEYS[2] ~= KEYS[1] then
  from = KEYS[2]
  before = tonumber(redis.call('HGET', KEYS[2], 'ls') or '0')
end
local p = redis.call('HMGET', from, 'tm', 'kd', 'bk', 'cs', 'ttl', 'li')
if start > last then
  redis.call('HSET', KEYS[1], 'ls', ARGV[1], 'lc', ARGV[6], 'm', ARGV[7], 'pv', ARGV[8], 'kd', ARGV[9], 'ttl', ARGV[10],
    'tm', ARGV[12], 'bk', ARGV[13], 'cs', ARGV[14], 'li', ARGV[2])
end
redis.call('HSETNX', KEYS[1], 't0', ARGV[1])
redis.call('HINCRBY', KEYS[1], 'n', 1)
redis.call('HINCRBY', KEYS[1], 'in', ARGV[2])
redis.call('HINCRBY', KEYS[1], 'cr', ARGV[3])
redis.call('HINCRBY', KEYS[1], 'cw', ARGV[4])
redis.call('HINCRBY', KEYS[1], 'out', ARGV[5])
redis.call('PEXPIRE', KEYS[1], ARGV[11])
return {tostring(before), p[1] or '', p[2] or '', p[3] or '', p[4] or '', p[5] or '', p[6] or ''}`)

// Finish adds a finished call to its turn, off the call's path, and says
// how long after the conversation's previous call it started: gap in
// milliseconds (0 when unknown), the chain state, and what that call was.
func (s *Store) Finish(ctx context.Context, project uuid.UUID, turn, prev Key, f Finished) (gap int64, state string, last Previous) {
	if turn.IsZero() {
		return 0, "", Previous{}
	}
	now := time.Now()
	r, _ := s.mem.Peek(turn, now)
	before, from := r.Last, r
	if before == 0 && !prev.IsZero() {
		p, _ := s.mem.Peek(prev, now)
		before, from = p.Last, p
	}
	last = Previous{Target: from.Target, Kind: from.Kind, Backend: from.Backend, Strategy: from.Strategy, TTL: from.TTL,
		Input: from.LastInput}
	state = ChainStart
	if s.kv != nil {
		ctx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()
		prevKey := s.key(project, turn)
		if !prev.IsZero() {
			prevKey = s.key(project, prev)
		}
		got, err := finishScript.Exec(ctx, s.kv.Client, []string{s.key(project, turn), prevKey}, []string{
			strconv.FormatInt(f.Start, 10), strconv.FormatInt(f.Input, 10), strconv.FormatInt(f.CacheRead, 10),
			strconv.FormatInt(f.CacheWrite, 10), strconv.FormatInt(f.Output, 10),
			strconv.FormatInt(f.CacheRead+f.CacheWrite, 10), f.Model, f.Provider, f.Kind, f.TTL,
			strconv.FormatInt(s.opts.TTL.Milliseconds(), 10), f.Target, f.Backend, f.Strategy,
		}).AsStrSlice()
		if err != nil || len(got) < 7 {
			state, last = ChainUnknown, Previous{}
		} else {
			before, _ = strconv.ParseInt(got[0], 10, 64)
			input, _ := strconv.ParseInt(got[6], 10, 64)
			last = Previous{Target: got[1], Kind: got[2], Backend: got[3], Strategy: got[4], TTL: got[5], Input: input}
		}
	}
	if before > 0 && state != ChainUnknown {
		state = ChainContinued
		if f.Start > before {
			gap = f.Start - before
		}
	}
	// This replica's copy follows.
	r.Calls++
	r.Input, r.CacheRead, r.CacheWrite, r.Output = r.Input+f.Input, r.CacheRead+f.CacheRead, r.CacheWrite+f.CacheWrite,
		r.Output+f.Output
	if r.Start == 0 || f.Start < r.Start {
		r.Start = f.Start
	}
	if f.Start >= r.Last {
		r.Last, r.Cached, r.Model, r.Provider, r.Kind, r.TTL = f.Start, f.CacheRead+f.CacheWrite, f.Model, f.Provider, f.Kind, f.TTL
		r.Target, r.Backend, r.Strategy, r.LastInput = f.Target, f.Backend, f.Strategy, f.Input
	}
	s.mem.Set(turn, r, now)
	return gap, state, last
}

func placementOf(v []string) Placement {
	at := func(i int) string {
		if i < len(v) {
			return v[i]
		}
		return ""
	}
	conf, _ := strconv.ParseFloat(at(3), 64)
	return Placement{Tier: at(0), Source: at(1), Decided: at(2), Confidence: conf, Version: at(4), Effort: at(5)}
}

// decode reads a turn's Valkey hash.
func decode(f map[string]string) Record {
	n := func(k string) int64 {
		v, _ := strconv.ParseInt(f[k], 10, 64)
		return v
	}
	conf, _ := strconv.ParseFloat(f["conf"], 64)
	return Record{
		Placement: Placement{Tier: f["t"], Source: f["src"], Decided: f["dec"], Confidence: conf, Version: f["ver"], Effort: f["eff"]},
		Start:     n("t0"), Last: n("ls"), Calls: n("n"), Input: n("in"), CacheRead: n("cr"), CacheWrite: n("cw"), Output: n("out"),
		Cached: n("lc"), Model: f["m"], Provider: f["pv"], Kind: f["kd"], TTL: f["ttl"],
		Target: f["tm"], Backend: f["bk"], Strategy: f["cs"], LastInput: n("li"),
	}
}
