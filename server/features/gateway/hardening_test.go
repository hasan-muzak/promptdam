package gateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hasan-muzak/keel-core/pkg/safehttp"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// hostile is a provider that answers what the test sets.
func hostile(t *testing.T, status int, body string, header http.Header) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(up.Close)
	gw := httptest.NewServer(gateway.New(gateway.Config{
		Resolver:  gateway.Static{{Provider: "custom", BaseURL: up.URL + "/v1"}},
		Transport: safehttp.NewTransport(safehttp.TransportOptions{AllowLoopback: true}),
	}))
	t.Cleanup(gw.Close)
	return gw.URL
}

// call makes a call through the gateway: its status, headers and body.
func call(t *testing.T, base string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer pd_sk_test_client")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(body)
}

// A provider refusing the project's key may quote it: the client gets the
// gateway's own error.
func TestProviderAuthErrorsAreTheGatewaysOwn(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		base := hostile(t, status, `{"error":{"message":"Incorrect API key provided: sk-proj-abcd****************wxyz"}}`, nil)
		got, h, body := call(t, base)
		if got != http.StatusBadGateway || h.Get(brand.HeaderErrorCode) != "provider_auth_failed" ||
			strings.Contains(body, "wxyz") || strings.Contains(body, "sk-proj") {
			t.Fatalf("%d: %d %s", status, got, body)
		}
	}
}

// Other errors pass through, what they quote masked.
func TestProviderErrorsAreMasked(t *testing.T) {
	base := hostile(t, http.StatusBadRequest,
		`{"error":{"message":"invalid content near AKIAIOSFODNN7EXAMPLE for ada@example.com"}}`, nil)
	got, _, body := call(t, base)
	if got != http.StatusBadRequest || strings.Contains(body, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(body, "ada@") ||
		!strings.Contains(body, "invalid content near") {
		t.Fatalf("%d %s", got, body)
	}
}

// A provider can't speak for the gateway, nor show the customer's account.
func TestProviderHeadersCantSpoofTheGateways(t *testing.T) {
	base := hostile(t, http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`, http.Header{
		"X-Promptdam-Router": {"spoofed"}, "X-Promptdam-Cache": {"hit"}, "Openai-Organization": {"org-secret"},
		"X-Request-Id": {"kept"},
	})
	_, h, _ := call(t, base)
	if h.Get(brand.HeaderRouter) != "" || h.Get(brand.HeaderCache) != "" ||
		h.Get("Openai-Organization") != "" || h.Get("X-Request-Id") != "kept" {
		t.Fatalf("headers %v", h)
	}
}
