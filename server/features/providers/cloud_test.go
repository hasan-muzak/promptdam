package providers

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/cloudauth/cloudauthtest"
)

// Each cloud's account becomes the root of its model APIs and reads back
// from it; Bedrock's and Vertex's are public (pooled), Azure's and an
// overridden one aren't.
func TestCloudBaseURL(t *testing.T) {
	for _, tt := range []struct {
		kind  string
		cloud Cloud
		want  string
	}{
		{Bedrock, Cloud{Region: "eu-west-1"}, "https://bedrock-mantle.eu-west-1.api.aws"},
		{Bedrock, Cloud{Region: "us-gov-west-1"}, "https://bedrock-mantle.us-gov-west-1.api.aws"},
		{Vertex, Cloud{Project: "acme-prod"}, "https://aiplatform.googleapis.com/v1/projects/acme-prod/locations/global"},
		{Vertex, Cloud{Project: "acme-prod", Location: "eu"}, "https://aiplatform.eu.rep.googleapis.com/v1/projects/acme-prod/locations/eu"},
		{Vertex, Cloud{Project: "acme-prod", Location: "europe-west4"},
			"https://europe-west4-aiplatform.googleapis.com/v1/projects/acme-prod/locations/europe-west4"},
		{Azure, Cloud{Resource: "Acme-AI"}, "https://acme-ai.services.ai.azure.com"},
	} {
		got, err := CloudBaseURL(tt.kind, tt.cloud)
		if err != nil || got != tt.want {
			t.Errorf("%s %+v: %q, %v", tt.kind, tt.cloud, got, err)
			continue
		}
		back := CloudOf(tt.kind, got)
		if again, _ := CloudBaseURL(tt.kind, back); again != got {
			t.Errorf("%s: %q reads back as %+v", tt.kind, got, back)
		}
		if Public(tt.kind, got) != (tt.kind != Azure) {
			t.Errorf("%s at %s: public %v", tt.kind, got, Public(tt.kind, got))
		}
	}
	for _, bad := range []struct {
		kind  string
		cloud Cloud
	}{
		{Bedrock, Cloud{}}, {Bedrock, Cloud{Region: "eu-west-1.evil.com"}}, {Vertex, Cloud{Project: "a"}},
		{Vertex, Cloud{Project: "acme-prod", Location: "../x"}}, {Azure, Cloud{Resource: "a.b"}},
	} {
		if got, err := CloudBaseURL(bad.kind, bad.cloud); err == nil {
			t.Errorf("%s %+v: %q accepted", bad.kind, bad.cloud, got)
		}
	}
	if Public(Bedrock, "https://bedrock.example.com") || Public(Vertex, "http://127.0.0.1:9/v1/projects/acme-prod/locations/global") {
		t.Error("an overridden cloud URL is public")
	}
}

// Each cloud takes its own sign-in methods, one at a time, and hints at
// which key it holds without showing it.
func TestCredentialCheck(t *testing.T) {
	sa := cloudauthtest.NewServiceAccount(t, "acme-prod").JSON
	for _, tt := range []struct {
		name string
		kind string
		c    Credential
		hint string
	}{
		{"a Bedrock API key", Bedrock, Credential{APIKey: "xxxxxxxxxxxxxxxxc123"}, "…c123"},
		{"Bedrock IAM keys", Bedrock, Credential{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "s"}, "…MPLE"},
		{"a Vertex service account", Vertex, Credential{ServiceAccount: sa}, "promptdam@acme-prod.iam.gserviceaccount.com"},
		{"an Azure key", Azure, Credential{APIKey: "test-azure-key-0123"}, "…0123"},
	} {
		hint, err := tt.c.check(tt.kind)
		if err != nil || hint != tt.hint {
			t.Errorf("%s: %q, %v", tt.name, hint, err)
		}
	}
	var forged map[string]string
	_ = json.Unmarshal([]byte(sa), &forged)
	forged["token_uri"] = "https://attacker.example/token"
	forgedJSON, _ := json.Marshal(forged)
	for _, tt := range []struct {
		name string
		kind string
		c    Credential
	}{
		{"Bedrock with both", Bedrock, Credential{APIKey: "k", AccessKeyID: "a", SecretAccessKey: "s"}},
		{"Bedrock without the secret", Bedrock, Credential{AccessKeyID: "a"}},
		{"Vertex with a key", Vertex, Credential{APIKey: "k"}},
		{"Vertex with a key file sending tokens elsewhere", Vertex, Credential{ServiceAccount: string(forgedJSON)}},
		{"Vertex with no key file", Vertex, Credential{ServiceAccount: `{"type":"authorized_user"}`}},
		{"Azure without a key", Azure, Credential{}},
	} {
		var invalid *InvalidError
		if _, err := tt.c.check(tt.kind); !errors.As(err, &invalid) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}

// A cloud calls Claude in Anthropic's API, the others in the
// OpenAI-compatible one, each by the cloud's own name; Bedrock calls every
// model in Anthropic's format (Converse is its dialect).
func TestCloudModels(t *testing.T) {
	for _, tt := range []struct {
		kind, model, format, id string
	}{
		{Bedrock, "claude-haiku-5-5", FormatAnthropic, "anthropic.claude-haiku-5-5"},
		{Bedrock, "anthropic.claude-haiku-5-5", FormatAnthropic, "anthropic.claude-haiku-5-5"},
		{Bedrock, "openai.gpt-oss-120b-1:0", FormatAnthropic, "openai.gpt-oss-120b-1:0"}, // through Converse
		{Bedrock, "us.meta.llama3-3-70b-instruct-v1:0", FormatAnthropic, "us.meta.llama3-3-70b-instruct-v1:0"},
		{Vertex, "gemini-3.8-flash", FormatOpenAI, "google/gemini-3.8-flash"},
		{Vertex, "claude-haiku-4-5@20251001", FormatAnthropic, "claude-haiku-4-5@20251001"},
		{Azure, "my-gpt", FormatOpenAI, "my-gpt"},
		{"openai", "claude-x", FormatOpenAI, "claude-x"},
	} {
		if f := FormatFor(tt.kind, FormatOpenAI, tt.model); f != tt.format {
			t.Errorf("%s %s: format %s", tt.kind, tt.model, f)
		}
		if id := CloudModel(tt.kind, tt.model); id != tt.id {
			t.Errorf("%s %s: id %s", tt.kind, tt.model, id)
		}
	}
}
