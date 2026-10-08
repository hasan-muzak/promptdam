package payloads

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/hasan-muzak/keel-core/pkg/blob"
	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/vault"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/payloadsegment"
	"github.com/hasan-muzak/promptdam/server/ent/predicate"
	"github.com/hasan-muzak/promptdam/server/features/answers"
)

// purpose seals payloads in the vault, per organization.
const purpose = "promptdam-payload"

// Limits of what is kept of a call.
const (
	// MaxRequest is the most a masked request may weigh to be kept.
	MaxRequest = 1 << 20
	// MaxAnswer is where an answer's text is cut.
	MaxAnswer = 256 << 10
	// MaxError is the most a failed call's error body may weigh to be kept.
	MaxError = 64 << 10
)

// Segments are written when they reach SegmentBytes or are SegmentAge old.
const (
	SegmentBytes = 4 << 20
	SegmentAge   = time.Minute
)

// Errors.
var (
	// ErrGone is a payload that expired, was deleted, or was never stored.
	ErrGone = errors.New("payloads: the payload expired or was deleted")
	// ErrRef is a reference that isn't one.
	ErrRef = errors.New("payloads: not a payload reference")
)

// Payload is what is kept of a call.
type Payload struct {
	Format string `json:"format"`
	Stream bool   `json:"stream"`
	Status int    `json:"status"`
	// Request is the request as the provider got it, masked; nil when it
	// was too large (Truncated holds "request").
	Request json.RawMessage `json:"request"`
	// Answer is what the model answered, masked; nil when answers aren't
	// kept, can't be read in the call's format, or the call failed.
	Answer *Answer `json:"answer"`
	// Error is a failed call's error body, masked.
	Error json.RawMessage `json:"error,omitempty"`
	// Truncated names what was cut or left out: request, answer.
	Truncated []string `json:"truncated"`
}

// Answer is a model's answer, as kept.
type Answer struct {
	Text    string `json:"text"`
	Tools   []Tool `json:"tools"`
	Refused bool   `json:"refused"`
}

// Tool is a tool the model called.
type Tool struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Entry is a call to keep.
type Entry struct {
	Org, Project uuid.UUID
	// RetentionDays is how long the plan keeps the call's log.
	RetentionDays int
	At            time.Time
	Capture       *Capture
	Format        wire.Format
	Stream        bool
	Status        int
	// Request is the body sent to the provider; Answer what the client got.
	Request, Answer []byte
}

// StoreConfig configures a Store.
type StoreConfig struct {
	Ent   *pdent.Client
	Blob  *blob.Store
	Vault *vault.Vault
	Now   func() time.Time
}

// Store keeps payloads: gateways add them (Start writes their segments),
// the API reads and deletes them, the worker expires them.
type Store struct {
	cfg StoreConfig

	mu   sync.Mutex
	open map[segmentKey]*segment

	stop chan struct{}
	done chan struct{}
}

// segmentKey groups payloads that expire alike.
type segmentKey struct {
	project uuid.UUID
	days    int
}

// segment is a project's payloads being gathered.
type segment struct {
	org, project uuid.UUID
	key          string
	buf          bytes.Buffer
	payloads     int
	opened, last time.Time
	days         int
}

// NewStore returns a Store; nil when there is no object storage.
func NewStore(cfg StoreConfig) *Store {
	if cfg.Blob == nil || cfg.Vault == nil {
		return nil
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Store{cfg: cfg, open: map[segmentKey]*segment{}}
}

// Build masks a call's payload: what is kept of it.
func Build(e Entry) Payload {
	p := Payload{Format: e.Format.String(), Stream: e.Stream, Status: e.Status, Truncated: []string{}}
	mask := e.Capture.Mask
	if o, err := mask.Inspect(e.Request); err == nil {
		req := e.Request
		if o.Body != nil {
			req = o.Body
		}
		if len(req) <= MaxRequest && json.Valid(req) {
			p.Request = req
		}
	}
	if p.Request == nil {
		p.Truncated = append(p.Truncated, "request")
	}
	if e.Answer == nil {
		return p
	}
	if e.Status >= 300 {
		if len(e.Answer) <= MaxError {
			if o, err := mask.Inspect(e.Answer); err == nil {
				p.Error = e.Answer
				if o.Body != nil {
					p.Error = o.Body
				}
			}
		}
		return p
	}
	a, err := answers.Of(e.Format, e.Answer, e.Stream)
	if err != nil {
		return p
	}
	text := a.Text
	if len(text) > MaxAnswer {
		cut := MaxAnswer
		for cut > 0 && text[cut]&0xC0 == 0x80 {
			cut--
		}
		text = text[:cut]
		p.Truncated = append(p.Truncated, "answer")
	}
	p.Answer = &Answer{Text: mask.MaskText(text), Tools: []Tool{}, Refused: a.Refused}
	for _, t := range a.Tools {
		args := t.Args
		if o, err := mask.Inspect(args); err == nil && json.Valid(args) {
			if o.Body != nil {
				args = o.Body
			}
		} else {
			args, _ = json.Marshal(mask.MaskText(string(t.Args)))
		}
		p.Answer.Tools = append(p.Answer.Tools, Tool{Name: t.Name, Args: args})
	}
	return p
}

// Add masks and seals a call's payload and adds it to its project's
// segment; it returns the payload's reference ("" when nothing was kept).
// Segments are written by Start's loop, or at once when full.
func (s *Store) Add(ctx context.Context, e Entry) string {
	if s == nil || e.Capture == nil || e.Request == nil {
		return ""
	}
	raw, err := json.Marshal(Build(e))
	if err != nil {
		return ""
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	sealed, err := s.cfg.Vault.Seal(e.Org, purpose, gz.Bytes())
	if err != nil {
		slog.Warn("payloads: a payload wasn't sealed", "project", e.Project, "err", err)
		return ""
	}
	sk := segmentKey{e.Project, e.RetentionDays}
	s.mu.Lock()
	seg := s.open[sk]
	if seg == nil {
		seg = &segment{org: e.Org, project: e.Project, opened: s.cfg.Now(), days: e.RetentionDays,
			key: s.cfg.Blob.Key(e.Org, e.RetentionDays) + "." + e.Project.String()}
		s.open[sk] = seg
	}
	offset := seg.buf.Len()
	seg.buf.WriteString(sealed)
	seg.buf.WriteByte('\n')
	seg.payloads++
	seg.last = e.At
	ref := seg.key + "#" + strconv.Itoa(offset) + "+" + strconv.Itoa(len(sealed))
	var full *segment
	if seg.buf.Len() >= SegmentBytes {
		full = seg
		delete(s.open, sk)
	}
	s.mu.Unlock()
	if full != nil {
		s.write(ctx, full)
	}
	return ref
}

// write stores a segment and its row: tried twice, then dropped (payload
// capture is best effort; the calls' rows point at nothing).
func (s *Store) write(ctx context.Context, seg *segment) {
	ctx = context.WithoutCancel(ctx)
	var err error
	for try := range 2 {
		if try > 0 {
			time.Sleep(time.Second)
		}
		if err = s.cfg.Blob.Put(ctx, seg.key, seg.buf.Bytes(), "application/octet-stream"); err == nil {
			break
		}
	}
	if err != nil {
		slog.Error("payloads: a segment wasn't written; its payloads are lost", "project", seg.project, "payloads", seg.payloads, "err", err)
		return
	}
	last := seg.last
	if last.IsZero() {
		last = seg.opened
	}
	err = s.cfg.Ent.PayloadSegment.Create().SetOrgID(seg.org).SetProjectID(seg.project).SetKey(seg.key).
		SetBytes(int64(seg.buf.Len())).SetPayloads(seg.payloads).SetExpiresAt(last.Add(time.Duration(seg.days) * 24 * time.Hour)).
		Exec(entx.WithOrg(ctx, seg.org))
	if err != nil {
		// Without its row the object can't be read or expired early:
		// storage's lifecycle still removes it.
		slog.Error("payloads: a segment's row wasn't written", "key", seg.key, "err", err)
	}
}

// Flush writes the segments opened before cutoff (all of them with a zero
// cutoff).
func (s *Store) Flush(ctx context.Context, cutoff time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	var due []*segment
	for k, seg := range s.open {
		if cutoff.IsZero() || seg.opened.Before(cutoff) {
			due = append(due, seg)
			delete(s.open, k)
		}
	}
	s.mu.Unlock()
	for _, seg := range due {
		s.write(ctx, seg)
	}
}

// Start writes segments as they age, until Stop.
func (s *Store) Start(context.Context) error {
	if s == nil {
		return nil
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(s.done)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.Flush(context.Background(), s.cfg.Now().Add(-SegmentAge))
			}
		}
	}()
	return nil
}

// Stop writes every open segment.
func (s *Store) Stop(ctx context.Context) error {
	if s == nil || s.stop == nil {
		return nil
	}
	close(s.stop)
	<-s.done
	s.Flush(ctx, time.Time{})
	return nil
}

// parseRef reads key#offset+length.
func parseRef(ref string) (key string, offset, length int64, err error) {
	key, span, ok := strings.Cut(ref, "#")
	o, l, ok2 := strings.Cut(span, "+")
	if !ok || !ok2 || key == "" {
		return "", 0, 0, ErrRef
	}
	if offset, err = strconv.ParseInt(o, 10, 64); err != nil || offset < 0 {
		return "", 0, 0, ErrRef
	}
	if length, err = strconv.ParseInt(l, 10, 64); err != nil || length <= 0 {
		return "", 0, 0, ErrRef
	}
	return key, offset, length, nil
}

// Read returns a project's payload by its reference.
func (s *Store) Read(ctx context.Context, org, project uuid.UUID, ref string) (Payload, error) {
	if s == nil {
		return Payload{}, ErrGone
	}
	key, offset, length, err := parseRef(ref)
	if err != nil {
		return Payload{}, err
	}
	seg, err := s.cfg.Ent.PayloadSegment.Query().
		Where(payloadsegment.Key(key), payloadsegment.ProjectID(project), payloadsegment.ExpiresAtGT(s.cfg.Now())).
		Only(entx.WithOrg(ctx, org))
	if pdent.IsNotFound(err) || err == nil && offset+length > seg.Bytes {
		return Payload{}, ErrGone
	}
	if err != nil {
		return Payload{}, err
	}
	sealed, err := s.cfg.Blob.GetRange(ctx, key, offset, length)
	if errors.Is(err, blob.ErrNotFound) {
		return Payload{}, ErrGone
	}
	if err != nil {
		return Payload{}, err
	}
	gz, err := s.cfg.Vault.Open(org, purpose, string(sealed))
	if err != nil {
		return Payload{}, fmt.Errorf("payloads: opening: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return Payload{}, fmt.Errorf("payloads: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(zr, 16<<20))
	if err != nil {
		return Payload{}, fmt.Errorf("payloads: %w", err)
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Payload{}, fmt.Errorf("payloads: %w", err)
	}
	return p, nil
}

// DeleteProject deletes every payload a project kept, now: its segments
// and their rows. A gateway's open segment is written later, and expires
// as its plan says.
func (s *Store) DeleteProject(ctx context.Context, org, project uuid.UUID) (int, error) {
	if s == nil {
		return 0, nil
	}
	return s.deleteWhere(entx.WithOrg(ctx, org), payloadsegment.ProjectID(project))
}

// Expire deletes the segments whose retention ended.
func (s *Store) Expire(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	return s.deleteWhere(entx.SkipTenant(ctx), payloadsegment.ExpiresAtLTE(s.cfg.Now()))
}

// deleteWhere deletes the matching segments' objects, then their rows, a
// thousand at a time.
func (s *Store) deleteWhere(ctx context.Context, where ...predicate.PayloadSegment) (int, error) {
	n := 0
	for {
		rows, err := s.cfg.Ent.PayloadSegment.Query().Where(where...).Limit(1000).All(ctx)
		if err != nil || len(rows) == 0 {
			return n, err
		}
		keys := make([]string, len(rows))
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			keys[i], ids[i] = r.Key, r.ID
		}
		if err := s.cfg.Blob.DeleteKeys(ctx, keys...); err != nil {
			return n, err
		}
		if _, err := s.cfg.Ent.PayloadSegment.Delete().Where(payloadsegment.IDIn(ids...)).Exec(ctx); err != nil {
			return n, err
		}
		n += len(rows)
	}
}

// PurgeProjects deletes the objects of an organization's projects
// (deleted ones), those written after their rows went too.
func (s *Store) PurgeProjects(ctx context.Context, org uuid.UUID, projects []uuid.UUID) (int, error) {
	if s == nil || len(projects) == 0 {
		return 0, nil
	}
	suffixes := make([]string, len(projects))
	for i, p := range projects {
		suffixes[i] = "." + p.String()
	}
	return s.cfg.Blob.DeleteMatching(ctx, org, func(key string) bool {
		for _, suf := range suffixes {
			if strings.HasSuffix(key, suf) {
				return true
			}
		}
		return false
	})
}

// DeleteOrg deletes every payload an organization kept, now: its
// segments, their rows, and objects written after their rows went.
func (s *Store) DeleteOrg(ctx context.Context, org uuid.UUID) (int, error) {
	if s == nil {
		return 0, nil
	}
	n, err := s.deleteWhere(entx.WithOrg(ctx, org), payloadsegment.OrgID(org))
	if err != nil {
		return n, err
	}
	_, err = s.cfg.Blob.DeleteOrg(ctx, org)
	return n, err
}

// PurgeOrg deletes every object of a deleted organization.
func (s *Store) PurgeOrg(ctx context.Context, org uuid.UUID) (int, error) {
	if s == nil {
		return 0, nil
	}
	return s.cfg.Blob.DeleteOrg(ctx, org)
}

// ExpireArgs deletes expired segments (hourly).
type ExpireArgs struct{}

// Kind implements river.JobArgs.
func (ExpireArgs) Kind() string { return "promptdam_payloads_expire" }

type expireWorker struct {
	river.WorkerDefaults[ExpireArgs]
	s *Store
}

func (w *expireWorker) Work(ctx context.Context, _ *river.Job[ExpireArgs]) error {
	_, err := w.s.Expire(ctx)
	return err
}

// RegisterWorkers deletes payloads hourly once their retention ends (none
// without object storage).
func RegisterWorkers(j *jobs.Jobs, s *Store) {
	if s == nil {
		return
	}
	jobs.Register(j, &expireWorker{s: s})
	j.Every("promptdam-payloads-expire", time.Hour, func() river.JobArgs { return ExpireArgs{} })
}
