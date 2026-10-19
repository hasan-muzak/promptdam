package firewall

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeModel marks the words it is given, wherever they are, as their
// labels, at score.
type fakeModel struct {
	marks map[string]string // word → label
	score float64
	err   error
	asked [][]string
	extra []Span // added to every text's spans
}

func (m *fakeModel) Spans(_ context.Context, texts []string) ([][]Span, error) {
	m.asked = append(m.asked, texts)
	if m.err != nil {
		return nil, m.err
	}
	out := make([][]Span, len(texts))
	for i, t := range texts {
		for w, label := range m.marks {
			for j := strings.Index(t, w); j >= 0; {
				out[i] = append(out[i], Span{Start: j, End: j + len(w), Label: label, Score: m.score})
				next := strings.Index(t[j+1:], w)
				if next < 0 {
					break
				}
				j += 1 + next
			}
		}
		out[i] = append(out[i], m.extra...)
	}
	return out, nil
}

func modelEngine(t *testing.T, mutate func(*Settings)) *Engine {
	return engine(t, func(s *Settings) {
		s.PII, s.Sensitive = Tokenize, Alert
		s.Model.PII, s.Model.Secrets = true, true
		if mutate != nil {
			mutate(s)
		}
	})
}

func TestModelNotAskedWhenOff(t *testing.T) {
	m := &fakeModel{marks: map[string]string{"Ayşe": "person"}}
	o, _ := engine(t, func(s *Settings) { s.PII = Tokenize }).InspectCall(chat("Hi Ayşe"), Scan{Model: m})
	if len(m.asked) != 0 || o.Body != nil {
		t.Fatalf("asked %v", m.asked)
	}
}

func TestModelFindingsApplied(t *testing.T) {
	m := &fakeModel{marks: map[string]string{"Ayşe Yılmaz": "person", "tip 2 diyabet": "health"}, score: 0.8}
	e := modelEngine(t, nil)
	o, err := e.InspectCall(chat("Ben Ayşe Yılmaz, tip 2 diyabet hastasıyım, mail ada@example.com"), Scan{Model: m})
	if err != nil || o.Body == nil {
		t.Fatalf("%+v %v", o, err)
	}
	body := string(o.Body)
	if strings.Contains(body, "Ayşe Yılmaz") || !strings.Contains(body, "tip 2 diyabet") || strings.Contains(body, "ada@example.com") {
		t.Fatalf("body %s", body)
	}
	kinds := o.Kinds()
	if !slices.Contains(kinds, "person") || !slices.Contains(kinds, "health") || !slices.Contains(kinds, "email") {
		t.Fatalf("kinds %v", kinds)
	}
	for _, h := range o.Hits {
		if h.Detector == "health" && (h.Action != Alert || !h.Model || h.Class != ClassSensitive) {
			t.Fatalf("health hit %+v", h)
		}
		if h.Detector == "email" && h.Model {
			t.Fatalf("email from the model: %+v", h)
		}
	}
	// The tool definitions and the system prompt's protocol fields stay
	// unread: only the messages' text went to the model.
	if len(m.asked) != 1 || len(m.asked[0]) != 2 {
		t.Fatalf("asked %q", m.asked)
	}
}

// A pattern wins a place the model shares: it is exact.
func TestPatternWinsOverlap(t *testing.T) {
	m := &fakeModel{marks: map[string]string{"ada@example.com": "person"}}
	o, _ := modelEngine(t, nil).InspectCall(chat("mail ada@example.com"), Scan{Model: m})
	if kinds := o.Kinds(); !slices.Equal(kinds, []string{"email"}) {
		t.Fatalf("kinds %v", kinds)
	}
}

// A value the model found once is masked wherever else the request holds
// it, thinking included, but not inside a longer word.
func TestFoundValuesAreConsistent(t *testing.T) {
	m := &fakeModel{marks: map[string]string{"Ayşe Yılmaz": "person"}}
	body := []byte(`{"model":"claude","max_tokens":5,"messages":[` +
		`{"role":"user","content":"I am Ayşe Yılmaz."},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"The user is Ayşe Yılmaz.","signature":"sig"},` +
		`{"type":"text","text":"Hello!"}]},` +
		`{"role":"user","content":"Tell AyşeYılmazX and Ayşe Yılmaz apart."}]}`)
	first := &onlyFirst{fakeModel: m} // the model sees only the first message's name
	o, err := modelEngine(t, nil).InspectCall(body, Scan{Model: first})
	if err != nil || o.Body == nil {
		t.Fatalf("%v", err)
	}
	got := string(o.Body)
	if strings.Count(got, "Ayşe Yılmaz") != 0 || !strings.Contains(got, "AyşeYılmazX") {
		t.Fatalf("body %s", got)
	}
	// The thinking was not sent to the model, only given its token.
	for _, texts := range m.asked {
		for _, s := range texts {
			if strings.Contains(s, "The user is") {
				t.Fatal("thinking read by the model")
			}
		}
	}
}

// onlyFirst answers for the first text only, as if the model had missed
// the name everywhere else.
type onlyFirst struct{ *fakeModel }

func (o *onlyFirst) Spans(ctx context.Context, texts []string) ([][]Span, error) {
	all, err := o.fakeModel.Spans(ctx, texts)
	for i := 1; i < len(all); i++ {
		all[i] = nil
	}
	return all, err
}

func TestNeverMasked(t *testing.T) {
	m := &fakeModel{marks: map[string]string{"Promptdam": "person", "Ayşe": "person"}}
	o, _ := modelEngine(t, func(s *Settings) { s.Model.Allow = []string{"promptdam", "Ayşe from Support"} }).
		InspectCall(chat("Promptdam's Ayşe from Support says hi to Ayşe"), Scan{Model: m})
	got := string(o.Body)
	if !strings.Contains(got, "Promptdam's Ayşe from Support") || strings.Contains(got, "hi to Ayşe") {
		t.Fatalf("body %s", got)
	}
}

// A secret no pattern knows blocks only when the model is sure of it; code
// reading a secret and placeholders aren't secrets.
func TestModelSecrets(t *testing.T) {
	value := "zz9Kq2LmP4tR7vX1wB3nH5"
	m := &fakeModel{marks: map[string]string{value: "credential", "cfg.Token": "credential", "your-key-here": "credential"}, score: 0.5}
	e := modelEngine(t, nil)
	o, _ := e.InspectCall(chat("deploy with "+value+" or cfg.Token or your-key-here"), Scan{Model: m})
	if o.Blocked {
		t.Fatal("an unsure secret blocked")
	}
	if kinds := o.Kinds(); !slices.Equal(kinds, []string{"credential"}) || o.Hits[0].Count != 1 || o.Hits[0].Action != Alert {
		t.Fatalf("hits %+v", o.Hits)
	}
	m.score = 0.95
	if o, _ := e.InspectCall(chat("deploy with "+value), Scan{Model: m}); !o.Blocked || o.BlockedBy != ClassSecret {
		t.Fatalf("a sure secret: %+v", o)
	}
}

func TestModelFailModes(t *testing.T) {
	m := &fakeModel{err: errors.New("timeout")}
	o, err := modelEngine(t, nil).InspectCall(chat("mail ada@example.com"), Scan{Model: m})
	if err != nil || !o.ModelFailed || o.Blocked || o.Body == nil {
		t.Fatalf("open: %+v %v", o, err)
	}
	o, err = modelEngine(t, func(s *Settings) { s.Model.FailMode = ModelFailClosed }).InspectCall(chat("hi"), Scan{Model: m})
	if !errors.Is(err, ErrScan) || !o.Blocked || o.BlockedBy != BlockedByModel || !strings.Contains(o.BlockMessage(), "detector model") {
		t.Fatalf("closed: %+v %v", o, err)
	}
}

// A span that isn't a place in its text is never trusted.
func TestModelSpansChecked(t *testing.T) {
	m := &fakeModel{extra: []Span{{Start: -1, End: 3, Label: "person"}, {Start: 2, End: 999, Label: "person"},
		{Start: 1, End: 2, Label: "person"}, {Start: 0, End: 2, Label: "no_such_label"}}}
	o, err := modelEngine(t, nil).InspectCall([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"Çağrı"}]}`), Scan{Model: m})
	if err != nil || o.Body != nil {
		t.Fatalf("%s %v", o.Body, err)
	}
}

// A term never masked is found whatever its case, even where lowering
// changes a letter's length (İ).
func TestNeverMaskedAnyCase(t *testing.T) {
	m := &fakeModel{marks: map[string]string{"Kargo": "person", "Ayşe": "person"}}
	o, _ := modelEngine(t, func(s *Settings) { s.Model.Allow = []string{"İstanbul Kargo"} }).
		InspectCall(chat("İSTANBUL KARGO? İstanbul Kargo, Ayşe"), Scan{Model: m})
	if got := string(o.Body); !strings.Contains(got, "İstanbul Kargo,") || strings.Contains(got, "Ayşe") {
		t.Fatalf("body %s", got)
	}
}
