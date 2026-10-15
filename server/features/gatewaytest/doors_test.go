package gatewaytest_test

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/events"

	"github.com/hasan-muzak/promptdam/bench/fakeupstream"
	"github.com/hasan-muzak/promptdam/server/features/firewall"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/resolver"
)

// token is a firewall placeholder in a request.
var token = regexp.MustCompile(`⟦[a-z_]+_\d+⟧`)

// Calls through the doors meet the whole gateway: the project's key, read
// where Google's and AWS's clients send it, the firewall, whose
// placeholders come back restored in a Gemini stream, and the request
// log, which names the door.
func TestDoors(t *testing.T) {
	w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true, Upstream: fakeupstream.Options{StreamReply: true,
		Reply: func(_ string, body []byte) string { return "Noted: " + token.FindString(string(body)) + "." }}})
	call := func(path, body string, header ...string) (int, http.Header, string) {
		req, _ := http.NewRequestWithContext(w.Ctx, http.MethodPost, w.Gateway.URL+path, strings.NewReader(body))
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return res.StatusCode, res.Header, string(out)
	}
	gemini := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	if status, _, body := call("/gemini/v1beta/models/gpt-5-mini:generateContent", gemini, "X-Goog-Api-Key", w.Raw); status != 200 ||
		!strings.Contains(body, `"finishReason":"STOP"`) {
		t.Fatalf("x-goog-api-key: %d %s", status, body)
	}
	if status, _, body := call("/gemini/v1beta/models/gpt-5-mini:generateContent?key="+w.Raw, gemini); status != 200 {
		t.Fatalf("?key=: %d %s", status, body)
	}
	status, h, body := call("/gemini/v1beta/models/gpt-5-mini:generateContent", gemini, "X-Goog-Api-Key", "pd_sk_live_wrong")
	if status != 401 || !strings.Contains(body, `"status":"UNAUTHENTICATED"`) || h.Get("X-Promptdam-Error-Code") == "" {
		t.Fatalf("a wrong key: %d %s", status, body)
	}
	// A Chat provider serves Converse calls (translated) for the models it
	// lists.
	w.Serves("main", "gpt-5-mini")
	converse := `{"messages":[{"role":"user","content":[{"text":"hi"}]}],"inferenceConfig":{"maxTokens":50}}`
	w.Eventually(func() bool {
		status, _, _ := call("/bedrock/model/gpt-5-mini/converse", converse, "Authorization", "Bearer "+w.Raw)
		return status == 200
	})
	if status, _, body := call("/bedrock/model/gpt-5-mini/converse", converse, "Authorization", "Bearer "+w.Raw); status != 200 ||
		!strings.Contains(body, `"stopReason":"end_turn"`) {
		t.Fatalf("converse: %d %s", status, body)
	}

	// The firewall masks the email; the stream brings it back.
	s := firewall.Defaults()
	s.PII = firewall.Tokenize
	if _, err := w.App.Firewall.Put(w.Scoped(), w.Org, w.Project, s); err != nil {
		t.Fatal(err)
	}
	mail := `{"contents":[{"role":"user","parts":[{"text":"Write to ada@example.com"}]}]}`
	w.Eventually(func() bool {
		call("/gemini/v1beta/models/gpt-5-mini:streamGenerateContent?alt=sse", mail, "X-Goog-Api-Key", w.Raw)
		recs := w.Records()
		return recs[len(recs)-1].Meta.(*resolver.Meta).Firewall.Tokens != nil
	})
	status, _, body = call("/gemini/v1beta/models/gpt-5-mini:streamGenerateContent?alt=sse", mail, "X-Goog-Api-Key", w.Raw)
	var text strings.Builder
	for _, m := range regexp.MustCompile(`"text":"([^"]*)"`).FindAllStringSubmatch(body, -1) {
		text.WriteString(m[1])
	}
	if status != 200 || text.String() != "Noted: ada@example.com." {
		t.Fatalf("restored: %d %q\n%s", status, text.String(), body)
	}
	reqs := w.Fake.Requests()
	if sent := string(reqs[len(reqs)-1].Body); strings.Contains(sent, "ada@example.com") || !token.MatchString(sent) {
		t.Fatalf("the provider got %s", sent)
	}

	if err := w.Log.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Door   string `ch:"door"`
		Format string `ch:"api_format"`
		Count  uint64 `ch:"n"`
	}
	if err := w.C.Events.Select(w.Ctx, &rows, events.Project(w.Project), `SELECT door, api_format, count() AS n FROM requests
		WHERE {tenant} AND status = 200 GROUP BY door, api_format ORDER BY door`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Door != "bedrock-converse" || rows[0].Format != "anthropic.messages" ||
		rows[1].Door != "gemini" || rows[1].Format != "openai.chat" || rows[1].Count < 4 {
		t.Fatalf("logged %+v", rows)
	}
}
