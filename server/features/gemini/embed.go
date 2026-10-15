package gemini

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
)

// EmbedRequest turns embedContent (or batchEmbedContents, when batch) for
// model into an OpenAI embeddings request. A content's text parts are
// embedded together, as Gemini does; the task type and title are left out
// and listed.
func EmbedRequest(body []byte, model string, batch bool) (out []byte, dropped []string, err error) {
	top, ok := readObj(body)
	if !ok || top == nil {
		return nil, nil, refuse("the request body isn't a JSON object")
	}
	reqs := []obj{top}
	if batch {
		if reqs, ok = top.list("requests"); !ok || len(reqs) == 0 {
			return nil, nil, refuse("requests isn't a list of embedContent requests")
		}
	}
	inputs := make([]string, 0, len(reqs))
	var dims *int64
	for i, r := range reqs {
		c, ok := readObj(r.get("content"))
		if !ok || c == nil {
			return nil, nil, refuse("a request has no content")
		}
		parts, ok := c.list("parts")
		if !ok {
			return nil, nil, refuse("a content's parts aren't a list")
		}
		var b strings.Builder
		for _, p := range parts {
			switch {
			case p.has("text"):
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.str("text"))
			case !empty(p):
				return nil, nil, refuse("embeddings of anything but text")
			}
		}
		inputs = append(inputs, b.String())
		d := whole(r.get("outputDimensionality"))
		switch {
		case i == 0:
			dims = d
		case (d == nil) != (dims == nil) || d != nil && *d != *dims:
			return nil, nil, refuse("requests asking for different outputDimensionality")
		}
		for _, k := range []string{"taskType", "title"} {
			if r.has(k) {
				dropped = append(dropped, k)
			}
		}
	}
	req := map[string]any{"model": model, "input": inputs, "encoding_format": "float"}
	if !batch {
		req["input"] = inputs[0]
	}
	if dims != nil {
		req["dimensions"] = *dims
	}
	if out, err = marshal(req); err != nil {
		return nil, nil, err
	}
	slices.Sort(dropped)
	return out, slices.Compact(dropped), nil
}

type embedding struct {
	Index     int             `json:"index"`
	Embedding json.RawMessage `json:"embedding"`
}

// EmbedResponse turns an OpenAI embeddings answer into embedContent's (or
// batchEmbedContents', when batch).
func EmbedResponse(body []byte, batch bool) ([]byte, error) {
	var in struct {
		Data []embedding `json:"data"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	slices.SortStableFunc(in.Data, func(a, b embedding) int { return cmp.Compare(a.Index, b.Index) })
	type values struct {
		Values json.RawMessage `json:"values"`
	}
	if !batch {
		v := values{Values: json.RawMessage(`[]`)}
		if len(in.Data) > 0 {
			v.Values = in.Data[0].Embedding
		}
		return marshal(map[string]values{"embedding": v})
	}
	list := make([]values, len(in.Data))
	for i, d := range in.Data {
		list[i] = values{Values: d.Embedding}
	}
	return marshal(map[string][]values{"embeddings": list})
}

// gModel is a model in Gemini's list.
type gModel struct {
	Name                       string   `json:"name"`
	BaseModelID                string   `json:"baseModelId"`
	Version                    string   `json:"version"`
	DisplayName                string   `json:"displayName"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

func modelOf(id string) gModel {
	return gModel{Name: "models/" + id, BaseModelID: id, Version: "001", DisplayName: id,
		SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent", "countTokens", "embedContent"}}
}

// Models is the model list, in Gemini's shape.
func Models(ids []string) []byte {
	list := make([]gModel, len(ids))
	for i, id := range ids {
		list[i] = modelOf(id)
	}
	out, _ := marshal(map[string][]gModel{"models": list})
	return out
}

// Model is one model, in Gemini's shape.
func Model(id string) []byte {
	out, _ := marshal(modelOf(id))
	return out
}

// CountRequest is countTokens' request as generateContent's: the
// generateContentRequest it wraps, or its own contents.
func CountRequest(body []byte) ([]byte, error) {
	top, ok := readObj(body)
	if !ok || top == nil {
		return nil, refuse("the request body isn't a JSON object")
	}
	if gen := top.get("generateContentRequest"); !null(gen) {
		return gen, nil
	}
	return body, nil
}

// CountResponse is countTokens' answer for a count of the input tokens.
func CountResponse(total int64) []byte {
	out, _ := marshal(map[string]int64{"totalTokens": total})
	return out
}
