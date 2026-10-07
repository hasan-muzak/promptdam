package providers

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
	{Name: Custom, Label: "Custom endpoint"},
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
