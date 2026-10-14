package translate

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/hasan-muzak/keel-core/pkg/llm/wire"
)

// Efforts are the reasoning efforts a router's tier may set, weakest
// first.
var Efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ValidEffort reports an effort a tier may set ("" sets none).
func ValidEffort(e string) bool { return e == "" || slices.Contains(Efforts, e) }

func rank(e string) int { return slices.Index(Efforts, e) }

// openAIEfforts are what OpenAI-compatible providers take when the price
// list doesn't say a model's: the levels every reasoning model there takes.
var openAIEfforts = []string{"low", "medium", "high"}

// FitEffort is the effort a model takes for want: want itself, else the
// nearest it takes, a tie going to the stronger (a weaker answer costs
// quality, a stronger only money). efforts are the model's, weakest first;
// when the price list doesn't know them (known false), those every model
// of the dialect takes. "" when the model takes none.
func FitEffort(want string, efforts []string, known bool, f wire.Format) string {
	if want == "" {
		return ""
	}
	if !known {
		if f.Anthropic() {
			efforts = Efforts[2:] // low … max
		} else {
			efforts = openAIEfforts
		}
	}
	if len(efforts) == 0 {
		return ""
	}
	if slices.Contains(efforts, want) {
		return want
	}
	best, bestDist, w := "", len(Efforts), rank(want)
	for _, e := range efforts {
		r := rank(e)
		if r < 0 {
			continue
		}
		d := r - w
		if d < 0 {
			d = -d
		}
		if d < bestDist || d == bestDist && r > w {
			best, bestDist = e, d
		}
	}
	return best
}

// Dialects: where a provider kind's API takes a reasoning effort.
const (
	dialectNone       = iota
	dialectAnthropic  // output_config.effort
	dialectChat       // reasoning_effort
	dialectOpenRouter // reasoning.effort
)

// chatEffortKinds are the OpenAI-format providers known to take
// reasoning_effort.
var chatEffortKinds = map[string]bool{"openai": true, "gemini": true, "xai": true, "groq": true}

func dialect(f wire.Format, kind string) int {
	switch {
	case f.Anthropic():
		return dialectAnthropic
	case kind == "openrouter":
		return dialectOpenRouter
	case chatEffortKinds[kind]:
		return dialectChat
	}
	return dialectNone
}

// HasEffort reports a request that sets a reasoning effort itself.
func HasEffort(body []byte, f wire.Format, kind string) bool {
	switch dialect(f, kind) {
	case dialectAnthropic:
		return member(body, "output_config", "effort")
	case dialectOpenRouter:
		return member(body, "reasoning", "effort")
	}
	raw, ok, err := wire.Field(body, "reasoning_effort")
	return err == nil && ok && !null(raw)
}

// SetEffort sets a request's reasoning effort for a target of format f and
// provider kind: Anthropic's output_config.effort (its other settings
// kept), OpenAI's reasoning_effort (most OpenAI-compatible providers take
// it), OpenRouter's reasoning.effort. ok is false for a provider whose
// dialect isn't known: the request goes as it is.
func SetEffort(body []byte, f wire.Format, kind, effort string) (out []byte, ok bool, err error) {
	value, _ := json.Marshal(effort)
	switch dialect(f, kind) {
	case dialectAnthropic:
		out, err = setMember(body, "output_config", "effort", value)
	case dialectOpenRouter:
		out, err = setMember(body, "reasoning", "effort", value)
	case dialectChat:
		out, err = wire.SetField(body, "reasoning_effort", value)
	default:
		return body, false, nil
	}
	if err != nil {
		return body, false, err
	}
	return out, true, nil
}

// StripEffort removes an OpenAI-format request's reasoning_effort, for a
// model known not to reason (which may refuse it). ok reports a removal.
func StripEffort(body []byte) (out []byte, ok bool) {
	out, gone := strip(body, "reasoning_effort")
	return out, len(gone) > 0
}

// Sampling are the parameters a model may refuse other than at their
// defaults (llm.Model.NoSampling), in either format.
var Sampling = []string{"temperature", "top_p", "top_k"}

// StripSampling removes a request's sampling parameters, for a call moved
// to a model that refuses them. gone names those removed that were set.
func StripSampling(body []byte) (out []byte, gone []string) { return strip(body, Sampling...) }

// strip removes the request's top-level fields; gone names those that were
// set (a null one is removed unnamed).
func strip(body []byte, fields ...string) (out []byte, gone []string) {
	seen := false
	for _, f := range fields {
		raw, found, err := wire.Field(body, f)
		if err != nil {
			return body, nil
		}
		if found {
			seen = true
			if !null(raw) {
				gone = append(gone, f)
			}
		}
	}
	if !seen {
		return body, nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body, nil
	}
	for _, f := range fields {
		delete(top, f)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(top) != nil {
		return body, nil
	}
	return bytes.TrimRight(b.Bytes(), "\n"), gone
}

// member reports a non-null member key of the object at field.
func member(body []byte, field, key string) bool {
	raw, ok, err := wire.Field(body, field)
	if err != nil || !ok || null(raw) {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	v, ok := obj[key]
	return ok && !null(v)
}

// setMember sets key in the object at field, its other members kept; an
// absent (or null) field becomes an object of that one member.
func setMember(body []byte, field, key string, value []byte) ([]byte, error) {
	obj := map[string]json.RawMessage{}
	if raw, ok, err := wire.Field(body, field); err != nil {
		return nil, err
	} else if ok && !null(raw) {
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
	}
	obj[key] = value
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return wire.SetField(body, field, bytes.TrimRight(b.Bytes(), "\n"))
}
