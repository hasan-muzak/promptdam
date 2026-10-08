package payloads_test

import (
	"strings"
	"testing"

	"github.com/hasan-muzak/promptdam/server/features/payloads"
)

func TestCompile(t *testing.T) {
	if payloads.Compile(payloads.Defaults(), nil) != nil {
		t.Fatal("off, but compiled")
	}
	if payloads.Compile(payloads.Settings{Enabled: true}, nil) != nil {
		t.Fatal("a sample rate of 0 keeps nothing")
	}
	c := payloads.Compile(payloads.Settings{Enabled: true, SampleRate: 1, Answers: true}, map[string]string{"badge": `BADGE-\d{6}`})
	o, err := c.Mask.Inspect([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"key AKIAIOSFODNN7EXAMPLE, mail ada@example.com, BADGE-123456"}]}`))
	got := string(o.Body)
	if err != nil || o.Blocked || !strings.Contains(got, "[REDACTED:aws_access_key]") || !strings.Contains(got, "[REDACTED:email]") ||
		!strings.Contains(got, "[REDACTED:badge]") || !strings.Contains(got, `"model":"gpt-5"`) {
		t.Fatalf("masked %s %v", got, err)
	}
}
