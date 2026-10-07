package tiers

import (
	"errors"
	"strings"
	"testing"
)

func spec() Spec {
	return Spec{
		Tiers: []Tier{{Name: "fast", Model: "gpt-5-nano"}, {Name: "standard", Model: "gpt-5-mini"},
			{Name: "reasoning", Model: "claude-sonnet-5-5"}, {Name: "expert", Model: "anthropic/claude-opus-5-5"}},
		Rules: []Rule{{When: "legal", Tier: "expert"}, {When: "short", Tier: "fast"}},
	}
}

func TestCheck(t *testing.T) {
	s := spec()
	s.Tiers[0].Name, s.Tiers[0].Fallbacks = " fast ", []string{" groq/llama "}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	if s.Default != "expert" || s.Tiers[0].Name != "fast" || s.Tiers[0].Fallbacks[0] != "groq/llama" {
		t.Fatalf("tidied %+v", s)
	}
	for name, mutate := range map[string]func(*Spec){
		"no tiers":         func(s *Spec) { s.Tiers = nil },
		"too many tiers":   func(s *Spec) { s.Tiers = append(s.Tiers, s.Tiers...); s.Tiers = append(s.Tiers, s.Tiers[0]) },
		"bad name":         func(s *Spec) { s.Tiers[1].Name = "Big Model" },
		"same name":        func(s *Spec) { s.Tiers[1].Name = "fast" },
		"no model":         func(s *Spec) { s.Tiers[2].Model = " " },
		"long description": func(s *Spec) { s.Tiers[0].Description = strings.Repeat("x", MaxDescription+1) },
		"bad fallback":     func(s *Spec) { s.Tiers[0].Fallbacks = []string{"openai/"} },
		"empty example":    func(s *Spec) { s.Tiers[0].Examples = []string{" "} },
		"unknown default":  func(s *Spec) { s.Default = "huge" },
		"rule to nowhere":  func(s *Spec) { s.Rules[0].Tier = "huge" },
		"empty rule":       func(s *Spec) { s.Rules[1].When = "" },
	} {
		s := spec()
		mutate(&s)
		var ie *InvalidError
		if err := s.Check(); !errors.As(err, &ie) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestChoose(t *testing.T) {
	s := spec()
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	holds := func(conds ...string) func(string) bool {
		return func(when string) bool {
			for _, c := range conds {
				if c == when {
					return true
				}
			}
			return false
		}
	}
	all := func(int) bool { return true }
	only := func(ok ...int) func(int) bool {
		return func(i int) bool {
			for _, o := range ok {
				if o == i {
					return true
				}
			}
			return false
		}
	}
	for _, tt := range []struct {
		name   string
		call   Call
		tier   int
		source string
		rule   int
		moved  bool
	}{
		{"nothing places it", Call{Holds: holds(), Serves: all}, 3, SourceDefault, -1, false},
		{"the first rule that holds", Call{Holds: holds("short", "legal"), Serves: all}, 3, SourceRule, 0, false},
		{"a later rule", Call{Holds: holds("short"), Serves: all}, 0, SourceRule, 1, false},
		{"the client's tier wins", Call{Pin: "standard", Holds: holds("legal"), Serves: all}, 1, SourceExplicit, -1, false},
		{"a tier that can't serve moves up", Call{Holds: holds("short"), Serves: only(2, 3)}, 2, SourceRule, 1, true},
		{"down only when nothing stronger can", Call{Holds: holds(), Serves: only(1)}, 1, SourceDefault, -1, true},
		{"none can: the choice stays", Call{Pin: "fast", Serves: only()}, 0, SourceExplicit, -1, false},
	} {
		ch, err := s.Choose(tt.call)
		if err != nil || ch.Tier != tt.tier || ch.Source != tt.source || ch.Rule != tt.rule || ch.Moved != tt.moved {
			t.Errorf("%s: %+v, %v", tt.name, ch, err)
		}
	}
	if _, err := s.Choose(Call{Pin: "huge"}); !errors.Is(err, ErrUnknownTier) {
		t.Fatalf("an unknown tier: %v", err)
	}
}

func TestParseModel(t *testing.T) {
	routers := map[string]int{"auto": 1}
	for _, tt := range []struct {
		model, pin string
		ok         bool
	}{
		{"auto", "", true}, {"auto:fast", "fast", true}, {"gpt-5", "", false}, {"meta-llama/llama-3:free", "", false},
	} {
		_, pin, ok := ParseModel(tt.model, routers)
		if ok != tt.ok || ok && pin != tt.pin {
			t.Errorf("%s: %q %v", tt.model, pin, ok)
		}
	}
}
