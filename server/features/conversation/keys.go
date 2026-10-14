// Package conversation follows a project's conversations through the
// gateway a turn at a time. A turn is a human message and every call that
// continues it: an agent's tool calls and their results. The calls of one
// turn share a key that any gateway replica reads off the request alone
// (core wire.Turn's anchors), so what the gateway decides for a turn (a
// router's tier) holds for all of it, and what the turn's calls cost and
// cached is known when the next turn starts.
//
// Keys never cross a project, an API key, an end user, a route, a router or
// a family (the call's normalised instructions and tools): conversations
// that say the same thing elsewhere are someone else's.
package conversation

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/brand"
)

// Key identifies a turn or a conversation.
type Key [16]byte

// IsZero reports the zero key: no turn, or none known.
func (k Key) IsZero() bool { return k == Key{} }

// String is the key in hex.
func (k Key) String() string { return hex.EncodeToString(k[:]) }

// Scope is where a conversation belongs.
type Scope struct {
	Project, APIKey             uuid.UUID
	User, Route, Router, Family string
}

// Keys are a call's place in its conversation.
type Keys struct {
	// Turn is the call's turn; Prev the turn before it, zero for a
	// conversation's first.
	Turn, Prev Key
	// Conversation is the whole conversation: the client's session when
	// it names one, else its first message.
	Conversation Key
	// Continuation reports a call past its turn's human message (a tool
	// loop's), not one that starts the turn.
	Continuation bool
	// HumanTurns counts the conversation's human messages so far.
	HumanTurns int
}

// SessionHeaders name a conversation when a client sends one: the
// gateway's own, then agents' (Claude Code's).
var SessionHeaders = []string{brand.HeaderSession, "X-Claude-Code-Session-Id"}

// maxSession bounds a session name.
const maxSession = 200

// Session is the conversation a client names, "" when it names none.
func Session(h http.Header) string {
	for _, name := range SessionHeaders {
		if v := strings.TrimSpace(h.Get(name)); v != "" && len(v) <= maxSession {
			return v
		}
	}
	return ""
}

// KeysOf are the keys of a call with turn t; ok is false for a call without
// a human message, which has no turn.
func KeysOf(s Scope, session string, t wire.Turn) (Keys, bool) {
	if t.Human < 0 {
		return Keys{}, false
	}
	scope := s.sum()
	k := Keys{Turn: turnKey(scope, t.Before, t.Ask), Continuation: t.Continuation, HumanTurns: t.Humans}
	if t.PrevAsk != (wire.Anchor{}) {
		k.Prev = turnKey(scope, t.PrevBefore, t.PrevAsk)
	}
	if session != "" {
		k.Conversation = derive(scope, "session", []byte(session))
	} else {
		k.Conversation = derive(scope, "first", t.First[:])
	}
	return k, true
}

// turnKey is the key of the turn a human message (ask) starts after
// another message (before): a turn is known by where its question was
// asked, so the same question later in a conversation is another turn.
func turnKey(scope [32]byte, before, ask wire.Anchor) Key {
	return derive(scope, "turn", append(before[:], ask[:]...))
}

func derive(scope [32]byte, kind string, data []byte) Key {
	h := sha256.New()
	_, _ = h.Write(scope[:])
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write(data)
	var k Key
	copy(k[:], h.Sum(nil))
	return k
}

// sum hashes the scope's fields, each length-prefixed.
func (s Scope) sum() [32]byte {
	b := make([]byte, 0, 32+len(s.User)+len(s.Route)+len(s.Router)+len(s.Family)+32)
	b = append(b, s.Project[:]...)
	b = append(b, s.APIKey[:]...)
	for _, f := range []string{s.User, s.Route, s.Router, s.Family} {
		b = binary.LittleEndian.AppendUint32(b, uint32(len(f)))
		b = append(b, f...)
	}
	return sha256.Sum256(b)
}
