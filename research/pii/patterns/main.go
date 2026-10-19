// Command patterns runs the firewall's current detectors (core redact, every
// default one and IPv4) over the bake-off's documents: JSON lines of {id, text}
// in, JSON lines of {id, spans} out, offsets in code points as Python counts
// them.
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"unicode/utf8"

	"github.com/hasan-muzak/keel-core/pkg/redact"
)

type doc struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type out struct {
	ID    string   `json:"id"`
	Spans [][3]any `json:"spans"`
}

func main() {
	r, err := redact.New(redact.Options{Detectors: append(append([]string{}, redact.DefaultDetectors...), redact.IPv4)})
	if err != nil {
		log.Fatal(err)
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 64<<20)
	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()
	enc := json.NewEncoder(w)
	for in.Scan() {
		var d doc
		if err := json.Unmarshal(in.Bytes(), &d); err != nil {
			log.Fatal(err)
		}
		o := out{ID: d.ID, Spans: [][3]any{}}
		for _, f := range r.Find(d.Text) {
			s := utf8.RuneCountInString(d.Text[:f.Start])
			o.Spans = append(o.Spans, [3]any{s, s + utf8.RuneCountInString(d.Text[f.Start:f.End]), f.Detector})
		}
		if err := enc.Encode(o); err != nil {
			log.Fatal(err)
		}
	}
	if err := in.Err(); err != nil {
		log.Fatal(err)
	}
}
