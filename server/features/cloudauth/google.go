package cloudauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// googleScope is what a Vertex token may do: call Google Cloud's APIs, as
// the service account's own roles allow.
const googleScope = "https://www.googleapis.com/auth/cloud-platform"

// A token is fetched again this long before it expires: a call signed with
// it must reach Google before it lapses.
const refreshBefore = 5 * time.Minute

// tokensUnused drops a provider's token cache this long after its last use
// (a provider deleted or its key replaced).
const tokensUnused = 2 * time.Hour

// Tokens mints and keeps Google access tokens for service-account keys,
// one cache per key across snapshot rebuilds: a token is fetched once an
// hour, not once a snapshot. Concurrent calls wait for one fetch.
type Tokens struct {
	client   *http.Client
	tokenURL string
	now      func() time.Time

	mu      sync.Mutex
	sources map[string]*googleSource
}

// NewTokens returns token caches fetching through client (a safehttp
// client) from tokenURL (GoogleTokenURL; a fake in tests).
func NewTokens(client *http.Client, tokenURL string) *Tokens {
	return &Tokens{client: client, tokenURL: tokenURL, now: time.Now, sources: map[string]*googleSource{}}
}

// Google is the authorizer of a service-account key; key identifies it
// (a hash of its sealed form), so a provider's tokens outlive a snapshot.
func (t *Tokens) Google(key string, sa *ServiceAccount) Authorizer {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, s := range t.sources {
		if now.Sub(time.Unix(0, s.used.Load())) > tokensUnused {
			delete(t.sources, k)
		}
	}
	s, ok := t.sources[key]
	if !ok {
		s = &googleSource{tokens: t, sa: sa}
		t.sources[key] = s
	}
	s.used.Store(now.UnixNano())
	return s
}

// fetchTimeout bounds one token fetch: calls wait on it.
const fetchTimeout = 10 * time.Second

type googleSource struct {
	tokens *Tokens
	sa     *ServiceAccount
	// used is when the source was last asked for (unix nanoseconds), read
	// without its lock: a fetch holds that.
	used atomic.Int64

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (s *googleSource) Authorize(ctx context.Context, req *http.Request, _ []byte) error {
	token, err := s.get(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// get is a token good for at least refreshBefore, fetched when needed.
func (s *googleSource) get(ctx context.Context) (string, error) {
	now := s.tokens.now()
	s.used.Store(now.UnixNano())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && now.Add(refreshBefore).Before(s.expires) {
		return s.token, nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	token, lifetime, err := s.fetch(ctx, now)
	if err != nil {
		return "", err
	}
	s.token, s.expires = token, now.Add(lifetime)
	return token, nil
}

// fetch exchanges a JWT the service account signs for an access token.
func (s *googleSource) fetch(ctx context.Context, now time.Time) (string, time.Duration, error) {
	assertion, err := s.sa.assertion(s.tokens.tokenURL, now)
	if err != nil {
		return "", 0, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokens.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := s.tokens.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("cloudauth: Google's token service: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return "", 0, fmt.Errorf("cloudauth: Google's token service: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Google's error names the problem (invalid_grant: a key deleted);
		// it never echoes the key.
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &e)
		return "", 0, &TokenError{Status: res.StatusCode, Code: e.Error, Description: e.Description}
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" || t.ExpiresIn <= 0 {
		return "", 0, errors.New("cloudauth: Google's token service answered without a token")
	}
	return t.AccessToken, time.Duration(t.ExpiresIn) * time.Second, nil
}

// TokenError is Google refusing a service-account key.
type TokenError struct {
	Status            int
	Code, Description string
}

func (e *TokenError) Error() string {
	return fmt.Sprintf("cloudauth: Google refused the service-account key (%d %s: %s)", e.Status, e.Code, e.Description)
}

// assertion is the RS256 JWT a service account signs to ask for a token
// (an hour, the longest Google grants).
func (sa *ServiceAccount) assertion(aud string, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": sa.PrivateKeyID})
	claims, _ := json.Marshal(map[string]any{
		"iss": sa.ClientEmail, "scope": googleScope, "aud": aud, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, sa.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("cloudauth: signing the token request: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
