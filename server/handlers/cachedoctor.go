package handlers

import (
	"errors"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel-core/pkg/httpx"
	"github.com/hasan-muzak/keel-core/pkg/platform"
	"github.com/hasan-muzak/keel-core/pkg/tenancy"

	"github.com/hasan-muzak/promptdam/server/catalog"
	"github.com/hasan-muzak/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/promptdam/server/features/families"
)

// FindingOut is one reason a family misses the cache.
type FindingOut struct {
	Code     string `json:"code" doc:"ok, prefix_too_short, timestamps_in_prefix, ids_in_prefix, numbers_in_prefix, tool_order_changes, instructions_vary, no_breakpoints, kept_one_hour or parallel_cold_writes"`
	Severity string `json:"severity" doc:"info or warning"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
}

// DiagnosisOut is a family's prompt cache health over a range.
type DiagnosisOut struct {
	Family                 string       `json:"family"`
	Model                  string       `json:"model"`
	Provider               string       `json:"provider_kind"`
	Requests               uint64       `json:"requests"`
	Prefixes               uint64       `json:"prefixes" doc:"Different prefixes (instructions and tools) the calls sent"`
	InputTokens            uint64       `json:"input_tokens"`
	CacheReadTokens        uint64       `json:"cache_read_tokens"`
	HitRate                float64      `json:"hit_rate" doc:"Share of input tokens the providers' caches served"`
	PrefixTokens           uint64       `json:"prefix_tokens" doc:"Estimated length of the prefix"`
	Findings               []FindingOut `json:"findings"`
	PotentialMonthlyMicros int64        `json:"potential_monthly_micros" doc:"What caching could save a month at most"`
	PrefixTTL              string       `json:"prefix_ttl" doc:"How long the gateway keeps the prefix cached: 1h, or 5m (learned from the calls' gaps)"`
	TailTTL                string       `json:"tail_ttl" doc:"How long the gateway keeps conversations' tails cached: 1h, or 5m"`
	// Locked leaves the findings' details and fixes out.
	Locked bool `json:"locked" doc:"The plan doesn't include the cache doctor: findings come without their detail and fix"`
}

func diagnosisOut(d cachedoctor.Diagnosis) DiagnosisOut {
	out := DiagnosisOut{Family: d.Family, Model: d.Model, Provider: d.Kind, Requests: d.Requests, Prefixes: d.Prefixes,
		InputTokens: d.InputTokens, CacheReadTokens: d.CacheReadTokens, HitRate: d.HitRate, PrefixTokens: d.PrefixTokens,
		Findings: make([]FindingOut, len(d.Findings)), PotentialMonthlyMicros: d.PotentialMonthlyMicros,
		PrefixTTL: or5m(d.TTL.Prefix), TailTTL: or5m(d.TTL.Tail)}
	for i, f := range d.Findings {
		out.Findings[i] = FindingOut{Code: f.Code, Severity: f.Severity, Title: f.Title, Detail: f.Detail, Fix: f.Fix}
	}
	return out
}

func or5m(ttl string) string {
	if ttl == "" {
		return "5m"
	}
	return ttl
}

// CacheDoctorIn asks for a diagnosis over a range.
type CacheDoctorIn struct {
	platform.ProjectPath
	TimeRange
}

// GetCacheDoctor diagnoses the project's 20 busiest families.
func (h *Handlers) GetCacheDoctor(ctx *muzak.Context, in CacheDoctorIn) (httpx.List[DiagnosisOut], error) {
	return h.diagnose(ctx, "", in.TimeRange)
}

// FamilyCacheDoctorIn asks for one family's diagnosis.
type FamilyCacheDoctorIn struct {
	FamilyPath
	TimeRange
}

// GetFamilyCacheDoctor diagnoses one family.
func (h *Handlers) GetFamilyCacheDoctor(ctx *muzak.Context, in FamilyCacheDoctorIn) (DiagnosisOut, error) {
	if !families.ValidID(in.Family) {
		return DiagnosisOut{}, httpx.NotFound("family")
	}
	list, err := h.diagnose(ctx, in.Family, in.TimeRange)
	if err != nil {
		return DiagnosisOut{}, err
	}
	if len(list.Data) == 0 {
		return DiagnosisOut{}, httpx.NotFound("family")
	}
	return list.Data[0], nil
}

func (h *Handlers) diagnose(ctx *muzak.Context, family string, r TimeRange) (httpx.List[DiagnosisOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := r.bounds(7 * 24 * time.Hour)
	if err != nil {
		return httpx.List[DiagnosisOut]{}, err
	}
	ds, err := h.CacheDoctor.Diagnose(ctx.Context(), pa.Project.ID, family, from, to)
	if errors.Is(err, cachedoctor.ErrRange) {
		return httpx.List[DiagnosisOut]{}, muzak.UnprocessableEntity("the time range must start before it ends and cover at most 90 days").
			WithCode(CodeInvalidQuery)
	}
	if err != nil {
		return httpx.List[DiagnosisOut]{}, err
	}
	out := httpx.List[DiagnosisOut]{Data: make([]DiagnosisOut, len(ds))}
	// Without the plan, what the doctor found and what fixing it is worth
	// show; how to fix it doesn't.
	locked := len(ds) > 0 && !h.planHas(ctx, catalog.PromptCaching)
	for i, d := range ds {
		out.Data[i] = diagnosisOut(d)
		if locked {
			out.Data[i].Locked = true
			for j := range out.Data[i].Findings {
				out.Data[i].Findings[j].Detail, out.Data[i].Findings[j].Fix = "", ""
			}
		}
	}
	return out, nil
}
