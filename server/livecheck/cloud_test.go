package livecheck_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hasan-muzak/keel-core/pkg/events"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/converse"
	"github.com/hasan-muzak/promptdam/server/features/gatewaytest"
	"github.com/hasan-muzak/promptdam/server/features/providers"
	"github.com/hasan-muzak/promptdam/server/features/routes"
)

// maxCloudSpend stops TestClouds: a few cents is what it costs.
const maxCloudSpend = 0.25 // dollars

// cloudAccount is one cloud's test account, as .env names it, and the
// models to ask: Claude, and the cloud's others (its own family, open
// models, older Claude through Converse).
type cloudAccount struct {
	name   string
	input  providers.Input
	claude string
	others []string
}

// bedrockModels are the models a Bedrock account is asked besides
// current Claude (PROMPTDAM_TEST_BEDROCK_MODELS, comma-separated, else
// Nova Lite, Sonnet 4.5 and, in a US region, Llama 3.3, through the
// region's profiles).
func bedrockModels(region, list string) []string {
	if list != "" {
		return strings.Split(list, ",")
	}
	geo, _, _ := strings.Cut(region, "-")
	if geo == "ap" {
		geo = "apac"
	}
	out := []string{geo + ".amazon.nova-lite-v1:0", geo + ".anthropic.claude-sonnet-4-5-20250929-v1:0"}
	if geo == "us" {
		out = append(out, "us.meta.llama3-3-70b-instruct-v1:0")
	}
	return out
}

// cloudAccounts are the clouds .env has credentials for:
// PROMPTDAM_TEST_BEDROCK_REGION with _BEDROCK_API_KEY or
// _AWS_ACCESS_KEY_ID and _AWS_SECRET_ACCESS_KEY (_BEDROCK_MODELS: bedrockModels);
// PROMPTDAM_TEST_VERTEX_PROJECT, _LOCATION and _SERVICE_ACCOUNT_FILE (a path),
// _VERTEX_GEMINI the Gemini model to ask, _VERTEX_OPEN_MODEL an open one
// (meta/llama-3.3-70b-instruct-maas); PROMPTDAM_TEST_AZURE_RESOURCE,
// _API_KEY, _GPT_DEPLOYMENT and _CLAUDE_DEPLOYMENT.
func cloudAccounts(t *testing.T) []cloudAccount {
	t.Helper()
	env := func(k string) string { return strings.TrimSpace(os.Getenv("PROMPTDAM_TEST_" + k)) }
	var out []cloudAccount
	if region := env("BEDROCK_REGION"); region != "" {
		cred := providers.Credential{APIKey: env("BEDROCK_API_KEY")}
		if cred.APIKey == "" {
			cred = providers.Credential{AccessKeyID: env("AWS_ACCESS_KEY_ID"), SecretAccessKey: env("AWS_SECRET_ACCESS_KEY")}
		}
		out = append(out, cloudAccount{name: "Bedrock", claude: "claude-haiku-5-5", others: bedrockModels(region, env("BEDROCK_MODELS")),
			input: providers.Input{Name: "bedrock",
				Kind: providers.Bedrock, Cloud: providers.Cloud{Region: region}, Credential: &cred, Enabled: true}})
	}
	if file := env("VERTEX_SERVICE_ACCOUNT_FILE"); file != "" {
		key, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading the Vertex service-account file: %v", err)
		}
		gemini := env("VERTEX_GEMINI")
		if gemini == "" {
			gemini = "gemini-3.8-flash"
		}
		others := []string{gemini}
		if open := env("VERTEX_OPEN_MODEL"); open != "" {
			others = append(others, open)
		}
		out = append(out, cloudAccount{name: "Vertex", claude: "claude-haiku-5-5", others: others, input: providers.Input{Name: "vertex",
			Kind: providers.Vertex, Cloud: providers.Cloud{Project: env("VERTEX_PROJECT"), Location: env("VERTEX_LOCATION")},
			Credential: &providers.Credential{ServiceAccount: string(key)}, Enabled: true}})
	}
	if resource := env("AZURE_RESOURCE"); resource != "" {
		var others []string
		if gpt := env("AZURE_GPT_DEPLOYMENT"); gpt != "" {
			others = []string{gpt}
		}
		out = append(out, cloudAccount{name: "Azure", claude: env("AZURE_CLAUDE_DEPLOYMENT"), others: others,
			input: providers.Input{Name: "azure", Kind: providers.Azure, Cloud: providers.Cloud{Resource: resource},
				Credential: &providers.Credential{APIKey: env("AZURE_API_KEY")}, Enabled: true}})
	}
	return out
}

// TestClouds calls each cloud the owner has a test account for (.env,
// cloudAccounts; the others are skipped) through a Promptdam provider of its
// kind: the provider's check, then a conversation's two turns with Claude
// (the second, streamed, reading the first's cache), a Chat call to Claude
// (translated), and a call to the cloud's own family, streamed and not.
// It prints the request log's rows and stops past maxCloudSpend.
func TestClouds(t *testing.T) {
	accounts := cloudAccounts(t)
	if len(accounts) == 0 {
		t.Skip("no cloud test account in .env (PROMPTDAM_TEST_BEDROCK_*, _VERTEX_*, _AZURE_*)")
	}
	system, _ := json.Marshal(strings.Repeat(pilotSystem+"\n\n", 4)) // past every model's shortest cached prefix
	var spent float64
	for _, a := range accounts {
		t.Run(a.name, func(t *testing.T) {
			w := gatewaytest.New(t, gatewaytest.Options{ClickHouse: true})
			w.Grant(catalog.PromptCaching)
			in := a.input
			in.Models = append([]string{a.claude}, a.others...)
			p, err := w.App.Providers.Create(w.Scoped(), w.Org, w.Project, in)
			if err != nil {
				t.Fatal(err)
			}
			checked, err := w.App.Providers.Check(w.Scoped(), w.Project, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s at %s, credential %s: check %q", a.name, p.BaseURL, p.SecretHint, checked.CheckError)
			if _, err := w.App.Routes.Create(w.Scoped(), w.Org, w.Project, routes.Input{Name: "default", PromptCache: true}); err != nil {
				t.Fatal(err)
			}
			call := func(label, path, body string) {
				t.Helper()
				status, h, out := w.Call(path, body)
				if status != 200 {
					t.Errorf("%s: %d %s", label, status, out)
					return
				}
				t.Logf("%s: served by %s (%s)", label, h.Get("X-Promptdam-Served-Model"), h.Get("X-Promptdam-Provider"))
			}
			if a.claude != "" {
				first := `{"model":"` + a.claude + `","max_tokens":200,"system":` + string(system) + `,"messages":[` +
					`{"role":"user","content":"What is a CMR consignment note? Two sentences."}]}`
				w.Eventually(func() bool { // the provider reached the gateway
					status, h, _ := w.Call("/v1/messages", first)
					return status == 200 && h.Get("X-Promptdam-Provider") == a.input.Name
				})
				time.Sleep(2 * time.Second) // the cache entry is readable once the first answer is done
				call("Claude, the second turn, streamed", "/v1/messages", `{"model":"`+a.claude+`","max_tokens":200,"stream":true,`+
					`"system":`+string(system)+`,"messages":[{"role":"user","content":"What is a CMR consignment note? Two sentences."},`+
					`{"role":"assistant","content":"A CMR is the consignment note for international road transport."},`+
					`{"role":"user","content":"And who signs it?"}]}`)
				call("Claude, a Chat call", "/v1/chat/completions", `{"model":"`+a.claude+`","max_tokens":50,`+
					`"messages":[{"role":"user","content":"Say ok."}]}`)
			}
			for _, other := range a.others {
				call(other, "/v1/chat/completions", `{"model":"`+other+`","max_tokens":50,"messages":[{"role":"user","content":"Say ok."}]}`)
				call(other+", streamed", "/v1/chat/completions", `{"model":"`+other+`","max_tokens":50,"stream":true,`+
					`"messages":[{"role":"user","content":"Say ok."}]}`)
				if a.name == "Bedrock" && converse.CachingOf(other) != converse.CachesNothing {
					// Two turns of a long conversation: the second reads the
					// first's cache through Converse's checkpoints.
					turn := func(extra string) string {
						return `{"model":"` + other + `","max_tokens":100,"system":` + string(system) + `,"messages":[` +
							`{"role":"user","content":"What is a CMR consignment note? One sentence."}` + extra + `]}`
					}
					call(other+", a conversation's first turn", "/v1/messages", turn(""))
					time.Sleep(2 * time.Second)
					call(other+", its second turn", "/v1/messages", turn(`,{"role":"assistant","content":"The CMR is the road`+
						` consignment note."},{"role":"user","content":"Who signs it? One sentence."}`))
				}
			}
			if err := w.Log.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			var rows []struct {
				Served    string `ch:"served_model"`
				Status    uint16 `ch:"status"`
				Input     uint32 `ch:"input_tokens"`
				CacheRead uint32 `ch:"cache_read_tokens"`
				Write     uint32 `ch:"cache_write_tokens"`
				Output    uint32 `ch:"output_tokens"`
				Cost      int64  `ch:"cost_micros"`
				Error     string `ch:"error_code"`
			}
			if err := w.C.Events.Select(context.Background(), &rows, events.Project(w.Project), `SELECT served_model, status,
				input_tokens, cache_read_tokens, cache_write_tokens, output_tokens, cost_micros, error_code FROM requests
				WHERE {tenant} ORDER BY ts`); err != nil {
				t.Fatal(err)
			}
			b := &strings.Builder{}
			fmt.Fprintf(b, "\n| %s | Status | Input | Read from cache | Written | Output | Cost |\n|---|---|---|---|---|---|---|\n", a.name)
			for _, r := range rows {
				spent += float64(r.Cost) / 1e6
				fmt.Fprintf(b, "| %s %s | %d | %d | %d | %d | %d | $%.5f |\n", r.Served, r.Error, r.Status, r.Input, r.CacheRead, r.Write,
					r.Output, float64(r.Cost)/1e6)
			}
			t.Log(b.String())
			if spent > maxCloudSpend {
				t.Fatalf("spent $%.4f, past $%.2f", spent, maxCloudSpend)
			}
		})
	}
}
