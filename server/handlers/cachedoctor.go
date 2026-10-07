package handlers

import (
	"errors"
	"time"

	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel/pkg/httpx"
	"github.com/hasan-muzak/keel/pkg/platform"
	"github.com/hasan-muzak/keel/pkg/tenancy"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/cachedoctor"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/families"
)

// FindingOut is one reason a family misses the cache.
type FindingOut struct {
	Code     string `json:"code" doc:"ok, prefix_too_short, timestamps_in_prefix, ids_in_prefix, numbers_in_prefix, tool_order_changes, instructions_vary or no_breakpoints"`
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
}

func diagnosisOut(d cachedoctor.Diagnosis) DiagnosisOut {
	out := DiagnosisOut{Family: d.Family, Model: d.Model, Provider: d.Kind, Requests: d.Requests, Prefixes: d.Prefixes,
		InputTokens: d.InputTokens, CacheReadTokens: d.CacheReadTokens, HitRate: d.HitRate, PrefixTokens: d.PrefixTokens,
		Findings: make([]FindingOut, len(d.Findings)), PotentialMonthlyMicros: d.PotentialMonthlyMicros}
	for i, f := range d.Findings {
		out.Findings[i] = FindingOut{Code: f.Code, Severity: f.Severity, Title: f.Title, Detail: f.Detail, Fix: f.Fix}
	}
	return out
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
	for i, d := range ds {
		out.Data[i] = diagnosisOut(d)
	}
	return out, nil
}
