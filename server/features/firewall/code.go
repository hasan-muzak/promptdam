package firewall

import (
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/redact"
)

// Coding agents (Claude Code, Codex, Cursor) send code, and code names
// secrets without holding them: Token: req.Token, key = os.Getenv("KEY").
// redact's secret_assignment reads those as a secret given to a name, so
// the firewall leaves them out; a literal given to the name still counts.
// Claude Code's own session id, in metadata.user_id, read the same way and
// blocked every call it made under the default firewall.

// find is the redactor's findings in s, less code reading a secret.
func (e *Engine) find(s string) []redact.Finding {
	fs := e.r.Find(s)
	out := fs[:0]
	for _, f := range fs {
		if f.Detector != redact.SecretAssigned || !expression(s[f.Start:f.End]) {
			out = append(out, f)
		}
	}
	return out
}

// opaque reports a field that holds an opaque id by its API's contract:
// Anthropic's metadata.user_id ("a uuid, hash value, or other opaque
// identifier"), where Claude Code puts its device and session ids.
func opaque(w *Walker) bool {
	return w.Key() == "user_id" && w.Top() == "metadata"
}

// expression reports a value that is code: a call or an index
// (os.Getenv(, cfg[) or a dotted name (req.Token, s.cfg.APIKey). A part no
// name would have, long or digits throughout, makes a dotted value a
// literal (pk.eyJ1Ijoi….Q9x7).
func expression(v string) bool {
	if strings.ContainsAny(v, "()[]") {
		return true
	}
	parts := 0
	for part := range strings.SplitSeq(v, ".") {
		if !identifier(part) {
			return false
		}
		parts++
	}
	return parts > 1
}

// identifier reports a name in code: a letter or underscore, then letters,
// digits and underscores, at most 32 of them and fewer than 4 digits.
func identifier(s string) bool {
	digits := 0
	for i, c := range s {
		letter := c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
		if !letter {
			digits++
		}
	}
	return s != "" && len(s) <= 32 && digits < 4
}
