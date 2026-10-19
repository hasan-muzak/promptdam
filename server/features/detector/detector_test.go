package detector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/features/detector/wire"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
)

// service is a fake detector service marking every "Ayşe" as a person.
type service struct {
	calls  atomic.Int32
	texts  atomic.Int32
	status atomic.Int32
	model  string
	extra  []wire.Span
	block  chan struct{} // when set, answers wait for it
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	if s.block != nil {
		<-s.block
	}
	if st := s.status.Load(); st != 0 {
		http.Error(w, "down", int(st))
		return
	}
	var req wire.SpansRequest
	if r.Header.Get("Authorization") != "Bearer tok" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Priority != wire.PrioritySync {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	out := wire.SpansResponse{Model: s.model}
	for _, t := range req.Texts {
		s.texts.Add(1)
		ts := wire.TextSpan{ID: t.ID, Spans: append([]wire.Span{}, s.extra...)}
		for i := strings.Index(t.Text, "Ayşe"); i >= 0; {
			ts.Spans = append(ts.Spans, wire.Span{Start: i, End: i + len("Ayşe"), Label: "person", Score: 0.9})
			next := strings.Index(t.Text[i+1:], "Ayşe")
			if next < 0 {
				break
			}
			i += 1 + next
		}
		out.Texts = append(out.Texts, ts)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func client(t *testing.T, s *service, o Options) (*Client, firewall.Reader) {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	o.URL, o.Token, o.CacheKey = srv.URL, "tok", []byte("0123456789abcdef0123456789abcdef")
	c := New(o)
	return c, c.For(uuid.New())
}

// A long text is read in pieces whose spans come back at the text's
// places; read once, it is remembered.
func TestSpansCachedAndSegmented(t *testing.T) {
	s := &service{model: "m1"}
	_, r := client(t, s, Options{})
	long := strings.Repeat("line of text\n", 300) + "then Ayşe wrote"
	texts := []string{"Hi Ayşe", long}
	got, err := r.Spans(context.Background(), texts)
	if err != nil || len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if sp := got[1][0]; long[sp.Start:sp.End] != "Ayşe" {
		t.Fatalf("place %+v", sp)
	}
	if s.texts.Load() < 3 {
		t.Fatalf("the long text was read whole: %d texts", s.texts.Load())
	}
	calls := s.calls.Load()
	again, err := r.Spans(context.Background(), texts)
	if err != nil || s.calls.Load() != calls || again[1][0] != got[1][0] {
		t.Fatalf("read again: %d calls, %v", s.calls.Load()-calls, err)
	}
	// A text that grew keeps its first pieces: only what is new is read.
	before := s.texts.Load()
	if _, err := r.Spans(context.Background(), []string{long + "\nand Ayşe again"}); err != nil {
		t.Fatal(err)
	}
	if n := s.texts.Load() - before; n != 1 {
		t.Fatalf("a grown text read %d pieces", n)
	}
}

func TestSegmentsAreStable(t *testing.T) {
	text := strings.Repeat("word ", 2000) + strings.Repeat("ç", 3000)
	a := segments(text)
	for _, s := range a {
		if s[1]-s[0] > segmentMax || s[1]-s[0] <= 0 {
			t.Fatalf("segment %v", s)
		}
	}
	b := segments(text + " more text")
	for i := 0; i < len(a)-1; i++ {
		if a[i] != b[i] {
			t.Fatalf("segment %d moved: %v %v", i, a[i], b[i])
		}
	}
}

// More new text than a call may wait for: the newest is read, the call says
// it read part.
func TestPartial(t *testing.T) {
	s := &service{model: "m1"}
	_, r := client(t, s, Options{MaxSync: 100})
	old, newest := strings.Repeat("old Ayşe text ", 20), "new Ayşe"
	got, err := r.Spans(context.Background(), []string{old, newest})
	if !errors.Is(err, firewall.ErrPartial) || len(got[1]) != 1 || len(got[0]) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

// A service failing 5 calls in a row is left alone for a while: calls
// don't wait on it.
func TestBreaker(t *testing.T) {
	s := &service{model: "m1"}
	s.status.Store(http.StatusInternalServerError)
	_, r := client(t, s, Options{})
	for i := range 6 {
		if _, err := r.Spans(context.Background(), []string{"Ayşe " + strings.Repeat("x", i)}); err == nil {
			t.Fatal("no error")
		}
	}
	if n := s.calls.Load(); n != failuresOpen {
		t.Fatalf("%d calls reached the service", n)
	}
}

// Spans that aren't places in their text are dropped.
func TestSpansChecked(t *testing.T) {
	s := &service{model: "m1", extra: []wire.Span{{Start: -1, End: 2, Label: "person"}, {Start: 1, End: 2, Label: "person"},
		{Start: 0, End: 99, Label: "person"}, {Start: 0, End: 1, Label: ""}}}
	_, r := client(t, s, Options{})
	got, err := r.Spans(context.Background(), []string{"Çağrı"})
	if err != nil || len(got[0]) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

// Calls asking for the same text at once share one read.
func TestConcurrentCallsShare(t *testing.T) {
	s := &service{model: "m1", block: make(chan struct{})}
	_, r := client(t, s, Options{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := r.Spans(context.Background(), []string{"same text, Ayşe"}); err != nil || len(got[0]) != 1 {
				t.Errorf("%v %v", got, err)
			}
		}()
	}
	for s.calls.Load() == 0 { // the first call reached the service
		time.Sleep(time.Millisecond)
	}
	close(s.block)
	wg.Wait()
	if n := s.calls.Load(); n != 1 {
		t.Fatalf("%d calls", n)
	}
}

// What one replica read, another finds in Valkey.
func TestSharedThroughValkey(t *testing.T) {
	store := testkit.Valkey(t)
	s := &service{model: "m1"}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	project := uuid.New()
	o := Options{URL: srv.URL, Token: "tok", CacheKey: []byte("k"), KV: store}
	a, b := New(o), New(o)
	if _, err := a.For(project).Spans(context.Background(), []string{"Ayşe here"}); err != nil {
		t.Fatal(err)
	}
	b.version.Store("m1") // b learned the version from its own calls
	// a's write to Valkey leaves the call's path: give it a moment.
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		calls := s.calls.Load()
		got, err := b.For(project).Spans(context.Background(), []string{"Ayşe here"})
		if err == nil && len(got[0]) == 1 && s.calls.Load() == calls {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("b read the service, not Valkey")
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	in := []firewall.Span{{Start: 3, End: 9, Label: "person", Score: 0.5}, {Start: 1000, End: 70000, Label: "health", Score: 1}}
	out, ok := decode(encode(in))
	if !ok || len(out) != 2 || out[1].End != 70000 || out[0].Label != "person" || out[0].Score < 0.49 || out[0].Score > 0.51 {
		t.Fatalf("%+v %v", out, ok)
	}
	if _, ok := decode([]byte{5, 1}); ok {
		t.Fatal("decoded garbage")
	}
	empty, ok := decode(encode(nil))
	if !ok || len(empty) != 0 {
		t.Fatal("no spans")
	}
}
