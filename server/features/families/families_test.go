package families_test

import (
	"strings"
	"testing"

	"github.com/hasan-muzak/keel/pkg/llm/wire"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/families"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
		flags    families.Flags
	}{
		{"You are a helpful   assistant.\n\nBe brief.", "You are a helpful assistant. Be brief.", 0},
		{"Today is 2026-10-07. The time is 14:05:09.", "Today is <date>. The time is <time>.", families.Dates | families.Times},
		{"Now: 2026-10-07T14:05:09.123Z, then 07/10/2026 and 7.10.2026",
			"Now: <datetime>, then <date> and <date>", families.Dates | families.Times},
		{"Request 9f8b2c1e-4a5d-4e6f-8a9b-0c1d2e3f4a5b for user 42",
			"Request <uuid> for user <n>", families.UUIDs | families.Numbers},
		{"trace 4bf92f3577b34da6a3ce929d0e0e4736 ok", "trace <id> ok", families.HexIDs},
		{"Use gpt4 v2 and step 3, costs 1,234.50 USD", "Use gpt4 v2 and step <n>, costs <n> USD", families.Numbers},
		{"Call 555-0100 or wait 100ms", "Call <n>-<n> or wait 100ms", families.Numbers},
		{"Türkçe metin: Merhaba dünya 2026", "Türkçe metin: Merhaba dünya <n>", families.Numbers},
		{"deadbeef is a word", "deadbeef is a word", 0},
	}
	for _, tt := range tests {
		got, flags := families.Normalize(tt.in)
		if got != tt.want || flags != tt.flags {
			t.Errorf("Normalize(%q) = %q, %v; want %q, %v", tt.in, got, flags.Names(), tt.want, tt.flags.Names())
		}
	}
}

func TestFamilies(t *testing.T) {
	prefix := func(system string, tools ...string) wire.Prefix {
		p := wire.Prefix{System: system, Tools: tools}
		if len(tools) > 0 {
			p.ToolsRaw = []byte(`[` + strings.Join(tools, ",") + `]`)
		}
		return p
	}
	a := families.Of(prefix("Summarise ticket 1234 from 2026-10-07.", "search", "fetch"), "")
	b := families.Of(prefix("Summarise ticket 98 from 2026-10-08.", "fetch", "search"), "")
	if a.Family == "" || a.Family != b.Family {
		t.Fatalf("one template, two families: %q %q", a.Family, b.Family)
	}
	// The same family with prefixes that differ: they miss the cache.
	if a.Prefix == b.Prefix || a.Flags&families.Dates == 0 || a.Flags&families.ToolsUnsorted == 0 ||
		b.Flags&families.ToolsUnsorted != 0 {
		t.Fatalf("shapes %+v %+v", a, b)
	}
	if c := families.Of(prefix("Translate to French.", "search", "fetch"), ""); c.Family == a.Family {
		t.Fatal("another template, the same family")
	}
	if d := families.Of(prefix("Summarise ticket 1 from 2026-10-07.", "search"), ""); d.Family == a.Family {
		t.Fatal("other tools, the same family")
	}
	if f := families.Of(wire.Prefix{System: "x", Format: "json_schema:answer"}, ""); f.Family == families.Of(wire.Prefix{System: "x"}, "").Family {
		t.Fatal("another answer format, the same family")
	}
	if n := families.Of(prefix("anything"), "invoice-extract"); n.Family != "name:invoice-extract" {
		t.Fatalf("named %q", n.Family)
	}
	if n := families.Of(prefix("anything"), "bad name!"); strings.HasPrefix(n.Family, "name:") {
		t.Fatal("an invalid name was used")
	}
	if e := families.Of(wire.Prefix{}, ""); e.Family != "" || e.Prefix != "" {
		t.Fatalf("no instructions, no family: %+v", e)
	}
}

func BenchmarkOf(b *testing.B) {
	system := strings.Repeat("You are an assistant for Acme. Today is 2026-10-07 and order 12345 is late. ", 50)
	p := wire.Prefix{System: system, Tools: []string{"search", "fetch", "create_ticket"}, ToolsRaw: []byte(`[{"x":1}]`)}
	b.SetBytes(int64(len(system)))
	b.ReportAllocs()
	for b.Loop() {
		families.Of(p, "")
	}
}
