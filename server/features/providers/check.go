package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/safehttp"

	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/ent/provider"
)

// Check calls the provider with its key (it lists its models, which costs
// nothing) and records whether the provider accepted it.
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
