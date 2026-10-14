package routers

import "testing"

// A judge from the same vendor as one side of a cross-vendor comparison
// may favor it.
func TestJudgeLeans(t *testing.T) {
	for _, tt := range []struct{ judge, tier, def, want string }{
		{"claude-sonnet-5-5", "gpt-5-mini", "claude-opus-5-5", "default"},
		{"claude/claude-sonnet-5-5", "claude/claude-haiku-5-5", "openai/gpt-6", "tier"},
		{"claude-sonnet-5-5", "claude-haiku-5-5", "claude-opus-5-5", ""}, // one vendor: nothing to favor
		{"gemini-3-pro", "gpt-5-mini", "claude-opus-5-5", ""},            // a third vendor
		{"my-judge", "gpt-5-mini", "claude-opus-5-5", ""},                // unknown
	} {
		if got := leans(tt.judge, tt.tier, tt.def); got != tt.want {
			t.Errorf("leans(%s, %s, %s) = %q, want %q", tt.judge, tt.tier, tt.def, got, tt.want)
		}
	}
}
