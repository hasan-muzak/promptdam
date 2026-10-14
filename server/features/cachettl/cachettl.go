// Package cachettl decides, per prompt family, how long Anthropic's prompt
// cache keeps a call's prefix and its conversation's tail: five minutes
// (written at 1.25 times the input price) or an hour (at twice). An hour
// pays when the family's calls come back 5 to 60 minutes later often
// enough: each such call reads its cached tokens at a tenth of the price
// instead of writing them again. It costs on every other write: calls within
// five minutes (which the shorter lifetime serves as well) and calls after
// an hour or that start a conversation (which write either way).
//
// The sums are weighted by tokens: a long conversation's gaps count for its
// size. The decision moves only on clear evidence (Enter, Leave, MinGaps),
// so a family doesn't flip between lifetimes on noise; features/cachepolicy
// learns and ships it.
package cachettl

import (
	"math"

	"github.com/hasan-muzak/keel-core/pkg/llm"
)

// OneHour is the longer lifetime (promptcache's); "" is the default five
// minutes.
const OneHour = "1h"

// Hysteresis: an hour starts when it pays 15% more than it costs, and
// stops when it pays 15% less; at least MinGaps returns of 5 to 60 minutes
// must back it.
const (
	Enter   = 1.15
	Leave   = 0.85
	MinGaps = 20
)

// Gaps are a family's calls by how long after their previous one they came,
// in cached tokens: Mid came 5 to 60 minutes later (their cached tokens: what
// an hour would have kept); Inc within five minutes (the tokens each wrote:
// what an hour writes at twice the price instead of 1.25 times); Long after
// an hour or first (the tokens each wrote either way). MidCalls counts the
// mid returns.
type Gaps struct {
	Mid, Inc, Long int64 `json:",omitempty"`
	MidCalls       int64 `json:",omitempty"`
}

// Decision is what an hour does for a family's prefix or tail.
type Decision struct {
	// TTL is "1h" when the hour is kept, "" for the default five minutes.
	TTL string `json:"ttl,omitempty"`
	// Ratio is what an hour saves over what it costs (≥ 1: it pays).
	Ratio float64 `json:"ratio"`
	// NetMicros is what an hour saved (negative: would have cost) over the
	// gaps weighed, at the model's prices.
	NetMicros int64 `json:"net_micros"`
}

// Decide weighs an hour against five minutes for g at model m's prices,
// prev being the current lifetime ("1h" or ""). Unknown prices keep the
// default.
func Decide(g Gaps, m llm.Model, prev string) Decision {
	read := m.CachedInput
	write5 := m.CacheWrite
	write1h := m.CacheWrite1h
	if write1h <= 0 {
		write1h = 2 * m.Input
	}
	if read <= 0 || write5 <= 0 || write1h <= write5 {
		return Decision{}
	}
	gain := (write5 - read) * float64(g.Mid)
	cost := (write1h - write5) * float64(g.Inc+g.Long)
	d := Decision{NetMicros: int64(math.Round(gain - cost))}
	switch {
	case cost > 0:
		d.Ratio = gain / cost
	case gain > 0:
		d.Ratio = math.Inf(1)
	}
	keep := prev == OneHour && d.Ratio > Leave
	start := prev != OneHour && d.Ratio >= Enter
	if (keep || start) && g.MidCalls >= MinGaps {
		d.TTL = OneHour
	}
	if math.IsInf(d.Ratio, 1) {
		d.Ratio = math.MaxFloat64
	}
	return d
}

// Family is what was learned of a prompt family.
type Family struct {
	// Model is the family's most used model, whose prices weighed it.
	Model string `json:"model"`
	// Tail weighs a conversation's tail; Prefix the instructions and tools
	// other conversations share.
	Tail       Decision `json:"tail"`
	Prefix     Decision `json:"prefix"`
	TailGaps   Gaps     `json:"tail_gaps"`
	PrefixGaps Gaps     `json:"prefix_gaps"`
	// ColdParallel counts calls that wrote the family's prefix within 10
	// seconds of another and read none of it: sent together, they all paid
	// the write (a cache entry is readable once the first answer starts).
	// ColdParallelMicros is what that cost over reading it.
	ColdParallel       int64 `json:"cold_parallel,omitempty"`
	ColdParallelMicros int64 `json:"cold_parallel_micros,omitempty"`
}

// TTL is a family's lifetimes as gateways apply them ("" for five minutes).
type TTL struct {
	Prefix string `json:"prefix,omitempty"`
	Tail   string `json:"tail,omitempty"`
}

// TTLOf is a family's lifetimes: a one-hour tail keeps the prefix an hour
// too (longer lifetimes come first in a request).
func (f Family) TTLOf() TTL {
	t := TTL{Prefix: f.Prefix.TTL, Tail: f.Tail.TTL}
	if t.Tail == OneHour {
		t.Prefix = OneHour
	}
	return t
}
