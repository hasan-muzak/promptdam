package providers

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/hasan-muzak/promptdam/server/features/cloudauth"
)

// Cloud kinds: one account of a cloud (and its region), serving several
// model families, each through the cloud's own API for it
// (gateway.UpstreamURL): Claude through Bedrock's, Vertex's and Foundry's
// Anthropic APIs, GPT and Gemini through their OpenAI-compatible ones.
const (
	Bedrock = "bedrock"
	Vertex  = "vertex"
	Azure   = "azure"
)

// IsCloud reports a cloud kind.
func IsCloud(kind string) bool { return kind == Bedrock || kind == Vertex || kind == Azure }

// FormatFor is the API a provider calls a model in: its own, or, on a
// cloud account, Anthropic's for Claude and the OpenAI-compatible one for
// the others; on Bedrock, Anthropic's for all (Claude's API, or Converse
// as its dialect: gateway.Converse).
func FormatFor(kind, format, model string) string {
	if !IsCloud(kind) {
		return format
	}
	if kind == Bedrock {
		return FormatAnthropic // Claude's API, or Converse spoken as Messages
	}
	if strings.Contains(strings.ToLower(model), "claude") {
		return FormatAnthropic
	}
	return FormatOpenAI
}

// CloudModel is a model's id on a cloud account: Claude on Bedrock is
// "anthropic."-prefixed, Gemini on Vertex "google/"-prefixed (a client may
// name them either way); Azure's are the project's deployment names, as
// called.
func CloudModel(kind, model string) string {
	switch {
	case kind == Bedrock && strings.HasPrefix(model, "claude-"):
		return "anthropic." + model
	case kind == Vertex && strings.HasPrefix(model, "gemini-"):
		return "google/" + model
	}
	return model
}

// Cloud is where a cloud account's models are: Bedrock's region, Vertex's
// project and location (global, eu, us, or a region), Azure AI Foundry's
// resource.
type Cloud struct {
	Region, Project, Location, Resource string
}

var (
	regionRE   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	projectRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	locationRE = regexp.MustCompile(`^(global|eu|us|[a-z]+(-[a-z]+)*[0-9])$`)
	resourceRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,62}$`)
)

// CloudBaseURL is the root of a cloud account's model APIs.
func CloudBaseURL(kind string, c Cloud) (string, error) {
	switch kind {
	case Bedrock:
		if !regionRE.MatchString(c.Region) {
			return "", invalid("a Bedrock provider needs its AWS region, such as eu-west-1")
		}
		return "https://bedrock-mantle." + c.Region + ".api.aws", nil
	case Vertex:
		if !projectRE.MatchString(c.Project) {
			return "", invalid("a Vertex provider needs its Google Cloud project id")
		}
		if c.Location == "" {
			c.Location = "global"
		}
		if !locationRE.MatchString(c.Location) {
			return "", invalid("a Vertex location is global, eu, us or a region such as europe-west4")
		}
		path := "/v1/projects/" + c.Project + "/locations/" + c.Location
		switch c.Location {
		case "global":
			return "https://aiplatform.googleapis.com" + path, nil
		case "eu", "us":
			return "https://aiplatform." + c.Location + ".rep.googleapis.com" + path, nil
		}
		return "https://" + c.Location + "-aiplatform.googleapis.com" + path, nil
	case Azure:
		if !resourceRE.MatchString(c.Resource) {
			return "", invalid("an Azure provider needs its AI Foundry resource name")
		}
		return "https://" + strings.ToLower(c.Resource) + ".services.ai.azure.com", nil
	}
	return "", invalid("%q isn't a cloud", kind)
}

// CloudOf reads a cloud account back from its base URL (CloudBaseURL).
func CloudOf(kind, baseURL string) Cloud {
	u, err := url.Parse(baseURL)
	if err != nil {
		return Cloud{}
	}
	host := u.Hostname()
	switch kind {
	case Bedrock:
		if r, ok := strings.CutPrefix(host, "bedrock-mantle."); ok {
			return Cloud{Region: strings.TrimSuffix(r, ".api.aws")}
		}
	case Vertex:
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 5 && parts[1] == "projects" && parts[3] == "locations" {
			return Cloud{Project: parts[2], Location: parts[4]}
		}
	case Azure:
		if r, ok := strings.CutSuffix(host, ".services.ai.azure.com"); ok {
			return Cloud{Resource: r}
		}
	}
	return Cloud{}
}

// Credential is how a provider signs in. Most kinds take an API key; a
// cloud takes one of its own methods: Bedrock an API key or IAM access
// keys, Vertex a service-account key, Azure an API key.
type Credential struct {
	APIKey          string `json:"api_key,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
	// ServiceAccount is the key file's JSON.
	ServiceAccount string `json:"service_account,omitempty"`
}

// maxServiceAccount bounds a service-account key file (about 2.4 kB).
const maxServiceAccount = 16 << 10

// check validates a cloud kind's credential and returns its hint: the
// key's or access key's last characters, the service account's email.
func (c Credential) check(kind string) (hint string, err error) {
	for _, s := range []string{c.APIKey, c.AccessKeyID, c.SecretAccessKey, c.SessionToken} {
		if len(s) > 4096 {
			return "", invalid("a credential field is too long")
		}
	}
	keys := c.AccessKeyID != "" || c.SecretAccessKey != "" || c.SessionToken != ""
	switch kind {
	case Bedrock:
		switch {
		case c.ServiceAccount != "":
			return "", invalid("a Bedrock provider signs in with an API key or IAM access keys")
		case c.APIKey != "" && keys:
			return "", invalid("a Bedrock provider takes an API key or IAM access keys, not both")
		case c.APIKey != "":
			return lastChars(c.APIKey), nil
		case c.AccessKeyID == "" || c.SecretAccessKey == "":
			return "", invalid("IAM access keys need both the access key id and the secret")
		}
		return lastChars(c.AccessKeyID), nil
	case Vertex:
		if c.APIKey != "" || keys {
			return "", invalid("a Vertex provider signs in with a service-account key")
		}
		if len(c.ServiceAccount) > maxServiceAccount {
			return "", invalid("the service-account key is too long")
		}
		sa, err := cloudauth.ParseServiceAccount([]byte(c.ServiceAccount))
		if err != nil {
			return "", invalid("%s", err.Error())
		}
		return sa.ClientEmail, nil
	case Azure:
		if keys || c.ServiceAccount != "" || c.APIKey == "" {
			return "", invalid("an Azure provider signs in with its resource's API key")
		}
		return lastChars(c.APIKey), nil
	}
	return "", invalid("%q isn't a cloud", kind)
}

// sealable is the credential as it is sealed.
func (c Credential) sealable() []byte {
	b, _ := json.Marshal(c)
	return b
}

// lastChars is a key's hint: its last four characters, none for a short
// key.
func lastChars(k string) string {
	if len(k) < 12 {
		return ""
	}
	return "…" + k[len(k)-4:]
}
