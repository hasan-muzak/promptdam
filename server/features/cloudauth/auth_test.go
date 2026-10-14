package cloudauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/hasan-muzak/promptdam/server/features/cloudauth/cloudauthtest"
)

func post(t *testing.T, url string, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	return req
}

// A Bedrock API key goes as x-api-key to the Anthropic API and as a bearer
// token to the OpenAI-compatible one.
func TestBedrockKey(t *testing.T) {
	ctx := context.Background()
	mantle := post(t, "https://bedrock-mantle.eu-west-1.api.aws/anthropic/v1/messages", "{}")
	runtime := post(t, "https://bedrock-runtime.eu-west-1.amazonaws.com/openai/v1/chat/completions", "{}")
	k := BedrockKey("ABSK-test")
	_ = k.Authorize(ctx, mantle, nil)
	_ = k.Authorize(ctx, runtime, nil)
	if mantle.Header.Get("X-Api-Key") != "ABSK-test" || mantle.Header.Get("Authorization") != "" ||
		runtime.Header.Get("Authorization") != "Bearer ABSK-test" || runtime.Header.Get("X-Api-Key") != "" {
		t.Fatalf("mantle %v, runtime %v", mantle.Header, runtime.Header)
	}
}

// IAM keys sign each request with SigV4 for the host's service and region,
// the body hashed in: exactly what AWS's own signer makes; a session token
// goes along; a host that isn't AWS's model API isn't signed.
func TestSigV4(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	body := `{"model":"anthropic.claude-haiku-5-5","max_tokens":9,"messages":[]}`
	for _, tt := range []struct {
		url, service, region string
	}{
		{"https://bedrock-mantle.eu-west-1.api.aws/anthropic/v1/messages", "bedrock-mantle", "eu-west-1"},
		{"https://bedrock-runtime.us-east-1.amazonaws.com/openai/v1/chat/completions", "bedrock", "us-east-1"},
	} {
		s := SigV4("AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "session-1").(*sigV4)
		s.now = func() time.Time { return at }
		req := post(t, tt.url, body)
		if err := s.Authorize(ctx, req, []byte(body)); err != nil {
			t.Fatal(err)
		}
		want := post(t, tt.url, body)
		sum := sha256.Sum256([]byte(body))
		creds := aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "session-1"}
		if err := v4.NewSigner().SignHTTP(ctx, creds, want, hex.EncodeToString(sum[:]), tt.service, tt.region, at); err != nil {
			t.Fatal(err)
		}
		got := req.Header.Get("Authorization")
		if got == "" || got != want.Header.Get("Authorization") || req.Header.Get("X-Amz-Security-Token") != "session-1" ||
			!strings.Contains(got, "/"+tt.region+"/"+tt.service+"/aws4_request") {
			t.Fatalf("%s: %q, want %q", tt.url, got, want.Header.Get("Authorization"))
		}
	}
	if err := SigV4("a", "b", "").Authorize(ctx, post(t, "https://example.com/v1/messages", "{}"), nil); !errors.Is(err, ErrNotAWS) {
		t.Fatalf("another host: %v", err)
	}
}

// A service account's token is fetched once for every call that needs it
// at the same time, kept until five minutes before it lapses, and fetched
// again then; the token service's refusal is reported, never a key.
func TestGoogleTokens(t *testing.T) {
	ctx := context.Background()
	sa := cloudauthtest.NewServiceAccount(t, "acme-prod")
	server := cloudauthtest.NewTokenServer(t, sa)
	parsed, err := ParseServiceAccount([]byte(sa.JSON))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	tokens := NewTokens(server.Client(), server.URL)
	tokens.now = func() time.Time { return now }
	auth := tokens.Google("provider-1", parsed)

	var wg sync.WaitGroup
	got := make([]string, 20)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := post(t, "https://aiplatform.googleapis.com/v1/x", "{}")
			if err := auth.Authorize(ctx, req, nil); err != nil {
				t.Error(err)
			}
			got[i] = req.Header.Get("Authorization")
		}()
	}
	wg.Wait()
	for _, h := range got {
		if h != "Bearer token-1" {
			t.Fatalf("authorization %q", h)
		}
	}
	if n := server.Fetches.Load(); n != 1 {
		t.Fatalf("%d fetches for concurrent calls", n)
	}
	// The same provider's next snapshot reuses the token.
	if again := tokens.Google("provider-1", parsed); again != auth {
		t.Fatal("a new snapshot got a new token cache")
	}
	now = now.Add(54 * time.Minute)
	req := post(t, "https://aiplatform.googleapis.com/v1/x", "{}")
	if err := auth.Authorize(ctx, req, nil); err != nil || req.Header.Get("Authorization") != "Bearer token-1" {
		t.Fatalf("still fresh: %v %q", err, req.Header.Get("Authorization"))
	}
	now = now.Add(2 * time.Minute) // 4 minutes left
	if err := auth.Authorize(ctx, req, nil); err != nil || req.Header.Get("Authorization") != "Bearer token-2" {
		t.Fatalf("refreshed: %v %q", err, req.Header.Get("Authorization"))
	}

	// A key Google doesn't accept (here, another account's).
	other := cloudauthtest.NewServiceAccount(t, "acme-prod")
	otherParsed, _ := ParseServiceAccount([]byte(other.JSON))
	var refused *TokenError
	err = tokens.Google("provider-2", otherParsed).Authorize(ctx, post(t, "https://aiplatform.googleapis.com/v1/x", "{}"), nil)
	if !errors.As(err, &refused) || refused.Code != "invalid_grant" || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatalf("refused: %v", err)
	}

	// A provider unused for hours is forgotten.
	now = now.Add(3 * time.Hour)
	tokens.Google("provider-3", parsed)
	tokens.mu.Lock()
	n := len(tokens.sources)
	tokens.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d token caches kept", n)
	}
}
