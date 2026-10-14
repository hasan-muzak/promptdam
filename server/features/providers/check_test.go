package providers_test

import (
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/cloudauth/cloudauthtest"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// A cloud account is checked with what it can do: Azure's key lists its
// resource's models; Bedrock and Vertex answer one token, signed, from the
// model the account lists first. What fails is said in the cloud's terms:
// a permission or a model not enabled, a key Google refuses.
func TestCheckClouds(t *testing.T) {
	sa := cloudauthtest.NewServiceAccount(t, "acme-prod")
	tokens := cloudauthtest.NewTokenServer(t, sa)
	w := gatewaytest.New(t, gatewaytest.Options{GoogleTokenURL: tokens.URL})
	add := func(name, kind, path string, cred providers.Credential, models ...string) string {
		t.Helper()
		p, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, providers.Input{Name: name, Kind: kind,
			BaseURL: w.Upstream.URL + path, Credential: &cred, Models: models, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		checked, err := w.App.Providers.Check(w.Scoped(), w.Project, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return checked.CheckError
	}
	if problem := add("bedrock", providers.Bedrock, "", providers.Credential{APIKey: "ABSK-test-key-0000"}); problem != "" {
		t.Fatalf("bedrock: %s", problem)
	}
	reqs := w.Fake.Requests()
	if got := reqs[len(reqs)-1]; got.URLPath != "/anthropic/v1/messages" || !strings.Contains(string(got.Body), `"anthropic.claude-haiku-5-5"`) {
		t.Fatalf("bedrock asked %s: %s", got.URLPath, got.Body)
	}
	if problem := add("vertex", providers.Vertex, "/v1/projects/acme-prod/locations/global",
		providers.Credential{ServiceAccount: sa.JSON}, "gemini-3.8-flash"); problem != "" {
		t.Fatalf("vertex: %s", problem)
	}
	reqs = w.Fake.Requests()
	if got := reqs[len(reqs)-1]; got.URLPath != "/v1/projects/acme-prod/locations/global/endpoints/openapi/chat/completions" ||
		got.Header.Get("Authorization") != "Bearer token-1" {
		t.Fatalf("vertex asked %s with %q", got.URLPath, got.Header.Get("Authorization"))
	}
	if problem := add("azure", providers.Azure, "", providers.Credential{APIKey: "azure-key-0000000000"}); problem != "" {
		t.Fatalf("azure: %s", problem)
	}
	reqs = w.Fake.Requests()
	if got := reqs[len(reqs)-1]; got.URLPath != "/openai/v1/models" || got.Header.Get("Api-Key") != "azure-key-0000000000" {
		t.Fatalf("azure asked %s with %v", got.URLPath, got.Header)
	}

	w.Fake.Fail(403)
	if problem := add("bedrock-2", providers.Bedrock, "", providers.Credential{APIKey: "ABSK-test-key-0000"}); !strings.Contains(problem,
		"may not call claude-haiku-5-5") {
		t.Fatalf("forbidden: %q", problem)
	}
	other := cloudauthtest.NewServiceAccount(t, "acme-prod")
	if problem := add("vertex-2", providers.Vertex, "/v1/projects/acme-prod/locations/global",
		providers.Credential{ServiceAccount: other.JSON}); !strings.Contains(problem, "Google refused the service-account key (invalid_grant)") {
		t.Fatalf("refused key: %q", problem)
	}
}
