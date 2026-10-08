package routers

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hasan-muzak/keel-core/pkg/llm"

	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
)

// Candidate is a model one of the project's providers serves, with what the
// price list knows of it.
type Candidate struct {
	// Provider is the provider's name in the project, Kind its kind.
	Provider, Kind string
	// Model is the model as the provider names it, with its prices and
	// features.
	Model llm.Model
}

// ref is how a tier names the candidate: provider/model, so the model goes
// to that provider even when another serves the same name.
func (c Candidate) ref() string { return c.Provider + "/" + c.Model.ID }

// blended prices a typical call: three input tokens for each output one.
func (c Candidate) blended() float64 { return 3*c.Model.Input + c.Model.Output }

// Suggestion is a router a project could start from.
type Suggestion struct {
	Spec  tiers.Spec
	Judge string
	// Picks say, per tier, what serves it and how it was picked.
	Picks []Pick
	Notes []string
}

// Pick is how a suggested tier was picked.
type Pick struct {
	Tier     string
	Provider string
	Input    float64
	Output   float64
	// How: "family" (the newest of a known family), "price" (by price
	// among the provider's models) or "current" (the model the project
	// uses most).
	How string
}

// Roles of suggested tiers, cheapest first.
var roles = []string{"fast", "standard", "expert"}

var descriptions = map[string]string{
	"fast":     "Short and simple requests: greetings, formatting, extraction, classification, yes/no answers.",
	"standard": "Everyday work: summaries, rewriting, questions about a text, simple code.",
	"expert":   "Hard problems: multi-step reasoning, complex code, legal or financial judgment, long planning.",
	"current":  "The model the project uses most today.",
}

// families are known model families by role, matched on a model's name
// without its vendor prefix ("anthropic/claude-x" is "claude-x").
var families = [][3]*regexp.Regexp{
	{regexp.MustCompile(`^claude-haiku-\d+([.-]\d+)?$`), regexp.MustCompile(`^claude-sonnet-\d+([.-]\d+)?$`),
		regexp.MustCompile(`^claude-opus-\d+([.-]\d+)?$`)},
	{regexp.MustCompile(`^gpt-\d+(\.\d+)?-nano$`), regexp.MustCompile(`^gpt-\d+(\.\d+)?-mini$`), regexp.MustCompile(`^gpt-\d+(\.\d+)?$`)},
	{regexp.MustCompile(`^gemini-\d+(\.\d+)?-flash-lite$`), regexp.MustCompile(`^gemini-\d+(\.\d+)?-flash$`),
		regexp.MustCompile(`^gemini-\d+(\.\d+)?-pro$`)},
	{regexp.MustCompile(`^(ministral-\d+b|mistral-small)(-latest|-\d+)?$`), regexp.MustCompile(`^mistral-medium(-latest|-\d+)?$`),
		regexp.MustCompile(`^mistral-large(-latest|-\d+)?$`)},
	{regexp.MustCompile(`^deepseek-flash$`), regexp.MustCompile(`^deepseek-chat$`), regexp.MustCompile(`^deepseek-reasoner$`)},
	{regexp.MustCompile(`^grok-\d+(\.\d+)?-(fast|mini)$`), nil, regexp.MustCompile(`^grok-\d+(\.\d+)?$`)},
}

var (
	digitsRE = regexp.MustCompile(`\d+`)
	datedRE  = regexp.MustCompile(`\d{8}|preview|exp`)
)

// version is a model name's numbers, for "newest": gpt-5.1 → [5 1].
func version(name string) []int {
	var out []int
	for _, d := range digitsRE.FindAllString(name, -1) {
		n, _ := strconv.Atoi(d)
		out = append(out, n)
	}
	return out
}

func bare(id string) string {
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.ToLower(id)
}

// ladder is one provider's model per role: the newest of a known family,
// else by price among its models.
func ladder(cands []Candidate) (map[string]Candidate, map[string]string) {
	out, how := map[string]Candidate{}, map[string]string{}
	for r := range roles {
		var best *Candidate
		for i := range cands {
			c := &cands[i]
			name := bare(c.Model.ID)
			if datedRE.MatchString(name) {
				continue
			}
			matched := false
			for _, f := range families {
				if f[r] != nil && f[r].MatchString(name) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if best == nil {
				best = c
				continue
			}
			v, bv := version(name), version(bare(best.Model.ID))
			if cmp := slices.Compare(v, bv); cmp > 0 || cmp == 0 && c.blended() < best.blended() {
				best = c
			}
		}
		if best != nil {
			out[roles[r]], how[roles[r]] = *best, "family"
		}
	}
	if len(out) > 0 {
		return out, how
	}
	// No known family: the 25th, 50th and 90th percentile by price.
	byPrice := slices.Clone(cands)
	sort.SliceStable(byPrice, func(i, j int) bool { return byPrice[i].blended() < byPrice[j].blended() })
	for r, q := range []float64{0.25, 0.5, 0.9} {
		if len(byPrice) == 0 {
			break
		}
		out[roles[r]], how[roles[r]] = byPrice[int(math.Round(q*float64(len(byPrice)-1)))], "price"
	}
	return out, how
}

// ErrNoModels is returned when none of the project's providers serves a
// priced chat model.
var ErrNoModels = errors.New("routers: no provider of the project serves a priced chat model")

// Suggest builds a router from the project's providers' models (in the
// providers' priority order): 3 tiers (or 2: fast and expert), each the
// newest model of a known family where one is served, from cheapest to
// strongest, with the same tier of the other providers as its fallbacks.
// current, when set, is the model the project uses most: it becomes the
// default tier, so savings are counted against it.
func Suggest(n int, cands []Candidate, current *Candidate) (Suggestion, error) {
	var usable []Candidate
	order := []string{}
	byProvider := map[string][]Candidate{}
	for _, c := range cands {
		if c.Model.Output <= 0 || c.Model.Input <= 0 {
			continue // embeddings, decisions, unpriced
		}
		if has, known := c.Model.Has(llm.FeatureTools); known && !has {
			continue
		}
		usable = append(usable, c)
		if _, seen := byProvider[c.Provider]; !seen {
			order = append(order, c.Provider)
		}
		byProvider[c.Provider] = append(byProvider[c.Provider], c)
	}
	if len(usable) == 0 {
		return Suggestion{}, ErrNoModels
	}
	wanted := roles
	if n == 2 {
		wanted = []string{"fast", "expert"}
	}

	type tier struct {
		role string
		main Candidate
		how  string
		alts []Candidate
	}
	var ladders []map[string]Candidate
	var hows []map[string]string
	for _, p := range order {
		l, h := ladder(byProvider[p])
		ladders, hows = append(ladders, l), append(hows, h)
	}
	var built []tier
	for _, role := range wanted {
		var t *tier
		for i, l := range ladders {
			c, ok := l[role]
			if !ok {
				continue
			}
			if t == nil {
				t = &tier{role: role, main: c, how: hows[i][role]}
			} else if len(t.alts) < 2 && c.Model.ID != t.main.Model.ID {
				t.alts = append(t.alts, c)
			}
		}
		if t != nil {
			built = append(built, *t)
		}
	}
	// One model for two roles (a provider with few models) is one tier.
	seen := map[string]bool{}
	kept := built[:0]
	for _, t := range built {
		if !seen[t.main.ref()] {
			seen[t.main.ref()] = true
			kept = append(kept, t)
		}
	}
	built = kept
	sort.SliceStable(built, func(i, j int) bool { return built[i].main.blended() < built[j].main.blended() })

	def := ""
	var notes []string
	if current != nil {
		for _, t := range built {
			if t.main.Model.ID == current.Model.ID || bare(t.main.Model.ID) == bare(current.Model.ID) {
				def = t.role
			}
		}
		if def == "" && current.Model.Output > 0 {
			built = append(built, tier{role: "current", main: *current, how: "current"})
			sort.SliceStable(built, func(i, j int) bool { return built[i].main.blended() < built[j].main.blended() })
			def = "current"
		}
		if def != "" {
			notes = append(notes, "The default tier is "+current.Model.ID+", the model your calls used most: every other "+
				"tier is judged against it, and savings are counted from it.")
		}
	}

	// Names follow the order by price (fast, standard, expert; fast and
	// expert for two), the current model's tier keeping its own.
	others := 0
	for _, t := range built {
		if t.role != "current" {
			others++
		}
	}
	s := Suggestion{}
	names := make([]string, 0, len(built))
	defName, j := "", 0
	for _, t := range built {
		name := "current"
		if t.role != "current" {
			name = roles[min(j, len(roles)-1)]
			if others == 2 && j == 1 {
				name = "expert"
			}
			j++
		}
		for slices.Contains(names, name) {
			name += "-2"
		}
		names = append(names, name)
		if t.role == def {
			defName = name
		}
		desc := descriptions[name]
		if t.role == "current" {
			desc = descriptions["current"]
		}
		tt := tiers.Tier{Name: name, Description: desc, Model: t.main.ref()}
		for _, a := range t.alts {
			tt.Fallbacks = append(tt.Fallbacks, a.ref())
		}
		s.Spec.Tiers = append(s.Spec.Tiers, tt)
		s.Picks = append(s.Picks, Pick{Tier: name, Provider: t.main.Provider, Input: t.main.Model.Input, Output: t.main.Model.Output, How: t.how})
		if t.how == "price" {
			notes = append(notes, "Tier "+name+" was picked by price among "+t.main.Provider+"'s models: check that it suits.")
		}
	}
	def = defName
	if def == "" {
		def = names[len(names)-1]
	}
	s.Spec.Default = def
	s.Judge = judgeOf(usable, s.Spec)
	notes = append(notes, "Start in shadow mode: the router decides and evaluates your calls but serves the default tier, and "+
		"its report shows what each tier would save and how well it holds up, before it routes anything.")
	s.Notes = notes
	return s, nil
}

// judgeOf is a suggested judge: the newest Claude Sonnet a provider
// serves, else the strongest tier's model.
func judgeOf(cands []Candidate, spec tiers.Spec) string {
	sonnet := families[0][1]
	var best *Candidate
	for i := range cands {
		c := &cands[i]
		name := bare(c.Model.ID)
		if sonnet.MatchString(name) && (best == nil || slices.Compare(version(name), version(bare(best.Model.ID))) > 0) {
			best = c
		}
	}
	if best != nil {
		return best.ref()
	}
	return spec.Tiers[len(spec.Tiers)-1].Model
}
