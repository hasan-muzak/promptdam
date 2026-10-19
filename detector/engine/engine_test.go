package engine

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/hasan-muzak/promptdam/server/features/detector/wire"
)

// The engine's tests read with a real model when one is at hand
// (DETECTOR_TEST_MODEL, default ../models/bardsai-v0) and ONNX Runtime is
// (DETECTOR_ORT_LIB, default ../lib/libonnxruntime.1.31.0.dylib); CI has
// neither, so they skip there.
var (
	once     sync.Once
	testEng  *Engine
	loadFail error
)

func engine(t *testing.T) *Engine {
	t.Helper()
	once.Do(func() {
		lib := env("DETECTOR_ORT_LIB", "../lib/libonnxruntime.1.31.0.dylib")
		dir := env("DETECTOR_TEST_MODEL", "../models/bardsai-v0")
		if _, err := os.Stat(lib); err != nil {
			loadFail = err
			return
		}
		if _, err := os.Stat(dir + "/model.onnx"); err != nil {
			loadFail = err
			return
		}
		ort.SetSharedLibraryPath(lib)
		if err := ort.InitializeEnvironment(); err != nil {
			loadFail = err
			return
		}
		m, err := Load(dir, Options{Threads: 4})
		if err != nil {
			loadFail = err
			return
		}
		testEng = NewEngine(m, EngineOptions{})
		testEng.Start()
	})
	if testEng == nil {
		t.Skip(loadFail)
	}
	return testEng
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func labels(text string, spans []wire.Span) map[string]string {
	out := map[string]string{}
	for _, s := range spans {
		out[text[s.Start:s.End]] = s.Label
	}
	return out
}

func TestReadFindsPersonalData(t *testing.T) {
	e := engine(t)
	texts := []string{
		"Merhaba, ben Ayşe Yılmaz. Siparişim gelmedi, adresim Bağdat Caddesi No:12, Kadıköy İstanbul.",
		"Patient John Miller, born 04/12/1961, was diagnosed with type 2 diabetes.",
	}
	got, err := e.Read(context.Background(), texts, wire.PrioritySync)
	if err != nil {
		t.Fatal(err)
	}
	tr, en := labels(texts[0], got[0]), labels(texts[1], got[1])
	if tr["Ayşe Yılmaz"] != "person" || en["John Miller"] != "person" {
		t.Fatalf("names: %v %v", tr, en)
	}
	for _, s := range append(got[0], got[1]...) {
		if s.Score < 0.3 || s.Score > 1 || s.Label == "" {
			t.Fatalf("span %+v", s)
		}
	}
	t.Logf("tr %v\nen %v", tr, en)
}

// A text's spans are the same whatever shares its batch.
func TestReadIsDeterministic(t *testing.T) {
	e := engine(t)
	text := "Mail Ayşe Yılmaz at ayse@example.com about invoice 4471, she lives in Kadıköy."
	alone, err := e.Read(context.Background(), []string{text}, wire.PrioritySync)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([][]wire.Span, 8)
	for i := range results {
		wg.Go(func() {
			others := []string{strings.Repeat("Some other text by Mehmet Demir. ", i+1), text, "short"}
			got, err := e.Read(context.Background(), others, wire.PrioritySync)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = got[1]
		})
	}
	wg.Wait()
	for i, r := range results {
		if !reflect.DeepEqual(r, alone[0]) {
			t.Fatalf("batch %d: %v, alone %v", i, r, alone[0])
		}
	}
}

// A text longer than one window is read whole: a name near its end too.
func TestReadLongText(t *testing.T) {
	e := engine(t)
	text := strings.Repeat("The shipment left the warehouse and arrived on time. ", 120) + "Signed, Florian Ziegler."
	got, err := e.Read(context.Background(), []string{text}, wire.PrioritySync)
	if err != nil {
		t.Fatal(err)
	}
	if labels(text, got[0])["Florian Ziegler"] != "person" {
		t.Fatalf("spans %v", got[0])
	}
}

func TestReadRefusesWhenBusy(t *testing.T) {
	e := engine(t)
	small := NewEngine(e.m, EngineOptions{MaxQueued: 10})
	small.Start()
	defer small.Stop()
	if _, err := small.Read(context.Background(), []string{"a text with more than ten tokens in it, surely"}, wire.PrioritySync); err != ErrBusy {
		t.Fatalf("%v", err)
	}
}
