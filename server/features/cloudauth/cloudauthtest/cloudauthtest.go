// Package cloudauthtest makes cloud credentials for tests: a service-account
// key file with a fresh RSA key, and a fake Google token service that checks
// the assertions it is sent.
package cloudauthtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ServiceAccount is a service-account key file for a project, and its key.
type ServiceAccount struct {
	JSON  string
	Email string
	Key   *rsa.PrivateKey
}

// NewServiceAccount makes a service-account key file with a fresh key.
func NewServiceAccount(t testing.TB, project string) ServiceAccount {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	email := "promptdam@" + project + ".iam.gserviceaccount.com"
	b, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": project, "private_key_id": "k1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": email, "token_uri": "https://oauth2.googleapis.com/token",
	})
	return ServiceAccount{JSON: string(b), Email: email, Key: key}
}

// TokenServer is a fake Google token service for one service account: it
// verifies each assertion's signature and claims, and hands out
// "token-<n>", Fetches counting them.
type TokenServer struct {
	*httptest.Server
	Fetches atomic.Int32
}

// NewTokenServer starts a token service trusting sa's key.
func NewTokenServer(t testing.TB, sa ServiceAccount) *TokenServer {
	t.Helper()
	ts := &TokenServer{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
			return
		}
		if err := verify(r.Form.Get("assertion"), sa, ts.URL); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, err.Error())
			return
		}
		n := ts.Fetches.Add(1)
		_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":3600,"token_type":"Bearer"}`, n)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// verify checks an assertion as Google does: RS256 by the account's key,
// issued by its email, for this token service, asking for Cloud's scope.
func verify(jwt string, sa ServiceAccount, aud string) error {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return fmt.Errorf("not a JWT")
	}
	enc := base64.RawURLEncoding
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&sa.Key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		return fmt.Errorf("bad signature")
	}
	raw, err := enc.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var c struct {
		Iss, Scope, Aud string
		Iat, Exp        int64
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	switch {
	case c.Iss != sa.Email:
		return fmt.Errorf("issuer %q", c.Iss)
	case c.Aud != aud:
		return fmt.Errorf("audience %q", c.Aud)
	case c.Scope != "https://www.googleapis.com/auth/cloud-platform":
		return fmt.Errorf("scope %q", c.Scope)
	case c.Exp-c.Iat != 3600:
		return fmt.Errorf("lifetime %d", c.Exp-c.Iat)
	}
	return nil
}
