package firewall

import (
	"slices"
	"testing"

	"github.com/hasan-muzak/promptdam/server/catalog"
)

func TestModelSettingsChecked(t *testing.T) {
	env, _ := NewEnv()
	for name, bad := range map[string]func(*Settings){
		"unknown label":        func(s *Settings) { s.Model.Labels = map[string]string{"shoe_size": Redact} },
		"bad action":           func(s *Settings) { s.Model.Labels = map[string]string{"person": "hide"} },
		"tokenized credential": func(s *Settings) { s.Model.Labels = map[string]string{"credential": Tokenize} },
		"fail mode":            func(s *Settings) { s.Model.FailMode = "sometimes" },
		"sensitive action":     func(s *Settings) { s.Sensitive = "hide" },
		"empty allow term":     func(s *Settings) { s.Model.Allow = []string{" "} },
		"custom named a label": func(s *Settings) { s.Custom = map[string]string{"health": "x+"} },
	} {
		s := Defaults()
		bad(&s)
		if err := s.Check(env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s := Defaults()
	s.Model.Allow = []string{"Promptdam", "promptdam ", "Ayşe from Support"}
	if err := s.Check(env); err != nil || len(s.Model.Allow) != 2 || s.Model.FailMode != ModelFailOpen {
		t.Fatalf("%v %+v", err, s.Model)
	}
}

// A label takes its own action, else its class's; dates and places are off
// unless chosen; nothing acts while the model doesn't read for its kind.
func TestLabelActions(t *testing.T) {
	s := Defaults()
	s.PII, s.Sensitive = Tokenize, Alert
	person, _ := modelLabel("person")
	location, _ := modelLabel("location")
	health, _ := modelLabel("health")
	cred, _ := modelLabel("credential")
	if s.labelAction(person) != Off || s.labelAction(cred) != Off {
		t.Fatal("acts without the model")
	}
	s.Model.PII, s.Model.Secrets = true, true
	if s.labelAction(person) != Tokenize || s.labelAction(location) != Off || s.labelAction(health) != Alert ||
		s.labelAction(cred) != Block {
		t.Fatalf("defaults: %s %s %s %s", s.labelAction(person), s.labelAction(location), s.labelAction(health), s.labelAction(cred))
	}
	s.Model.Labels = map[string]string{"location": Redact, "person": Alert}
	if s.labelAction(location) != Redact || s.labelAction(person) != Alert {
		t.Fatal("a label's own action")
	}
}

// The model's personal data needs the plan that masks it, alerts included
// (reading costs what the plan pays for); its secrets need none.
func TestModelNeeds(t *testing.T) {
	old := Defaults()
	s := old
	s.Model.Secrets = true
	if n := s.Needs(old); len(n) != 0 {
		t.Fatalf("secrets need %v", n)
	}
	s.Model.PII = true
	if n := s.Needs(old); !slices.Contains(n, catalog.PIIRedaction) {
		t.Fatalf("pii needs %v", n)
	}
	// Kept from before a downgrade, it needs nothing more.
	if n := s.Needs(s); len(n) != 0 {
		t.Fatalf("kept needs %v", n)
	}
	s2 := s
	s2.Model.Labels = map[string]string{"health": Tokenize}
	if n := s2.Needs(s); !slices.Contains(n, catalog.PIIRedaction) {
		t.Fatalf("masking a label needs %v", n)
	}
}

// Every use case gives settings that hold.
func TestUseCasesHold(t *testing.T) {
	env, _ := NewEnv()
	for _, u := range UseCases {
		s := Defaults()
		s.Secrets, s.PII, s.Sensitive, s.Model = u.Secrets, u.PII, u.Sensitive, u.Model
		if err := s.Check(env); err != nil {
			t.Errorf("%s: %v", u.Name, err)
		}
		if _, err := Compile(s, env); err != nil {
			t.Errorf("%s: %v", u.Name, err)
		}
	}
	for _, l := range ModelLabels {
		if l.Description == "" || l.Group == "" || (l.Class != ClassPII && l.Class != ClassSensitive && l.Class != ClassSecret) {
			t.Errorf("label %+v", l)
		}
	}
}
