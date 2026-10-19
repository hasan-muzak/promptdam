package engine

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hasan-muzak/promptdam/server/features/detector/wire"
)

// rules builds the scheme's transitions once: which tag may start the
// text, follow another, end the text. BIOES when the model has E or S
// tags; otherwise BIO, where a span ends where the next tag isn't its I.
func (m *Model) rules() {
	bioes := false
	for _, t := range m.tags {
		bioes = bioes || t == 'E' || t == 'S'
	}
	l := len(m.labels)
	m.start, m.end = make([]bool, l), make([]bool, l)
	m.follows = make([][]bool, l)
	for j, tj := range m.tags {
		m.start[j] = tj == 'O' || tj == 'B' || tj == 'S'
		m.end[j] = !bioes || tj == 'O' || tj == 'E' || tj == 'S'
	}
	for i, ti := range m.tags {
		m.follows[i] = make([]bool, l)
		for j, tj := range m.tags {
			switch {
			case bioes && (ti == 'O' || ti == 'E' || ti == 'S'):
				m.follows[i][j] = tj == 'O' || tj == 'B' || tj == 'S'
			case bioes:
				m.follows[i][j] = (tj == 'I' || tj == 'E') && m.kinds[i] == m.kinds[j]
			case tj == 'I':
				m.follows[i][j] = (ti == 'B' || ti == 'I') && m.kinds[i] == m.kinds[j]
			default:
				m.follows[i][j] = true
			}
		}
	}
}

// viterbi is the most likely tag path through logits ([tokens][labels])
// that the scheme allows, and each chosen tag's probability.
func (m *Model) viterbi(logits [][]float32) (path []int, prob []float64) {
	n, l := len(logits), len(m.labels)
	if n == 0 {
		return nil, nil
	}
	logp := make([][]float64, n)
	for t, row := range logits {
		logp[t] = logSoftmax(row)
	}
	score := make([]float64, l)
	back := make([][]int32, n)
	for j := range l {
		score[j] = math.Inf(-1)
		if m.start[j] {
			score[j] = logp[0][j]
		}
	}
	next := make([]float64, l)
	for t := 1; t < n; t++ {
		back[t] = make([]int32, l)
		for j := range l {
			best, arg := math.Inf(-1), int32(0)
			for i := range l {
				if s := score[i]; s > best && m.follows[i][j] {
					best, arg = s, int32(i)
				}
			}
			next[j], back[t][j] = best+logp[t][j], arg
		}
		score, next = next, score
	}
	last, best := 0, math.Inf(-1)
	for j := range l {
		if score[j] > best && m.end[j] {
			best, last = score[j], j
		}
	}
	path = make([]int, n)
	path[n-1] = last
	for t := n - 1; t > 0; t-- {
		path[t-1] = int(back[t][path[t]])
	}
	prob = make([]float64, n)
	for t, j := range path {
		prob[t] = math.Exp(logp[t][j])
	}
	return path, prob
}

func logSoftmax(row []float32) []float64 {
	mx := math.Inf(-1)
	for _, v := range row {
		mx = math.Max(mx, float64(v))
	}
	sum := 0.0
	for _, v := range row {
		sum += math.Exp(float64(v) - mx)
	}
	lse := mx + math.Log(sum)
	out := make([]float64, len(row))
	for i, v := range row {
		out[i] = float64(v) - lse
	}
	return out
}

// spans turns a tag path over tokens (offsets: each token's bytes in
// text) into spans of firewall labels, at least minScore sure on average.
func (m *Model) spans(text string, path []int, prob []float64, offsets [][2]int) []wire.Span {
	var out []wire.Span
	emit := func(from, to int) {
		kind := m.kinds[path[from]]
		label, ok := m.firewall[kind]
		if !ok || label == "" {
			return
		}
		sum := 0.0
		for t := from; t <= to; t++ {
			sum += prob[t]
		}
		score := sum / float64(to-from+1)
		start, end := offsets[from][0], offsets[to][1]
		start, end = trim(text, start, end)
		if score < m.minScore || !keep(text[start:end], label) {
			return
		}
		out = append(out, wire.Span{Start: start, End: end, Label: label, Score: math.Round(score*1000) / 1000})
	}
	from := -1
	for t, j := range path {
		tag := m.tags[j]
		switch {
		case tag == 'O':
			if from >= 0 {
				emit(from, t-1)
			}
			from = -1
		case tag == 'B' || tag == 'S' || from < 0 || m.kinds[path[from]] != m.kinds[j]:
			if from >= 0 {
				emit(from, t-1)
			}
			from = t
			if tag == 'S' {
				emit(t, t)
				from = -1
			}
		case tag == 'E':
			emit(from, t)
			from = -1
		}
	}
	if from >= 0 {
		emit(from, len(path)-1)
	}
	return out
}

// trim drops white space and quotes at a span's edges.
func trim(s string, start, end int) (int, int) {
	for start < end {
		r, n := utf8.DecodeRuneInString(s[start:end])
		if !unicode.IsSpace(r) && !strings.ContainsRune(`"'«»“”‘’`, r) {
			break
		}
		start += n
	}
	for end > start {
		r, n := utf8.DecodeLastRuneInString(s[start:end])
		if !unicode.IsSpace(r) && !strings.ContainsRune(`"'«»“”‘’,.;:`, r) {
			break
		}
		end -= n
	}
	return start, end
}

// keep drops what is too small to be what a label says: one character
// (an ID may be short, a name or an address isn't), and a lone Chinese or
// Japanese character, which the model tags in passing.
func keep(v, label string) bool {
	n := utf8.RuneCountInString(v)
	if n == 0 {
		return false
	}
	if n == 1 {
		return label == "government_id" || label == "account_number"
	}
	return true
}
