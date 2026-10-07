// Package families groups a project's calls into prompt families: the
// calls of one task template, whatever the values filled into it. A family
// is a hash of the request's normalised instructions, its tools' names and
// the answer's format (or a name the client gives it). It is the unit of
// spend breakdowns, the cache doctor and savings experiments.
//
// Normalising also says which kinds of changing values the instructions
// hold (dates, times, IDs, numbers): those that break providers' prompt
// caches when they sit in a stable prefix, which the cache doctor reports.
package families

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"

	"github.com/hasan-muzak/keel/pkg/llm/wire"
)

// Flags say which changing values a request's instructions hold, and how
// its tools are listed.
type Flags uint16

// Flag bits.
const (
	Dates Flags = 1 << iota
	Times
	UUIDs
	HexIDs
	Numbers
	// ToolsUnsorted marks tools not listed in name order: a list built in
	// changing order breaks prompt caches.
	ToolsUnsorted
)

// Names lists the flags set, for reports.
func (f Flags) Names() []string {
	var out []string
	for _, n := range []struct {
		f    Flags
		name string
	}{{Dates, "dates"}, {Times, "times"}, {UUIDs, "uuids"}, {HexIDs, "hex_ids"}, {Numbers, "numbers"}, {ToolsUnsorted, "tools_unsorted"}} {
		if f&n.f != 0 {
			out = append(out, n.name)
		}
	}
	return out
}

// Shape is a request's family and how its prefix looks.
type Shape struct {
	// Family is the family's ID: 16 hex characters, or "name:<name>" when
	// the client names it; "" for calls without instructions or tools.
	Family string
	// Prefix hashes the prefix exactly as sent (instructions and tools):
	// calls of a family with different prefixes miss providers' caches.
	Prefix string
	Flags  Flags
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

// Of returns the shape of a request. named is the family the client named
// (X-Promptdam-Family), used when valid.
func Of(p wire.Prefix, named string) Shape {
	var s Shape
	if p.System == "" && len(p.Tools) == 0 && p.Format == "" && named == "" {
		return s
	}
	norm, flags := Normalize(p.System)
	if !slices.IsSorted(p.Tools) {
		flags |= ToolsUnsorted
	}
	s.Flags = flags

	h := sha256.New()
	h.Write([]byte(p.System))
	h.Write([]byte{0})
	h.Write(p.ToolsRaw)
	s.Prefix = hex.EncodeToString(h.Sum(nil)[:8])

	if named = strings.TrimSpace(named); nameRE.MatchString(named) {
		s.Family = "name:" + named
		return s
	}
	tools := slices.Clone(p.Tools)
	slices.Sort(tools)
	h.Reset()
	h.Write([]byte(norm))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(tools, "\x1f")))
	h.Write([]byte{0})
	h.Write([]byte(p.Format))
	s.Family = hex.EncodeToString(h.Sum(nil)[:8])
	return s
}

// Normalize replaces the values that change from call to call (dates,
// times, UUIDs, long hex IDs, numbers) with placeholders and collapses
// whitespace, in one pass, and says which it found. Text between them is
// copied in runs, never decoded.
func Normalize(s string) (string, Flags) {
	var b strings.Builder
	b.Grow(len(s))
	var flags Flags
	lit := 0       // start of the literal run not yet written
	space := false // whitespace seen since the last thing written
	flush := func(to int) {
		if lit < to {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteString(s[lit:to])
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			flush(i)
			for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
				i++
			}
			lit, space = i, true
			continue
		}
		// Values start words, with a digit or (hex IDs, UUIDs) a hex letter.
		if isHex(c) && (i == 0 || !isAlnum(s[i-1])) {
			if n, f, ph := value(s[i:]); n > 0 {
				flush(i)
				if space && b.Len() > 0 {
					b.WriteByte(' ')
				}
				space = false
				b.WriteString(ph)
				flags |= f
				i += n
				lit = i
				continue
			}
		}
		i++
	}
	flush(len(s))
	return b.String(), flags
}

// value recognises a changing value at the start of s (which starts a
// word): its length, its kind and its placeholder; 0 when there is none.
func value(s string) (int, Flags, string) {
	if n := uuidAt(s); n > 0 {
		return n, UUIDs, "<uuid>"
	}
	if n := dateAt(s); n > 0 {
		// A time right after a date ("2026-10-07T12:00:00Z", "… 12:00")
		// is part of it.
		if m := dateTimeTail(s[n:]); m > 0 {
			return n + m, Dates | Times, "<datetime>"
		}
		return n, Dates, "<date>"
	}
	if n := timeAt(s); n > 0 {
		return n, Times, "<time>"
	}
	if n := hexIDAt(s); n > 0 {
		return n, HexIDs, "<id>"
	}
	if n := numberAt(s); n > 0 {
		return n, Numbers, "<n>"
	}
	return 0, 0, ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || (c|0x20) >= 'a' && (c|0x20) <= 'f' }

func isAlnum(c byte) bool { return isDigit(c) || (c|0x20) >= 'a' && (c|0x20) <= 'z' || c == '_' }

// ends reports that s ends a word at n.
func ends(s string, n int) bool { return n == len(s) || !isAlnum(s[n]) }

// digits counts the digits at the start of s, up to most.
func digits(s string, most int) int {
	n := 0
	for n < len(s) && n < most && isDigit(s[n]) {
		n++
	}
	return n
}

// uuidAt matches 8-4-4-4-12 hex digits.
func uuidAt(s string) int {
	if len(s) < 36 {
		return 0
	}
	for i := range 36 {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return 0
			}
		} else if !isHex(s[i]) {
			return 0
		}
	}
	if !ends(s, 36) {
		return 0
	}
	return 36
}

// dateAt matches YYYY-MM-DD, YYYY/MM/DD, DD/MM/YYYY, DD.MM.YYYY and
// DD-MM-YYYY.
func dateAt(s string) int {
	a := digits(s, 5)
	if a == 0 || a >= len(s) {
		return 0
	}
	sep := s[a]
	if sep != '-' && sep != '/' && sep != '.' {
		return 0
	}
	b := digits(s[a+1:], 3)
	if b == 0 || a+1+b >= len(s) || s[a+1+b] != sep {
		return 0
	}
	c := digits(s[a+2+b:], 5)
	n := a + 2 + b + c
	if c == 0 || !ends(s, n) && (s[n] != 'T' || n+1 >= len(s) || !isDigit(s[n+1])) {
		return 0
	}
	if (a == 4 && b <= 2 && c <= 2) || (a <= 2 && b <= 2 && c == 4) {
		return n
	}
	return 0
}

// dateTimeTail matches "T12:00", " 12:00:00", "T12:00:00.123Z",
// "T12:00:00+03:00" after a date.
func dateTimeTail(s string) int {
	if len(s) < 6 || (s[0] != 'T' && s[0] != ' ') {
		return 0
	}
	n := timeAt(s[1:])
	if n == 0 {
		return 0
	}
	n++
	if n < len(s) && s[n] == 'Z' {
		n++
	} else if n < len(s) && (s[n] == '+' || s[n] == '-') {
		if m := timeAt(s[n+1:]); m > 0 {
			n += 1 + m
		}
	}
	return n
}

// timeAt matches HH:MM, HH:MM:SS and HH:MM:SS.fff.
func timeAt(s string) int {
	h := digits(s, 3)
	if h == 0 || h > 2 || h >= len(s) || s[h] != ':' {
		return 0
	}
	m := digits(s[h+1:], 3)
	if m != 2 {
		return 0
	}
	n := h + 1 + m
	if n < len(s) && s[n] == ':' {
		if sec := digits(s[n+1:], 3); sec == 2 {
			n += 3
			if n < len(s) && s[n] == '.' {
				if f := digits(s[n+1:], 10); f > 0 {
					n += 1 + f
				}
			}
		}
	}
	if n < len(s) && isAlnum(s[n]) && s[n] != 'Z' {
		return 0
	}
	return n
}

// hexIDAt matches 16 or more hex characters mixing digits and letters
// (hashes, trace and request IDs).
func hexIDAt(s string) int {
	n, digit, letter := 0, false, false
	for n < len(s) && isHex(s[n]) {
		if isDigit(s[n]) {
			digit = true
		} else {
			letter = true
		}
		n++
	}
	if n >= 16 && digit && letter && ends(s, n) {
		return n
	}
	return 0
}

// numberAt matches 123, 1,234, 12.5 and -free numbers ending a word.
func numberAt(s string) int {
	n := digits(s, len(s))
	if n == 0 {
		return 0
	}
	for n < len(s)-1 && (s[n] == '.' || s[n] == ',') && isDigit(s[n+1]) {
		n++
		n += digits(s[n:], len(s))
	}
	if !ends(s, n) {
		return 0 // part of a word: "gpt4", "v2"
	}
	return n
}
