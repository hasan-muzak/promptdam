package providers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"
)

// ServiceAccountJSON is a service-account key file with a fresh RSA key,
// for tests.
func ServiceAccountJSON(t testing.TB, project string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": project, "private_key_id": "k1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "promptdam@" + project + ".iam.gserviceaccount.com", "token_uri": "https://oauth2.googleapis.com/token",
	})
	return string(b)
}

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
	sa := ServiceAccountJSON(t, "acme-prod")
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
