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
	redact.INAadhaar:        "India: Aadhaar numbers (Verhoeff-checked)",
	redact.INPAN:            "India: PAN tax numbers",
	redact.BRCPF:            "Brazil: CPF numbers (checksum-checked)",
	redact.BRCNPJ:           "Brazil: CNPJ company numbers (checksum-checked)",
	redact.CNResidentID:     "China: resident identity card numbers (birth date and checksum)",
	redact.JPMyNumber:       "Japan: My Number (checksum-checked, near its name)",
	redact.KRRRN:            "South Korea: resident registration numbers (birth date)",
	redact.SGNRIC:           "Singapore: NRIC and FIN (checksum-checked)",
	redact.AUTFN:            "Australia: tax file numbers (checksum-checked, near their name)",
	redact.AUMedicare:       "Australia: Medicare card numbers (checksum-checked, near their name)",
	redact.UKNINO:           "UK: National Insurance numbers",
	redact.UKNHS:            "UK: NHS numbers (checksum-checked, near their name)",
	redact.DETaxID:          "Germany: tax identification numbers, Steuer-IdNr (checksum-checked)",
	redact.FRNIR:            "France: social security numbers, NIR (key-checked)",
	redact.ESDNI:            "Spain: DNI numbers (letter-checked)",
	redact.ESNIE:            "Spain: NIE numbers for foreigners (letter-checked)",
	redact.ITFiscalCode:     "Italy: codice fiscale (checksum-checked)",
	redact.NLBSN:            "Netherlands: BSN citizen numbers (11-proof, near their name)",
	redact.PLPESEL:          "Poland: PESEL numbers (birth date and checksum, near their name)",
	redact.SEPersonnr:       "Sweden: personnummer (birth date and Luhn)",
	redact.FIHETU:           "Finland: personal identity codes, HETU (checksum-checked)",
	redact.DKCPR:            "Denmark: CPR numbers (birth date, near their name)",
	redact.NOFNR:            "Norway: national identity numbers, fødselsnummer (two check digits)",
	redact.BENational:       "Belgium: national register numbers (key-checked)",
	redact.PTNIF:            "Portugal: NIF tax numbers (checksum-checked, near their name)",
	redact.MXCURP:           "Mexico: CURP (checksum-checked)",
	redact.MXRFC:            "Mexico: RFC tax numbers (birth date)",
	redact.CASIN:            "Canada: social insurance numbers, SIN (Luhn, near their name)",
	redact.ZAID:             "South Africa: identity numbers (birth date and Luhn)",
	redact.ILID:             "Israel: identity numbers, Teudat Zehut (checksum-checked, near their name)",
	redact.AEEmiratesID:     "United Arab Emirates: Emirates ID numbers (Luhn)",
	redact.USITIN:           "US: individual taxpayer identification numbers, ITIN",
	redact.PassportMRZ:      "Passports' machine-readable line, any country (check digits)",
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
		Description: "Personal data: email addresses, phone numbers, payment cards, IBANs, and national ID numbers of 33 countries and passports, each checked as its issuer checks it."},
}
