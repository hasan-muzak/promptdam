package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

// A model directory holds:
//
//	model.onnx      the token classifier (inputs input_ids and
//	                attention_mask, int64 [batch, tokens]; output logits,
//	                float32 [batch, tokens, labels])
//	tokenizer.json  its tokenizer
//	config.json     its id2label, and its bos, eos and pad token ids
//	labels.json     {"labels": {model label: firewall label, …}, "min_score":
//	                0.3}: a model label not mapped is never reported
type labelsFile struct {
	Labels   map[string]string `json:"labels"`
	MinScore float64           `json:"min_score"`
}

type configFile struct {
	ID2Label map[string]string `json:"id2label"`
	BOS      *int64            `json:"bos_token_id"`
	EOS      *int64            `json:"eos_token_id"`
	Pad      *int64            `json:"pad_token_id"`
}

// Model is a loaded detector model. Its session and tokenizer are safe for
// concurrent use (ONNX Runtime runs a session from many goroutines).
type Model struct {
	// Version identifies the weights and the label mapping: spans of one
	// version are cached apart from another's.
	Version string
	tok     *tokenizers.Tokenizer
	session *ort.DynamicAdvancedSession
	// labels are the model's (B-PERSON_NAME…), by output index; kinds the
	// category each names ("" for O); begins, insides, ends and singles
	// mark the BIO or BIOES tags.
	labels []string
	kinds  []string
	tags   []byte // 'O', 'B', 'I', 'E', 'S'
	// start, follows and end are the scheme's transitions (rules).
	start, end []bool
	follows    [][]bool
	firewall   map[string]string
	minScore   float64
	bos, eos   int64
	pad        int64
}

// Options load a model.
type Options struct {
	// Threads are ONNX Runtime's intra-op threads (0: its default).
	Threads int
	// Device is cpu (default) or cuda.
	Device string
}

// Load reads a model directory. InitializeEnvironment (ONNX Runtime's)
// must have run.
func Load(dir string, o Options) (*Model, error) {
	var cfg configFile
	if err := readJSON(filepath.Join(dir, "config.json"), &cfg); err != nil {
		return nil, err
	}
	var lf labelsFile
	if err := readJSON(filepath.Join(dir, "labels.json"), &lf); err != nil {
		return nil, err
	}
	if lf.MinScore <= 0 {
		lf.MinScore = 0.3
	}
	if cfg.BOS == nil || cfg.EOS == nil || cfg.Pad == nil {
		return nil, errors.New("engine: config.json lacks bos, eos or pad token ids")
	}
	m := &Model{firewall: lf.Labels, minScore: lf.MinScore, bos: *cfg.BOS, eos: *cfg.EOS, pad: *cfg.Pad}
	ids := make([]int, 0, len(cfg.ID2Label))
	for k := range cfg.ID2Label {
		i, err := strconv.Atoi(k)
		if err != nil {
			return nil, fmt.Errorf("engine: id2label key %q", k)
		}
		ids = append(ids, i)
	}
	sort.Ints(ids)
	for n, i := range ids {
		if n != i {
			return nil, errors.New("engine: id2label must number its labels from 0 without gaps")
		}
		label := cfg.ID2Label[strconv.Itoa(i)]
		m.labels = append(m.labels, label)
		tag, kind := byte('O'), ""
		if label != "O" {
			if len(label) < 3 || label[1] != '-' || !strings.ContainsRune("BIES", rune(label[0])) {
				return nil, fmt.Errorf("engine: label %q is neither O nor B-, I-, E-, S- something", label)
			}
			tag, kind = label[0], label[2:]
		}
		m.tags, m.kinds = append(m.tags, tag), append(m.kinds, kind)
	}
	m.rules()
	sum := sha256.New()
	for _, f := range []string{"model.onnx", "labels.json", "tokenizer.json"} {
		if err := hashFile(sum, filepath.Join(dir, f)); err != nil {
			return nil, err
		}
	}
	m.Version = hex.EncodeToString(sum.Sum(nil))[:16]
	tok, err := loadTokenizer(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, err
	}
	m.tok = tok
	so, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer so.Destroy()
	if o.Threads > 0 {
		if err := so.SetIntraOpNumThreads(o.Threads); err != nil {
			return nil, err
		}
	}
	if o.Device == "cuda" {
		cuda, err := ort.NewCUDAProviderOptions()
		if err != nil {
			return nil, fmt.Errorf("engine: cuda: %w", err)
		}
		defer cuda.Destroy()
		if err := so.AppendExecutionProviderCUDA(cuda); err != nil {
			return nil, fmt.Errorf("engine: cuda: %w", err)
		}
	}
	s, err := ort.NewDynamicAdvancedSession(filepath.Join(dir, "model.onnx"), []string{"input_ids", "attention_mask"},
		[]string{"logits"}, so)
	if err != nil {
		return nil, fmt.Errorf("engine: session: %w", err)
	}
	m.session = s
	return m, nil
}

// Close frees the session and the tokenizer.
func (m *Model) Close() {
	if m.session != nil {
		_ = m.session.Destroy()
	}
	if m.tok != nil {
		_ = m.tok.Close()
	}
}

// loadTokenizer reads a tokenizer without the truncation and padding its
// file may set: the engine reads long texts in windows of its own, and a
// tokenizer cutting at 512 tokens would hide everything after.
func loadTokenizer(path string) (*tokenizers.Tokenizer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("engine: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("engine: tokenizer.json: %w", err)
	}
	doc["truncation"], doc["padding"] = json.RawMessage("null"), json.RawMessage("null")
	if raw, err = json.Marshal(doc); err != nil {
		return nil, err
	}
	tok, err := tokenizers.FromBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("engine: tokenizer: %w", err)
	}
	return tok, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("engine: %s: %w", filepath.Base(path), err)
	}
	return nil
}

func hashFile(h io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer f.Close()
	_, err = io.Copy(h, f)
	return err
}
