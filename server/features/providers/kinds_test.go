package providers_test

import (
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/providers"
)

// Only a kind's own public API is public: a base URL a project overrides
// may be a server of its own, and a custom endpoint always is.
func TestPublic(t *testing.T) {
	for _, tt := range []struct {
		kind, base string
		want       bool
	}{
		{"openrouter", "https://openrouter.ai/api/v1", true},
		{"openrouter", "https://openrouter.ai/api/v1/", true},
		{"openrouter", "https://proxy.example.com/api/v1", false},
		{"openai", "https://api.openai.com/v1", true},
		{"custom", "https://openrouter.ai/api/v1", false},
		{"nonsense", "https://openrouter.ai/api/v1", false},
	} {
		if got := providers.Public(tt.kind, tt.base); got != tt.want {
			t.Errorf("%s at %s: %v", tt.kind, tt.base, got)
		}
	}
}
