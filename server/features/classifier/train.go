package classifier

import (
	"math"
	"math/rand/v2"
	"sort"
)

// Sample is one judged call: its features and whether the tier was right
// for it.
type Sample struct {
	Features []uint32
	Y        bool
}

// Options shape a tier's training.
type Options struct {
	// MinSamples is the fewest judged calls a tier needs to get a
	// threshold (default 200).
	MinSamples int
	// Floor is the share of right calls the calls placed on a cheaper tier
	// must reach (the router's quality floor, default 0.95); calls placed
	// on a stronger tier need only be more likely right than not.
	Floor float64
	// MinPlaced is the fewest held-out calls over a threshold that can
	// justify it (default 20).
	MinPlaced int
	// Epochs, Rate and L2 tune the fit (defaults 10, 0.1, 1e-4); Folds
	// the cross-validation (default 5); Seed the shuffles.
	Epochs int
	Rate   float64
	L2     float64
	Folds  int
	Seed   uint64
}

func (o *Options) defaults() {
	if o.MinSamples == 0 {
		o.MinSamples = 200
	}
	if o.Floor == 0 {
		o.Floor = 0.95
	}
	if o.MinPlaced == 0 {
		o.MinPlaced = 20
	}
	if o.Epochs == 0 {
		o.Epochs = 10
	}
	if o.Rate == 0 {
		o.Rate = 0.1
	}
	if o.L2 == 0 {
		o.L2 = 1e-4
	}
	if o.Folds == 0 {
		o.Folds = 5
	}
}

// TrainTier fits a tier's model and calibrates its threshold on held-out
// samples (cross-validation). The model is the average of the folds'
// models, so the threshold applies to scores of the same scale.
//
// For a cheaper tier the threshold is the lowest at which the lower bound
// (Wilson, 95%) of the right calls' share among the calls over it still
// meets the floor. For a stronger tier it is the lowest at which the calls
// around it (a window of the held-out calls) needed the tier at least half
// the time: sending a call up costs money, not quality. A tier with too
// few samples, or none of both kinds, gets no threshold.
func TrainTier(tier string, rank int, samples []Sample, o Options) TierModel {
	o.defaults()
	t := TierModel{Tier: tier, Rank: rank}
	t.Metrics.Samples = len(samples)
	for _, s := range samples {
		if s.Y {
			t.Metrics.Positives++
		}
	}
	if len(samples) < o.MinSamples || t.Metrics.Positives == 0 || t.Metrics.Positives == len(samples) {
		return t
	}
	order := make([]int, len(samples))
	for i := range order {
		order[i] = i
	}
	rng := rand.New(rand.NewPCG(o.Seed, 0x5eed))
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	// Out-of-fold probabilities: each sample scored by a model that never
	// saw it. The folds' models are summed into avg.
	scored := make([]scoredSample, 0, len(samples))
	w := make([]float64, Dim)
	g := make([]float64, Dim)
	avg := make([]float64, Dim)
	var avgBias float64
	for fold := range o.Folds {
		var train []int
		for k, i := range order {
			if k%o.Folds != fold {
				train = append(train, i)
			}
		}
		bias := fit(samples, train, w, g, o, rng)
		for k, i := range order {
			if k%o.Folds == fold {
				scored = append(scored, scoredSample{p: score(w, bias, samples[i].Features), y: samples[i].Y})
			}
		}
		for i, v := range w {
			avg[i] += v
		}
		avgBias += bias
	}
	t.Metrics.AUC = auc(scored)
	var threshold, precision, coverage float64
	if rank < 0 {
		threshold, precision, coverage = calibrateDown(scored, o.Floor, o.MinPlaced)
	} else {
		threshold, precision, coverage = calibrateUp(scored, o.MinPlaced)
	}
	if threshold <= 0 {
		return t
	}
	t.Metrics.Precision, t.Metrics.Coverage = precision, coverage

	var index []uint32
	var weight []float32
	k := float64(o.Folds)
	for i, v := range avg {
		if v /= k; math.Abs(v) >= 1e-5 {
			index = append(index, uint32(i))
			weight = append(weight, float32(v))
		}
	}
	t.Bias, t.Threshold, t.Weights = float32(avgBias/k), float32(threshold), encodeWeights(index, weight)
	return t
}

type scoredSample struct {
	p float64
	y bool
}

// fit trains a logistic regression on the samples at idx (AdaGrad, L2 on
// the weights a sample touches), into w (reset) using g as scratch, and
// returns its bias. Features are binary.
func fit(samples []Sample, idx []int, w, g []float64, o Options, rng *rand.Rand) float64 {
	clear(w)
	clear(g)
	var bias, gb float64
	const eps = 1e-8
	order := append([]int(nil), idx...)
	for range o.Epochs {
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, i := range order {
			s := samples[i]
			p := score(w, bias, s.Features)
			y := 0.0
			if s.Y {
				y = 1
			}
			grad := p - y
			gb += grad * grad
			bias -= o.Rate * grad / math.Sqrt(gb+eps)
			for _, f := range s.Features {
				gf := grad + o.L2*w[f]
				g[f] += gf * gf
				w[f] -= o.Rate * gf / math.Sqrt(g[f]+eps)
			}
		}
	}
	return bias
}

func score(w []float64, bias float64, features []uint32) float64 {
	z := bias
	for _, f := range features {
		z += w[f]
	}
	return sigmoid(z)
}

// calibrateDown finds the lowest probability threshold whose calls over it
// (at least minPlaced) are right at a share whose Wilson lower bound meets
// floor; 0 when none does. It never splits calls of the same probability.
func calibrateDown(scored []scoredSample, floor float64, minPlaced int) (threshold, precision, coverage float64) {
	sort.Slice(scored, func(i, j int) bool { return scored[i].p > scored[j].p })
	right := 0
	for k := 1; k <= len(scored); k++ {
		if scored[k-1].y {
			right++
		}
		if k < len(scored) && scored[k].p == scored[k-1].p {
			continue
		}
		if lo, _ := Wilson(right, k); k >= minPlaced && lo >= floor {
			threshold, precision, coverage = scored[k-1].p, float64(right)/float64(k), float64(k)/float64(len(scored))
		}
	}
	return threshold, precision, coverage
}

// calibrateUp finds the lowest probability threshold over which calls,
// from the most likely down, keep needing the tier at least half the time
// around each (a window of a tenth of the calls, at least 2 × minPlaced);
// 0 when fewer than minPlaced calls qualify.
func calibrateUp(scored []scoredSample, minPlaced int) (threshold, precision, coverage float64) {
	sort.Slice(scored, func(i, j int) bool { return scored[i].p > scored[j].p })
	n := len(scored)
	right := make([]int, n+1) // right[k]: right calls among the k most likely
	for k, s := range scored {
		right[k+1] = right[k]
		if s.y {
			right[k+1]++
		}
	}
	half := max(n/20, minPlaced)
	last := 0
	for k := 1; k <= n; k++ {
		lo, hi := max(0, k-half), min(n, k+half)
		if 2*(right[hi]-right[lo]) < hi-lo {
			break
		}
		if k == n || scored[k].p != scored[k-1].p {
			last = k
		}
	}
	if last < minPlaced {
		return 0, 0, 0
	}
	return scored[last-1].p, float64(right[last]) / float64(last), float64(last) / float64(n)
}

// auc is the probability that a right call scores over a wrong one.
func auc(scored []scoredSample) float64 {
	s := append([]scoredSample(nil), scored...)
	sort.Slice(s, func(i, j int) bool { return s[i].p < s[j].p })
	var pos, neg, rankSum float64
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j].p == s[i].p {
			j++
		}
		avg := float64(i+j+1) / 2 // ties share their average rank (1-based)
		for k := i; k < j; k++ {
			if s[k].y {
				pos++
				rankSum += avg
			} else {
				neg++
			}
		}
		i = j
	}
	if pos == 0 || neg == 0 {
		return 0.5
	}
	return (rankSum - pos*(pos+1)/2) / (pos * neg)
}

// Wilson is the 95% interval of a share of k in n (Wilson score).
func Wilson(k, n int) (lo, hi float64) {
	if n == 0 {
		return 0, 1
	}
	const z = 1.959964
	p, nf := float64(k)/float64(n), float64(n)
	den := 1 + z*z/nf
	mid := p + z*z/(2*nf)
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	return max(0, (mid-half)/den), min(1, (mid+half)/den)
}
