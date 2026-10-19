package detector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/hasan-muzak/promptdam/detector/engine"
	"github.com/hasan-muzak/promptdam/server/features/detector/wire"
)

// The service's routes, on a real model when one is at hand (as the
// engine's tests).
func TestServer(t *testing.T) {
	lib, dir := "lib/libonnxruntime.1.31.0.dylib", "models/bardsai-v0"
	if _, err := os.Stat(dir + "/model.onnx"); err != nil {
		t.Skip(err)
	}
	if _, err := os.Stat(lib); err != nil {
		t.Skip(err)
	}
	ort.SetSharedLibraryPath(lib)
	if err := ort.InitializeEnvironment(); err != nil {
		t.Skip(err)
	}
	m, err := engine.Load(dir, engine.Options{Threads: 2})
	if err != nil {
		t.Fatal(err)
	}
	e := engine.NewEngine(m, engine.EngineOptions{})
	e.Start()
	defer e.Stop()
	s := &Server{Engine: e, Version: m.Version, Token: "tok"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	if res, _ := http.Get(srv.URL + "/readyz"); res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready before warming up: %d", res.StatusCode)
	}
	if err := s.WarmUp(context.Background()); err != nil {
		t.Fatal(err)
	}
	post := func(token, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/spans", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	body := `{"priority":"sync","deadline_ms":2000,"texts":[{"id":"a","text":"Call Ayşe Yılmaz"},{"id":"b","text":""}]}`
	if res := post("wrong", body); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", res.StatusCode)
	}
	res := post("tok", body)
	var out wire.SpansResponse
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil || out.Model != m.Version ||
		len(out.Texts) != 2 || out.Texts[0].ID != "a" || len(out.Texts[0].Spans) != 1 || out.Texts[1].Spans == nil {
		t.Fatalf("%d %+v", res.StatusCode, out)
	}
}
