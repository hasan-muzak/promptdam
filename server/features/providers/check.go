package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
	"github.com/hasan-muzak/keel-core/pkg/safehttp"

	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/provider"
	"github.com/hasan-muzak/promptdam/server/features/cloudauth"
	"github.com/hasan-muzak/promptdam/server/features/gateway"
)

// Check calls the provider with its key (it lists its models, which costs
// nothing; a cloud's Claude API lists none, so it answers one token) and
// records whether the provider accepted it.
func (s *Service) Check(ctx context.Context, project, id uuid.UUID) (*pdent.Provider, error) {
	p, err := s.Get(ctx, project, id)
	if err != nil {
		return nil, err
	}
	problem := s.call(ctx, p)
	return s.cfg.Ent.Provider.UpdateOne(p).SetCheckedAt(time.Now().UTC()).SetCheckError(problem).Save(ctx)
}

// call lists the provider's models; it returns what went wrong, "" when
// nothing did. Messages never quote the provider's answer, which may echo
// the key.
func (s *Service) call(ctx context.Context, p *pdent.Provider) string {
	if IsCloud(p.Kind) {
		return s.callCloud(ctx, p)
	}
	header, err := s.Header(p)
	if err != nil {
		return "the stored key can't be read: set it again"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/models", nil)
	if err != nil {
		return "the base URL is invalid"
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if p.APIFormat == provider.APIFormatAnthropic {
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	res, err := s.cfg.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, safehttp.ErrBlocked) {
			return "the base URL points at an address that isn't public"
		}
		return "the provider could not be reached"
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	_ = res.Body.Close()
	switch {
	case res.StatusCode < 300:
		return ""
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return "the provider refused the key"
	default:
		return fmt.Sprintf("the provider answered %d", res.StatusCode)
	}
}

// checkModel is the model a cloud account's check asks: the first it lists,
// else Claude's smallest, the model most projects reach a cloud for.
func checkModel(p *pdent.Provider) string {
	if len(p.Models) > 0 && !strings.HasSuffix(p.Models[0], "*") {
		return p.Models[0]
	}
	return "claude-haiku-5-5"
}

// callCloud checks a cloud account: Azure's key lists its resource's
// models; Bedrock and Vertex answer one token from checkModel, so a wrong
// region, a model not enabled there, or credentials that may not call it
// show at setup. What went wrong is the status's meaning, never the
// cloud's own words, which may quote the request.
func (s *Service) callCloud(ctx context.Context, p *pdent.Provider) string {
	header, auth, err := s.Auth(p.OrgID, p.Kind, string(p.APIFormat), p.Secrets)
	if err != nil {
		return "the stored credential can't be read: set it again"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	model := checkModel(p)
	t := gateway.Target{Kind: p.Kind, BaseURL: p.BaseURL, Model: CloudModel(p.Kind, model), Header: header, Auth: auth}
	method, f, path := http.MethodPost, wire.AnthropicMessages, "/messages"
	body := []byte(`{"model":` + quote(t.Model) + `,"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`)
	switch {
	case p.Kind == Azure:
		method, f, path, body = http.MethodGet, wire.OpenAIChat, "/models", nil
	case FormatFor(p.Kind, "", model) == FormatOpenAI:
		f, path = wire.OpenAIChat, "/chat/completions"
	}
	body, _, err = gateway.CloudBody(t, f, path, body)
	if err != nil {
		return "the check can't be sent to " + model
	}
	req, err := http.NewRequestWithContext(ctx, method, gateway.UpstreamURL(t, f, path, false), bytes.NewReader(body))
	if err != nil {
		return "the account's address is invalid"
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if f.Anthropic() {
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	gateway.CloudHeader(t, f, req.Header, false)
	if err := gateway.Sign(ctx, t, req, body); err != nil {
		var refused *cloudauth.TokenError
		if errors.As(err, &refused) {
			return "Google refused the service-account key (" + refused.Code + "): check it wasn't deleted or disabled"
		}
		return "the credential couldn't sign a call"
	}
	res, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return "the cloud could not be reached"
	}
	defer func() { _ = res.Body.Close() }()
	gateway.CloudResponse(t, f, res)
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode < 300:
		return ""
	case res.StatusCode == http.StatusUnauthorized:
		return "the cloud refused the credentials"
	case res.StatusCode == http.StatusForbidden:
		return "the credentials may not call " + model + ": grant the permission, or enable the model for the account"
	case res.StatusCode == http.StatusNotFound:
		return model + " isn't available to this account here: enable it, or list the models it serves"
	case res.StatusCode == http.StatusTooManyRequests:
		return "" // signed in; the account is only busy
	default:
		return fmt.Sprintf("the cloud answered %d", res.StatusCode)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
