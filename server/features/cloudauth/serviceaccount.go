// Package cloudauth signs requests to the clouds' model APIs with a
// project's own credentials: AWS's SigV4 for IAM keys, Bedrock API keys,
// Google OAuth tokens minted from a service-account key, Azure keys. It
// uses no cloud SDK's credential discovery: a project's credentials are
// the only ones, never the server's own.
package cloudauth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
)

// GoogleTokenURL is where service-account keys are exchanged for access
// tokens: the only one accepted, so a key file can't send its signed
// assertion anywhere else.
const GoogleTokenURL = "https://oauth2.googleapis.com/token"

// ServiceAccount is a Google service-account key, as its JSON file holds it.
type ServiceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`

	key *rsa.PrivateKey
}

// ParseServiceAccount reads a service-account key file and its RSA key.
func ParseServiceAccount(raw []byte) (*ServiceAccount, error) {
	var sa ServiceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, errors.New("the service-account key isn't JSON")
	}
	switch {
	case sa.Type != "service_account":
		return nil, errors.New(`the key file's type isn't "service_account"`)
	case sa.ClientEmail == "" || sa.PrivateKey == "" || sa.PrivateKeyID == "":
		return nil, errors.New("the key file lacks client_email, private_key or private_key_id")
	case sa.TokenURI != "" && sa.TokenURI != GoogleTokenURL:
		return nil, fmt.Errorf("the key file's token_uri must be %s", GoogleTokenURL)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("the key file's private_key isn't PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return nil, errors.New("the key file's private_key can't be read")
		}
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the key file's private_key isn't an RSA key")
	}
	sa.key = key
	return &sa, nil
}
