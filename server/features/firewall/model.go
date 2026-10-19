package firewall

import (
	"slices"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/redact"
)

// The detector model (round PD) reads the text for what no pattern has a
// shape for: names, addresses, health, beliefs, secrets in formats nobody
// listed. It reports every label it finds; the project decides per label
// what happens, so a setting changes without a model trained again.

// ClassSensitive is the special categories' class (GDPR Articles 9 and 10):
// health, beliefs, orientation, union membership, criminal records.
const ClassSensitive = "sensitive"

// Groups of labels, as the dashboard's accordion shows them.
const (
	GroupIdentity = "identity"
	GroupContact  = "contact"
	GroupLocation = "location"
	GroupIDs      = "ids"
	GroupSpecial  = "special"
	GroupSecrets  = "secrets"
)

// Label is one kind of finding the model reports, under the name the
// firewall gives it (events, settings, tokens).
type Label struct {
	Name        string `json:"name"`
	Class       string `json:"class"`
	Group       string `json:"group"`
	Description string `json:"description"`
	// Off by default even when its class acts: dates and places an answer
	// usually needs.
	Off bool `json:"off,omitempty"`
}

// ModelLabels are the model's labels. Those a pattern finds too (email,
// phone, ipv4) keep the pattern's name: a finding is one finding.
var ModelLabels = []Label{
	{"person", ClassPII, GroupIdentity, "Names of private people, in any form: Ayşe Yılmaz, Dr. Weber, Jo", false},
	{"username", ClassPII, GroupIdentity, "Account handles and logins: @ayse_y, ayilmaz", false},
	{"date_of_birth", ClassPII, GroupIdentity, "Birth dates, in any format", false},
	{"personal_date", ClassPII, GroupIdentity, "Other dates of a person's life: hire, admission, an arrest", true},
	{redact.Email, ClassPII, GroupContact, "Email addresses", false},
	{redact.Phone, ClassPII, GroupContact, "Phone numbers, in national formats too", false},
	{"url_personal", ClassPII, GroupContact, "Links that identify a person: a profile, a personal site", false},
	{"address", ClassPII, GroupLocation, "Street and postal addresses", false},
	{"location", ClassPII, GroupLocation, "Cities and regions where a person lives, works or is", true},
	{"government_id", ClassPII, GroupIDs, "ID, passport, licence and tax numbers no pattern knows", false},
	{"account_number", ClassPII, GroupIDs, "Bank, card, customer, member and policy numbers no pattern knows", false},
	{redact.IPv4, ClassPII, GroupIDs, "IP addresses", true},
	{"health", ClassSensitive, GroupSpecial, "Health: conditions, medicines, allergies, disabilities, sick leave", false},
	{"religion", ClassSensitive, GroupSpecial, "Religious and philosophical beliefs", false},
	{"sexual_orientation", ClassSensitive, GroupSpecial, "Sexual orientation, sex life, gender identity", false},
	{"political_opinion", ClassSensitive, GroupSpecial, "Political views, party membership", false},
	{"trade_union", ClassSensitive, GroupSpecial, "Trade union membership", false},
	{"criminal_record", ClassSensitive, GroupSpecial, "Convictions, offences, arrests, charges", false},
	{"ethnicity", ClassSensitive, GroupSpecial, "Racial or ethnic origin", false},
	{"biometric_genetic", ClassSensitive, GroupSpecial, "Fingerprints, face data, DNA, genetic results", false},
	{"credential", ClassSecret, GroupSecrets, "Keys, tokens and passwords in formats no pattern knows", false},
}

// modelLabel is a model label by name.
func modelLabel(name string) (Label, bool) {
	i := slices.IndexFunc(ModelLabels, func(l Label) bool { return l.Name == name })
	if i < 0 {
		return Label{}, false
	}
	return ModelLabels[i], true
}

// Model fail modes: what a call gets when the model can't answer in time.
const (
	// ModelFailOpen goes on with the patterns alone (the default): the
	// patterns read everything and are exact on secrets.
	ModelFailOpen   = "open"
	ModelFailClosed = "closed"
)

// Limits of the model's settings.
const (
	MaxAllow    = 200
	MaxAllowLen = 100
)

// ModelSettings are what the detector model does for a project.
type ModelSettings struct {
	// Secrets finds keys, tokens and passwords in formats no pattern knows
	// (the credential label), with the secrets' action: every plan.
	Secrets bool `json:"secrets"`
	// PII finds personal data and special categories by reading the text:
	// a plan with PII redaction.
	PII bool `json:"pii"`
	// Labels are actions for single labels, over their class's (person:
	// tokenize, location: redact); a label not listed takes its class's, or
	// off when it is off by default.
	Labels map[string]string `json:"labels,omitempty"`
	// Allow are terms never masked: the company's own name, its products, a
	// support agent's name. Compared whole, in any case.
	Allow    []string `json:"allow,omitempty"`
	FailMode string   `json:"fail_mode,omitempty"`
}

// labelAction is what a model label's findings get in s: its own action,
// else its class's (off for labels off by default), off when the model
// doesn't read for its kind.
func (s Settings) labelAction(l Label) string {
	if l.Class == ClassSecret && !s.Model.Secrets || l.Class != ClassSecret && !s.Model.PII {
		return Off
	}
	if a, ok := s.Model.Labels[l.Name]; ok {
		return a
	}
	if l.Off {
		return Off
	}
	switch l.Class {
	case ClassSecret:
		return s.Secrets
	case ClassSensitive:
		return s.Sensitive
	}
	return s.PII
}

// checkModel validates and tidies the model's settings.
func (s *Settings) checkModel() error {
	m := &s.Model
	if m.FailMode == "" {
		m.FailMode = ModelFailOpen
	}
	if m.FailMode != ModelFailOpen && m.FailMode != ModelFailClosed {
		return invalid("model.fail_mode is open or closed")
	}
	actions := []string{Off, Alert, Redact, Tokenize, Block}
	for name, a := range m.Labels {
		l, ok := modelLabel(name)
		switch {
		case !ok:
			return invalid("model.labels: %q is not one of the model's labels", name)
		case !slices.Contains(actions, a):
			return invalid("model.labels.%s: an action is off, alert, redact, tokenize or block", name)
		case a == Tokenize && l.Class == ClassSecret:
			return invalid("model.labels.%s: secrets can't be tokenized", name)
		}
	}
	if len(m.Allow) > MaxAllow {
		return invalid("model.allow: at most %d terms", MaxAllow)
	}
	seen := map[string]bool{}
	allow := m.Allow[:0]
	for _, t := range m.Allow {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > MaxAllowLen {
			return invalid("model.allow: a term is 1 to %d characters", MaxAllowLen)
		}
		if k := strings.ToLower(t); !seen[k] {
			seen[k] = true
			allow = append(allow, t)
		}
	}
	m.Allow = allow
	return nil
}

// UseCase is a starting point for a project's firewall: what it sets, the
// rest of the settings kept.
type UseCase struct {
	Name        string        `json:"name"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Secrets     string        `json:"secrets"`
	PII         string        `json:"pii"`
	Sensitive   string        `json:"sensitive"`
	Model       ModelSettings `json:"model"`
}

// UseCases are the presets the dashboard offers, most common first.
var UseCases = []UseCase{
	{Name: "customer_support", Title: "Customer support",
		Description: "Names, contact details, addresses, IDs and accounts masked, given back in answers; special categories alerted; dates, places, companies and amounts kept, as the assistant needs them.",
		Secrets:     Block, PII: Tokenize, Sensitive: Alert, Model: ModelSettings{Secrets: true, PII: true}},
	{Name: "healthcare", Title: "Healthcare",
		Description: "As customer support, and health data masked too.",
		Secrets:     Block, PII: Tokenize, Sensitive: Alert,
		Model: ModelSettings{Secrets: true, PII: true, Labels: map[string]string{"health": Tokenize}}},
	{Name: "hr_finance", Title: "HR and finance",
		Description: "As customer support, and dates of people's lives masked.",
		Secrets:     Block, PII: Tokenize, Sensitive: Alert,
		Model: ModelSettings{Secrets: true, PII: true, Labels: map[string]string{"personal_date": Tokenize}}},
	{Name: "coding_agents", Title: "Coding agents",
		Description: "Secrets redacted, not blocked (a block stops an agent's whole session); personal data in code and logs redacted.",
		Secrets:     Redact, PII: Redact, Sensitive: Off, Model: ModelSettings{Secrets: true, PII: true}},
	{Name: "strict", Title: "Strict",
		Description: "Every label masked, places and dates included: answers lose details they may need.",
		Secrets:     Block, PII: Tokenize, Sensitive: Tokenize,
		Model: ModelSettings{Secrets: true, PII: true, Labels: map[string]string{
			"location": Tokenize, "personal_date": Tokenize, redact.IPv4: Redact}}},
}
