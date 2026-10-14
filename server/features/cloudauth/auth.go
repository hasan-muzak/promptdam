package cloudauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Authorizer signs a request to a cloud for one provider, as the last step
// before it is sent: its headers set, body the exact bytes sent. A request
// sent again (a heal) is signed again.
type Authorizer interface {
	Authorize(ctx context.Context, req *http.Request, body []byte) error
}

// BedrockKey signs in with a Bedrock API key: x-api-key on the Anthropic
// API (bedrock-mantle), a bearer token on the OpenAI-compatible one
// (bedrock-runtime).
func BedrockKey(key string) Authorizer { return bedrockKey(key) }

type bedrockKey string

func (k bedrockKey) Authorize(_ context.Context, req *http.Request, _ []byte) error {
	if strings.HasPrefix(req.URL.Hostname(), "bedrock-mantle.") {
		req.Header.Set("X-Api-Key", string(k))
	} else {
		req.Header.Set("Authorization", "Bearer "+string(k))
	}
	return nil
}

// SigV4 signs with IAM access keys (and a session token, for temporary
// ones), AWS's Signature Version 4: the service and region are the host's
// (bedrock-mantle.{region}.api.aws, bedrock-runtime.{region}.amazonaws.com),
// the body is hashed into the signature.
func SigV4(accessKeyID, secret, sessionToken string) Authorizer {
	return &sigV4{creds: aws.Credentials{AccessKeyID: accessKeyID, SecretAccessKey: secret, SessionToken: sessionToken},
		signer: v4.NewSigner(), now: time.Now}
}

type sigV4 struct {
	creds  aws.Credentials
	signer *v4.Signer
	now    func() time.Time
}

// ErrNotAWS is a request to a host whose AWS service and region aren't
// known: signing for a guessed one would only be refused.
var ErrNotAWS = errors.New("cloudauth: not an AWS model API host")

func (s *sigV4) Authorize(ctx context.Context, req *http.Request, body []byte) error {
	service, region, ok := awsHost(req.URL.Hostname())
	if !ok {
		return ErrNotAWS
	}
	sum := sha256.Sum256(body)
	return s.signer.SignHTTP(ctx, s.creds, req, hex.EncodeToString(sum[:]), service, region, s.now())
}

// awsHost is the signing service and region of an AWS model API host.
func awsHost(host string) (service, region string, ok bool) {
	labels := strings.Split(host, ".")
	if len(labels) < 3 {
		return "", "", false
	}
	switch {
	case labels[0] == "bedrock-mantle" && strings.HasSuffix(host, ".api.aws"):
		return "bedrock-mantle", labels[1], true
	case labels[0] == "bedrock-runtime" && strings.HasSuffix(host, ".amazonaws.com"):
		return "bedrock", labels[1], true
	}
	return "", "", false
}

// Header signs in with fixed headers (Azure's api-key).
func Header(h http.Header) Authorizer { return header(h) }

type header http.Header

func (h header) Authorize(_ context.Context, req *http.Request, _ []byte) error {
	for k, v := range h {
		req.Header[k] = v
	}
	return nil
}
