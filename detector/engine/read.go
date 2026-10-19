package engine

import (
	"context"
	"errors"
	"expvar"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"

	"github.com/hasan-muzak/promptdam/server/features/detector/wire"
)

// Windows: a text longer than the model reads at once is read in windows
// that overlap, each token's labels taken from the window it sits most
// centrally in. Every window is padded to a bucket's length, so a text is
// read with the same shape whatever else shares its batch: its spans are
// the same in every call, which keeps tokens and prompt caches stable.
const (
	maxTokens = 512 // the model's, its two special tokens included
	overlap   = 128
)

var buckets = []int{64, 128, 256, 512}

// ErrBusy is a read refused because the queue holds too much already: the
// caller falls back at once instead of waiting for a timeout.
var ErrBusy = errors.New("engine: busy")

// Counters, at /debug/vars.
var (
	readTexts  = expvar.NewInt("detector_texts")
	readTokens = expvar.NewInt("detector_tokens")
	batches    = expvar.NewInt("detector_batches")
	refused    = expvar.NewInt("detector_refused")
	dropped    = expvar.NewInt("detector_dropped")
)

// EngineOptions shape the batching.
type EngineOptions struct {
	// Workers run batches at once (default 2); MaxBatch windows go in one
	// batch (16); MaxWait is how long a batch waits to fill (3 ms).
	Workers  int
	MaxBatch int
	MaxWait  time.Duration
	// MaxQueued tokens may wait; more is refused (ErrBusy). Default 64k.
	MaxQueued int64
}

// Engine reads texts with a model, batching windows across requests:
// waiting calls first, reads for alerts after.
type Engine struct {
	m        *Model
	o        EngineOptions
	sync, bg chan *job
	queued   atomic.Int64
	stop     chan struct{}
	wg       sync.WaitGroup
}

// job is one window to run: ids padded to a bucket, its real length.
type job struct {
	ctx    context.Context
	ids    []int64
	n      int
	logits [][]float32
	err    error
	done   chan struct{}
}

// NewEngine returns an Engine; Start runs its workers.
func NewEngine(m *Model, o EngineOptions) *Engine {
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.MaxBatch <= 0 {
		o.MaxBatch = 16
	}
	if o.MaxWait <= 0 {
		o.MaxWait = 3 * time.Millisecond
	}
	if o.MaxQueued <= 0 {
		o.MaxQueued = 64 << 10
	}
	return &Engine{m: m, o: o, sync: make(chan *job, 4096), bg: make(chan *job, 4096), stop: make(chan struct{})}
}

// Start runs the workers.
func (e *Engine) Start() {
	for range e.o.Workers {
		e.wg.Go(e.work)
	}
}

// Stop ends the workers once their batches are done.
func (e *Engine) Stop() {
	close(e.stop)
	e.wg.Wait()
}

// Read returns each text's spans.
func (e *Engine) Read(ctx context.Context, texts []string, priority string) ([][]wire.Span, error) {
	type textJobs struct {
		offsets [][2]int
		jobs    []*job
		starts  []int // each job's first token in the text
	}
	all := make([]textJobs, len(texts))
	var tokens int64
	for i, t := range texts {
		enc := e.m.tok.EncodeWithOptions(t, false, tokenizers.WithReturnOffsets())
		tj := textJobs{offsets: make([][2]int, len(enc.IDs))}
		for k, o := range enc.Offsets {
			tj.offsets[k] = [2]int{int(o[0]), int(o[1])}
		}
		content := maxTokens - 2
		for start := 0; start < len(enc.IDs); start += content - overlap {
			end := min(start+content, len(enc.IDs))
			j := e.window(ctx, enc.IDs[start:end])
			tj.jobs, tj.starts = append(tj.jobs, j), append(tj.starts, start)
			tokens += int64(len(j.ids))
			if end == len(enc.IDs) {
				break
			}
		}
		all[i] = tj
	}
	if e.queued.Add(tokens) > e.o.MaxQueued {
		e.queued.Add(-tokens)
		refused.Add(1)
		return nil, ErrBusy
	}
	q := e.bg
	if priority != wire.PriorityBackground {
		q = e.sync
	}
	for _, tj := range all {
		for _, j := range tj.jobs {
			q <- j
		}
	}
	out := make([][]wire.Span, len(texts))
	for i, tj := range all {
		logits := make([][]float32, len(tj.offsets))
		best := make([]int, len(tj.offsets)) // how central each token's window put it
		for k, j := range tj.jobs {
			select {
			case <-j.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if j.err != nil {
				return nil, j.err
			}
			n := j.n - 2
			for p := range n {
				t := tj.starts[k] + p
				if c := min(p, n-1-p) + 1; c > best[t] {
					best[t], logits[t] = c, j.logits[p+1]
				}
			}
		}
		path, prob := e.m.viterbi(logits)
		out[i] = e.m.spans(texts[i], path, prob, tj.offsets)
		readTexts.Add(1)
	}
	readTokens.Add(tokens)
	return out, nil
}

// window is a job for content tokens: the model's own start and end added,
// padded to its bucket.
func (e *Engine) window(ctx context.Context, content []uint32) *job {
	n := len(content) + 2
	size := buckets[len(buckets)-1]
	for _, b := range buckets {
		if n <= b {
			size = b
			break
		}
	}
	ids := make([]int64, size)
	ids[0] = e.m.bos
	for i, id := range content {
		ids[i+1] = int64(id)
	}
	ids[n-1] = e.m.eos
	for i := n; i < size; i++ {
		ids[i] = e.m.pad
	}
	return &job{ctx: ctx, ids: ids, n: n, done: make(chan struct{})}
}

// work runs batches: a waiting call's windows first, then whatever comes
// within MaxWait, run bucket by bucket.
func (e *Engine) work() {
	for {
		var first *job
		select {
		case first = <-e.sync:
		default:
			select {
			case first = <-e.sync:
			case first = <-e.bg:
			case <-e.stop:
				return
			}
		}
		batch := []*job{first}
		timer := time.NewTimer(e.o.MaxWait)
	fill:
		for len(batch) < e.o.MaxBatch {
			select {
			case j := <-e.sync:
				batch = append(batch, j)
			default:
				select {
				case j := <-e.sync:
					batch = append(batch, j)
				case j := <-e.bg:
					batch = append(batch, j)
				case <-timer.C:
					break fill
				}
			}
		}
		timer.Stop()
		e.run(batch)
	}
}

// run runs a batch, grouped by bucket.
func (e *Engine) run(batch []*job) {
	sort.SliceStable(batch, func(a, b int) bool { return len(batch[a].ids) < len(batch[b].ids) })
	for start := 0; start < len(batch); {
		end := start
		for end < len(batch) && len(batch[end].ids) == len(batch[start].ids) {
			end++
		}
		e.runBucket(batch[start:end])
		start = end
	}
}

func (e *Engine) runBucket(group []*job) {
	var live []*job
	for _, j := range group {
		e.queued.Add(-int64(len(j.ids)))
		if j.ctx.Err() != nil {
			j.err = j.ctx.Err()
			close(j.done)
			dropped.Add(1)
			continue
		}
		live = append(live, j)
	}
	if len(live) == 0 {
		return
	}
	finish := func(err error) {
		for _, j := range live {
			if err != nil {
				j.err = err
			}
			close(j.done)
		}
	}
	size := len(live[0].ids)
	ids := make([]int64, 0, len(live)*size)
	mask := make([]int64, 0, len(live)*size)
	for _, j := range live {
		ids = append(ids, j.ids...)
		for i := range size {
			if i < j.n {
				mask = append(mask, 1)
			} else {
				mask = append(mask, 0)
			}
		}
	}
	shape := ort.NewShape(int64(len(live)), int64(size))
	idT, err := ort.NewTensor(shape, ids)
	if err != nil {
		finish(err)
		return
	}
	defer idT.Destroy()
	maskT, err := ort.NewTensor(shape, mask)
	if err != nil {
		finish(err)
		return
	}
	defer maskT.Destroy()
	outputs := []ort.Value{nil}
	if err := e.m.session.Run([]ort.Value{idT, maskT}, outputs); err != nil {
		finish(err)
		return
	}
	defer outputs[0].Destroy()
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		finish(errors.New("engine: the model's logits aren't float32"))
		return
	}
	data, l := out.GetData(), len(e.m.labels)
	if dims := out.GetShape(); len(dims) != 3 || int(dims[2]) != l || int(dims[1]) != size {
		finish(errors.New("engine: the model's logits have an unexpected shape"))
		return
	}
	for b, j := range live {
		j.logits = make([][]float32, j.n)
		for t := range j.n {
			row := make([]float32, l)
			copy(row, data[(b*size+t)*l:(b*size+t+1)*l])
			j.logits[t] = row
		}
	}
	batches.Add(1)
	finish(nil)
}
