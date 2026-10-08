package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/apikeys"
	"github.com/hasan-muzak/keel-core/pkg/cli"
	"github.com/hasan-muzak/keel-core/pkg/config"
	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/identity"
	"github.com/hasan-muzak/keel-core/pkg/keel"

	"github.com/hasan-muzak/promptdam/server/brand"
	"github.com/hasan-muzak/promptdam/server/core"
	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// devCommands help local development (dev seed refuses to run
// elsewhere) and operations.
func devCommands(c *keel.Core, app *core.App) []cli.Command {
	return []cli.Command{{
		Name:  "prices sync",
		Usage: "fetch the model price list from OpenRouter now and make it current (the worker does it daily)",
		Run: func(ctx context.Context, _ []string, out io.Writer) error {
			if err := app.Prices.Sync(ctx, true); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "%d models priced\n", len(app.Prices.Current().Models))
			return err
		},
	}, {
		Name: "dev seed",
		Usage: "development only: create an organization, a project, a gateway key and two providers (OpenAI and " +
			"Anthropic format) at -upstream (default the fake provider, `task promptdam:dev:fake`), and print how to call them; " +
			"with -openai-key or -anthropic-key, the real providers instead",
		Run: func(ctx context.Context, args []string, out io.Writer) error {
			if c.Config.Env != config.Development {
				return errors.New("dev seed runs only with KEEL_ENV=development")
			}
			fs := flag.NewFlagSet("dev seed", flag.ContinueOnError)
			upstream := fs.String("upstream", "http://localhost:8299/v1", "the providers' base URL")
			providerKey := fs.String("provider-key", "sk-fake-provider-key", "the providers' API key")
			name := fs.String("name", "Dev", "the organization's name")
			openaiKey := fs.String("openai-key", "", "an OpenAI API key: add the real OpenAI provider instead of the fake ones")
			anthropicKey := fs.String("anthropic-key", "", "an Anthropic API key: add the real Anthropic provider instead of the fake ones")
			if err := fs.Parse(args); err != nil {
				return err
			}
			var real []realProvider
			if *openaiKey != "" {
				real = append(real, realProvider{kind: "openai", key: *openaiKey})
			}
			if *anthropicKey != "" {
				real = append(real, realProvider{kind: "anthropic", key: *anthropicKey})
			}
			return seed(ctx, c, app, out, *name, *upstream, *providerKey, real)
		},
	}}
}

// realProvider is a provider of a known kind, at its own API.
type realProvider struct{ kind, key string }

func seed(ctx context.Context, c *keel.Core, app *core.App, out io.Writer, name, upstream, providerKey string, real []realProvider) error {
	u, err := c.Tenancy.SignIn(ctx, identity.Claims{UID: "dev-seed", Email: "dev@" + brand.Key + ".test", EmailVerified: true, Name: "Dev"})
	if err != nil {
		return err
	}
	org, err := c.Tenancy.CreateOrg(ctx, u.ID, name, "")
	if err != nil {
		return err
	}
	prj, err := c.Tenancy.CreateProject(ctx, org.ID, "Dev", "")
	if err != nil {
		return err
	}
	_, key, err := c.APIKeys.Issue(ctx, apikeys.IssueInput{
		Org: org.ID, Project: &prj.ID, Name: "dev", Kind: apikeys.Secret, Scopes: []string{core.ScopeGateway}, CreatedBy: &u.ID,
	})
	if err != nil {
		return err
	}
	scoped := entx.WithOrg(ctx, org.ID)
	where, model := "providers at "+upstream, "gpt-5-mini"
	if len(real) == 0 {
		for _, p := range []struct{ name, format string }{{"openai", providers.FormatOpenAI}, {"anthropic", providers.FormatAnthropic}} {
			if _, err := app.Providers.Create(scoped, org.ID, prj.ID, providers.Input{
				Name: p.name, Kind: providers.Custom, Format: p.format, BaseURL: upstream, APIKey: &providerKey, Enabled: true,
			}); err != nil {
				return fmt.Errorf("adding the %s provider: %w", p.name, err)
			}
		}
	} else {
		var kinds []string
		for _, p := range real {
			kind, _ := providers.LookupKind(p.kind)
			if _, err := app.Providers.Create(scoped, org.ID, prj.ID, providers.Input{
				Name: p.kind, Kind: p.kind, Format: kind.Format, BaseURL: kind.BaseURL, APIKey: &p.key, Enabled: true,
			}); err != nil {
				return fmt.Errorf("adding the %s provider: %w", p.kind, err)
			}
			kinds = append(kinds, kind.Label)
		}
		where = strings.Join(kinds, " and ") + ", with your keys"
		if real[0].kind == "anthropic" {
			model = "claude-haiku-4-5"
		}
	}
	gw := app.GatewayURL
	_, err = fmt.Fprintf(out, `Seeded %q: %s.

  export OPENAI_BASE_URL=%s/v1 OPENAI_API_KEY=%s
  export ANTHROPIC_BASE_URL=%s ANTHROPIC_API_KEY=%s

  curl %s/v1/chat/completions -H "Authorization: Bearer %s" \
    -H "Content-Type: application/json" -d '{"model":"%s","messages":[{"role":"user","content":"hi"}]}'
`, name, where, gw, key, gw, key, gw, key, model)
	return err
}
