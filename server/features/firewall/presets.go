package firewall

import "github.com/hasan-muzak/keel-core/pkg/redact"

// Preset is a class's detectors as the firewall offers them: "source
// code" is the secret class's, "customer data" the personal data's.
type Preset struct {
	Name        string     `json:"name"`
	Class       string     `json:"class"`
	Description string     `json:"description"`
	Detectors   []Detector `json:"detectors"`
}

// Detector is one kind of finding.
type Detector struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Default reports that the class looks for it unless the settings
	// choose its detectors.
	Default bool `json:"default"`
}

// described say what each built-in detector finds.
var described = map[string]string{
	redact.PrivateKey:       "PEM private keys (RSA, EC, OpenSSH…)",
	redact.AWSAccessKey:     "AWS access key IDs",
	redact.GCPAPIKey:        "Google Cloud API keys",
	redact.AzureKey:         "Azure storage account keys",
	redact.GitHubToken:      "GitHub tokens",
	redact.StripeKey:        "Stripe secret and restricted keys, webhook secrets",
	redact.SlackToken:       "Slack tokens",
	redact.SlackWebhook:     "Slack incoming webhook URLs",
	redact.OpenAIKey:        "OpenAI API keys",
	redact.AnthropicKey:     "Anthropic API keys",
	redact.JWT:              "JSON Web Tokens",
	redact.FixwireSecretKey: "Fixwire secret keys",
	redact.URLCredentials:   "Passwords in URLs and connection strings (postgres://user:password@host)",
	redact.HTTPAuth:         "Bearer and Basic credentials",
	redact.SecretAssigned:   "Values given to secrets' names, as .env files and config write them (DB_PASSWORD=…, api_key: …)",
	redact.Email:            "Email addresses",
	redact.Phone:            "Phone numbers in international format",
	redact.CreditCard:       "Payment card numbers (Luhn-checked)",
	redact.IBAN:             "IBANs (checksum-checked)",
	redact.USSSN:            "US Social Security numbers",
	redact.TRTCKN:           "Turkish national ID numbers (checksum-checked)",
	redact.IPv4:             "IPv4 addresses (only when chosen)",
}

func detectors(names []string, defaults bool) []Detector {
	out := make([]Detector, len(names))
	for i, n := range names {
		out[i] = Detector{Name: n, Description: described[n], Default: defaults}
	}
	return out
}

// Presets are the classes' detectors.
var Presets = []Preset{
	{Name: "source code", Class: ClassSecret, Detectors: detectors(SecretDetectors, true),
		Description: "What coding agents and pasted config leak: .env secrets, private keys, cloud credentials, connection strings, API tokens."},
	{Name: "customer data", Class: ClassPII, Detectors: append(detectors(PIIDetectors, true), detectors([]string{redact.IPv4}, false)...),
		Description: "Personal data: email addresses, phone numbers, payment cards, IBANs, national IDs."},
}
