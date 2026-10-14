package classifier

import (
	"encoding/binary"
	"math"
	"slices"
	"sort"
	"time"
)

// Model is a router's classifier: one model per tier but the default.
type Model struct {
	Version   int       `json:"version"`
	TrainedAt time.Time `json:"trained_at"`
	// Default is the router's default tier when the model was trained: a
	// model of another default tier says nothing of this one.
	Default string      `json:"default"`
	Tiers   []TierModel `json:"tiers"`
}

// TierModel predicts, for one tier, the probability that a call is one the
// tier is right for: for a tier cheaper than the default, that its answer
// holds up (as good or better); for a stronger one, that the call needed
// it (better).
type TierModel struct {
	Tier string `json:"tier"`
	// Rank is the tier's place against the default tier: negative for
	// cheaper tiers, positive for stronger ones.
	Rank int     `json:"rank"`
	Bias float32 `json:"bias"`
	// Threshold is the probability from which calls are placed on the
	// tier, calibrated on held-out samples; 0 places none (too few
	// samples, or no threshold met the bar).
	Threshold float32 `json:"threshold"`
	// Weights are the model's sparse weights, packed (encodeWeights).
	Weights []byte      `json:"weights,omitempty"`
	Metrics TierMetrics `json:"metrics"`
	// OutputRatio is how many output tokens the default tier writes for
	// each one this tier writes on the same calls (judged samples answered
	// by both), and OutputPairs how many calls measured it: the ledger
	// prices what a routed call would have cost on the default tier with
	// it (0: not measured on enough calls).
	OutputRatio float64 `json:"output_ratio,omitempty"`
	OutputPairs int     `json:"output_pairs,omitempty"`

	// index and weight are Weights unpacked (Prepare).
	prepared bool
	index    []uint32
	weight   []float32
}

// MinOutputPairs is how many calls answered by both a tier and the
// default tier its output ratio needs.
const MinOutputPairs = 50

// Ratio is the tier's output ratio against the default tier, when enough
// calls measured it.
func (t *TierModel) Ratio() (float64, bool) {
	if t == nil || t.OutputPairs < MinOutputPairs || t.OutputRatio <= 0 {
		return 0, false
	}
	return t.OutputRatio, true
}

// Tier is a tier's model (nil when the classifier has none).
func (m *Model) Tier(name string) *TierModel {
	if m == nil {
		return nil
	}
	for i := range m.Tiers {
		if m.Tiers[i].Tier == name {
			return &m.Tiers[i]
		}
	}
	return nil
}

// TierMetrics say how a tier's model did on samples it wasn't trained on.
type TierMetrics struct {
	// Samples judged, and how many of them the tier was right for.
	Samples   int `json:"samples"`
	Positives int `json:"positives"`
	// Precision is the share of right calls among those over the
	// threshold; Coverage the share of calls over it.
	Precision float64 `json:"precision"`
	Coverage  float64 `json:"coverage"`
	// AUC is how well the model ranks right calls over wrong ones (0.5:
	// no better than chance).
	AUC float64 `json:"auc"`
}

// Prepare unpacks the model's weights. Call it once before the model is
// used by more than one goroutine; until then each prediction unpacks
// them anew.
func (m *Model) Prepare() {
	for i := range m.Tiers {
		t := &m.Tiers[i]
		t.index, t.weight = decodeWeights(t.Weights)
		t.prepared = true
	}
}

// Prob is the probability that the tier is right for a call with these
// features (sorted, as Features returns them).
func (t *TierModel) Prob(features []uint32) float64 {
	index, weight := t.index, t.weight
	if !t.prepared {
		index, weight = decodeWeights(t.Weights)
	}
	z := float64(t.Bias)
	for _, f := range features {
		if i, ok := slices.BinarySearch(index, f); ok {
			z += float64(weight[i])
		}
	}
	return sigmoid(z)
}

func sigmoid(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// Placement is what the classifier says of a call.
type Placement struct {
	// Tier is the tier it places the call on ("" for none) and P the
	// probability that tier is right.
	Tier string
	P    float64
	// Unsure reports a call it placed on no tier while a cheaper tier may
	// hold up for it (a probability of at least ½, under the threshold),
	// or one it has no trained tier for: the calls the decision model is
	// asked about.
	Unsure bool
}

// Place places a call: on the strongest tier above the default that it
// needs, else on the cheapest below the default that holds up for it, else
// on none.
func (m *Model) Place(features []uint32) Placement {
	ordered := make([]*TierModel, 0, len(m.Tiers))
	for i := range m.Tiers {
		ordered = append(ordered, &m.Tiers[i])
	}
	// Stronger tiers, strongest first, then cheaper ones, cheapest first.
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].Rank, ordered[j].Rank
		if (a > 0) != (b > 0) {
			return a > 0
		}
		if a > 0 {
			return a > b
		}
		return a < b
	})
	trained, unsure := false, false
	for _, t := range ordered {
		if t.Threshold <= 0 {
			continue
		}
		trained = true
		p := t.Prob(features)
		if p >= float64(t.Threshold) {
			return Placement{Tier: t.Tier, P: p}
		}
		if t.Rank < 0 && p >= 0.5 {
			unsure = true
		}
	}
	return Placement{Unsure: unsure || !trained}
}

// encodeWeights packs sparse weights, indexes ascending: each the gap from
// the last as a uvarint, then the weight as a float32.
func encodeWeights(index []uint32, weight []float32) []byte {
	out := make([]byte, 0, len(index)*6)
	var last uint32
	for i, idx := range index {
		out = binary.AppendUvarint(out, uint64(idx-last))
		out = binary.LittleEndian.AppendUint32(out, math.Float32bits(weight[i]))
		last = idx
	}
	return out
}

func decodeWeights(b []byte) ([]uint32, []float32) {
	var index []uint32
	var weight []float32
	var last uint32
	for len(b) > 0 {
		gap, n := binary.Uvarint(b)
		if n <= 0 || len(b) < n+4 {
			break
		}
		last += uint32(gap)
		index = append(index, last)
		weight = append(weight, math.Float32frombits(binary.LittleEndian.Uint32(b[n:])))
		b = b[n+4:]
	}
	return index, weight
}
