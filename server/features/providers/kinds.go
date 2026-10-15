package providers

import "strings"

// Kind is a provider Promptdam knows: the API it speaks and where.
type Kind struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Format is the API it speaks: "openai" or "anthropic".
	Format string `json:"api_format"`
	// BaseURL is its API root (empty for custom: the project sets it).
	BaseURL string `json:"base_url"`
	// Catalog is its name in the model price catalog (pkg/llm), whose
	// models it serves unless the provider lists its own.
	Catalog string `json:"-"`
	// Prefixes start the names of its own models, so a model too new for
	// the catalog still finds its provider.
	Prefixes []string `json:"-"`
	// Cloud marks a cloud account (Bedrock, Vertex, Azure): where its models
	// are (Cloud) and how it signs in (Credential) replace a base URL and a
	// key, and its models' prices are their vendors'. Vendors are the
	// catalogs whose models it serves.
	Cloud   bool     `json:"cloud"`
	Vendors []string `json:"-"`
}

// API formats.
const (
	FormatOpenAI    = "openai"
	FormatAnthropic = "anthropic"
)

// Custom is an endpoint of the project's choice speaking either API (a
// self-hosted model, another gateway).
const Custom = "custom"

// Kinds are the providers projects can add.
var Kinds = []Kind{
	{Name: "openai", Label: "OpenAI", Format: FormatOpenAI, BaseURL: "https://api.openai.com/v1", Catalog: "openai",
		Prefixes: []string{"gpt-", "o1", "o3", "o4", "chatgpt-", "codex-", "text-embedding-", "omni-moderation", "computer-use"}},
	{Name: "anthropic", Label: "Anthropic", Format: FormatAnthropic, BaseURL: "https://api.anthropic.com/v1", Catalog: "anthropic",
		Prefixes: []string{"claude-"}},
	{Name: "gemini", Label: "Google Gemini", Format: FormatOpenAI,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Catalog: "gcp.gemini",
		Prefixes: []string{"gemini-", "gemma-"}},
	{Name: "mistral", Label: "Mistral", Format: FormatOpenAI, BaseURL: "https://api.mistral.ai/v1", Catalog: "mistral",
		Prefixes: []string{"mistral-", "codestral-", "pixtral-", "ministral-", "magistral-", "devstral-", "open-mistral", "open-mixtral"}},
	{Name: "deepseek", Label: "DeepSeek", Format: FormatOpenAI, BaseURL: "https://api.deepseek.com/v1", Catalog: "deepseek",
		Prefixes: []string{"deepseek-"}},
	{Name: "groq", Label: "Groq", Format: FormatOpenAI, BaseURL: "https://api.groq.com/openai/v1", Catalog: "groq"},
	{Name: "together", Label: "Together AI", Format: FormatOpenAI, BaseURL: "https://api.together.xyz/v1", Catalog: "together"},
	{Name: "xai", Label: "xAI", Format: FormatOpenAI, BaseURL: "https://api.x.ai/v1", Catalog: "xai",
		Prefixes: []string{"grok-"}},
	{Name: "fireworks", Label: "Fireworks AI", Format: FormatOpenAI, BaseURL: "https://api.fireworks.ai/inference/v1", Catalog: "fireworks"},
	{Name: "openrouter", Label: "OpenRouter", Format: FormatOpenAI, BaseURL: "https://openrouter.ai/api/v1", Catalog: "openrouter"},
	{Name: Bedrock, Label: "AWS Bedrock", Format: FormatAnthropic, Cloud: true, Vendors: []string{"anthropic"},
		Prefixes: []string{"claude-", "anthropic.", "amazon.", "meta.", "mistral.", "cohere.", "deepseek.", "qwen.", "minimax.",
			"ai21.", "openai.", "us.", "eu.", "apac.", "jp.", "au.", "ca.", "in.", "global.", "us-gov."}},
	{Name: Vertex, Label: "Google Vertex AI", Format: FormatAnthropic, Cloud: true, Vendors: []string{"anthropic", "gcp.gemini"},
		Prefixes: []string{"claude-", "gemini-", "google/"}},
	{Name: Azure, Label: "Azure AI Foundry", Format: FormatOpenAI, Cloud: true, Vendors: []string{"openai", "anthropic"},
		Prefixes: []string{"gpt-", "o1", "o3", "o4", "claude-", "text-embedding-"}},
	{Name: Custom, Label: "Custom endpoint"},
}

// Public reports a provider on its kind's own public API (the kind's base
// URL; never a custom endpoint): its calls are the same for every project,
// so what they show of a model may be pooled (features/cacheprofile). A
// base URL a project overrides may be a server of its own. Bedrock's and
// Vertex's accounts are public (their models are named the same for
// everyone); Azure's aren't: a model there is a deployment the project
// named.
func Public(kind, baseURL string) bool {
	if kind == Bedrock || kind == Vertex {
		built, err := CloudBaseURL(kind, CloudOf(kind, baseURL))
		return err == nil && built == strings.TrimRight(baseURL, "/")
	}
	k, ok := LookupKind(kind)
	return ok && kind != Custom && k.BaseURL != "" && strings.TrimRight(baseURL, "/") == strings.TrimRight(k.BaseURL, "/")
}

// LookupKind finds a kind by name.
func LookupKind(name string) (Kind, bool) {
	for _, k := range Kinds {
		if k.Name == name {
			return k, true
		}
	}
	return Kind{}, false
}
