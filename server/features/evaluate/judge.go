package evaluate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Verdicts, from the candidate tier's side: its answer was better than the
// default tier's, as good, or worse.
const (
	Win  = "win"
	Tie  = "tie"
	Loss = "loss"
)

// Checks: how a verdict was reached.
const (
	// CheckSame: the answers say the same (text, or the same tool calls).
	CheckSame = "same"
	// CheckRefusal: the candidate refused what the default tier answered.
	CheckRefusal = "refusal"
	// CheckEmpty: the candidate answered nothing.
	CheckEmpty = "empty"
	// CheckJSON: JSON was asked for and the candidate's isn't.
	CheckJSON = "json"
	// CheckTools: the candidate called other tools, or none.
	CheckTools = "tools"
	// CheckError: the candidate's model failed on the call.
	CheckError = "error"
	// CheckJudge: the judge model decided.
	CheckJudge = "judge"
	// CheckNoJudge: only a judge could decide, and the router has none
	// (no verdict).
	CheckNoJudge = "no_judge"
)

// check compares a candidate's answer with the default tier's without a
// judge; ok is false when only a judge can decide.
func check(base, cand Answer, wantJSON bool) (verdict, how string, ok bool) {
	switch {
	case base.Refused && cand.Refused:
		return Tie, CheckSame, true
	case cand.Refused:
		return Loss, CheckRefusal, true
	case cand.Empty() && base.Empty():
		return Tie, CheckSame, true
	case cand.Empty():
		return Loss, CheckEmpty, true
	case wantJSON && !validJSON(cand.Text) && validJSON(base.Text) && len(base.Tools) == 0:
		return Loss, CheckJSON, true
	}
	if len(base.Tools) > 0 {
		switch {
		case !sameNames(base.Tools, cand.Tools):
			return Loss, CheckTools, true
		case sameArgs(base.Tools, cand.Tools):
			return Tie, CheckSame, true
		}
		return "", "", false
	}
	if len(cand.Tools) == 0 && normalize(base.Text) == normalize(cand.Text) {
		return Tie, CheckSame, true
	}
	return "", "", false
}

// validJSON reports JSON, fenced in a Markdown code block or not.
func validJSON(s string) bool {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		rest = strings.TrimPrefix(rest, "json")
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
	}
	return s != "" && json.Valid([]byte(s))
}

// sameNames reports the same tools called, as many times each, in any
// order.
func sameNames(a, b []ToolCall) bool {
	if len(a) != len(b) {
		return false
	}
	names := func(ts []ToolCall) []string {
		out := make([]string, len(ts))
		for i, t := range ts {
			out[i] = t.Name
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(names(a), names(b))
}

// sameArgs reports the same calls with the same arguments, as JSON values,
// in order.
func sameArgs(a, b []ToolCall) bool {
	for i := range a {
		if a[i].Name != b[i].Name || !sameJSON(a[i].Args, b[i].Args) {
			return false
		}
	}
	return true
}

func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b)) {
		return true
	}
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

var spaces = regexp.MustCompile(`\s+`)

func normalize(s string) string { return spaces.ReplaceAllString(strings.TrimSpace(s), " ") }

// What the judge reads of a call and its answers.
const (
	maxInstructions = 4_000
	maxRequest      = 8_000
	maxAnswer       = 8_000
)

// judgeSystem is the rubric.
const judgeSystem = `You compare two AI assistants' answers to the same request.
Judge which answer serves the request better: correctness first, then completeness, then following the request's instructions and format.
Length, tone and style count only when the request asks for them. Calling a tool counts as the answer when the request offers tools.
If both answers are equally good, or equally bad, the verdict is a tie.
Answer with a JSON object only: {"better": "a"} when answer A is better, {"better": "b"} when answer B is, {"better": "tie"} otherwise.`

// verdictSchema holds the judge's answer to {"better": "a" | "b" | "tie"}.
var verdictSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"better": map[string]any{"type": "string", "enum": []string{"a", "b", "tie"}}},
	"required":             []string{"better"},
	"additionalProperties": false,
}

// judgePrompt shows the judge the call (its instructions and the request's
// end) and two answers.
func judgePrompt(instructions, request string, a, b Answer) string {
	var s strings.Builder
	s.WriteString("<request>\n")
	if instructions != "" {
		s.WriteString("<instructions>\n" + clip(instructions, maxInstructions) + "\n</instructions>\n")
	}
	s.WriteString("<last_message>\n" + clip(request, maxRequest) + "\n</last_message>\n</request>\n\n")
	s.WriteString("<answer_a>\n" + render(a) + "\n</answer_a>\n\n<answer_b>\n" + render(b) + "\n</answer_b>\n\n")
	s.WriteString(`Which answer is better? Answer {"better": "a"}, {"better": "b"} or {"better": "tie"}.`)
	return s.String()
}

// render writes an answer for the judge: its text and the tools it called.
func render(a Answer) string {
	var s strings.Builder
	s.WriteString(a.Text)
	for _, t := range a.Tools {
		s.WriteString("\n[calls tool " + t.Name + " with " + string(t.Args) + "]")
	}
	if a.Refused {
		s.WriteString("\n[the assistant refused]")
	}
	return clip(s.String(), maxAnswer)
}

// clip cuts s to at most n bytes, on a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n[…cut]"
}

var betterRE = regexp.MustCompile(`"better"\s*:\s*"(a|b|tie)"`)

// readVerdict reads the judge's answer: "a", "b" or "tie" ("" when it
// says none).
func readVerdict(text string) string {
	var v struct {
		Better string `json:"better"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(text)), &v) == nil {
		switch v.Better {
		case "a", "b", "tie":
			return v.Better
		}
	}
	if m := betterRE.FindStringSubmatch(strings.ToLower(text)); m != nil {
		return m[1]
	}
	return ""
}
