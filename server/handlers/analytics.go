package handlers

import (
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel/pkg/httpx"
	"github.com/hasan-muzak/keel/pkg/ids"
	"github.com/hasan-muzak/keel/pkg/platform"
	"github.com/hasan-muzak/keel/pkg/tenancy"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/analytics"
)

// CodeInvalidQuery reports an analytics query that can't be answered.
const CodeInvalidQuery = "invalid_query"

// TimeRange is a query's time range (RFC 3339).
type TimeRange struct {
	From string `query:"from" doc:"Start (RFC 3339), inclusive"`
	To   string `query:"to" doc:"End (RFC 3339), exclusive; default now"`
}

// bounds parses the range, from to-def when From is empty.
func (r TimeRange) bounds(def time.Duration) (from, to time.Time, err error) {
	to = time.Now().UTC()
	if r.To != "" {
		if to, err = time.Parse(time.RFC3339, r.To); err != nil {
			return from, to, muzak.UnprocessableEntity("to is not an RFC 3339 time").WithCode(CodeInvalidQuery)
		}
	}
	from = to.Add(-def)
	if r.From != "" {
		if from, err = time.Parse(time.RFC3339, r.From); err != nil {
			return from, to, muzak.UnprocessableEntity("from is not an RFC 3339 time").WithCode(CodeInvalidQuery)
		}
	}
	return from, to, nil
}

// RequestOut is a logged gateway call.
type RequestOut struct {
	ID               string    `json:"id" doc:"The call's trace (req_…), as X-Promptdam-Trace returned it"`
	Time             time.Time `json:"time"`
	Key              string    `json:"key" doc:"API key ID (key_…)"`
	Developer        string    `json:"developer"`
	User             string    `json:"user" doc:"The end user the client named (X-Promptdam-User)"`
	Tags             []string  `json:"tags"`
	Route            string    `json:"route"`
	APIFormat        string    `json:"api_format" doc:"openai.chat, openai.responses, openai.embeddings, anthropic.messages…"`
	Provider         string    `json:"provider"`
	RequestedModel   string    `json:"requested_model"`
	ServedModel      string    `json:"served_model"`
	Stream           bool      `json:"stream"`
	Status           int       `json:"status" doc:"HTTP status the client got (499: it left)"`
	ErrorCode        string    `json:"error_code"`
	Attempts         int       `json:"attempts" doc:"Provider calls made, retries and fallbacks included"`
	TTFBms           int       `json:"ttfb_ms"`
	LatencyMs        int       `json:"latency_ms"`
	UpstreamMs       int       `json:"upstream_ms"`
	UsageKnown       bool      `json:"usage_known"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	CacheReadTokens  int       `json:"cache_read_tokens"`
	CacheWriteTokens int       `json:"cache_write_tokens"`
	ReasoningTokens  int       `json:"reasoning_tokens"`
	CostMicros       int64     `json:"cost_micros" doc:"At list prices, in millionths of a dollar"`
	SavingsMicros    int64     `json:"savings_micros" doc:"Against the model asked for at list price without any cache"`
	SavingsReason    string    `json:"savings_reason"`
	Cache            string    `json:"cache" doc:"hit or miss on routes with the response cache"`
	Family           string    `json:"family"`
}

func requestOut(r analytics.Request) RequestOut {
	return RequestOut{
		ID: r.Trace, Time: r.TS.UTC(), Key: ids.Encode(ids.APIKey, r.KeyID), Developer: r.Developer, User: r.User,
		Tags: r.Tags, Route: r.Route, APIFormat: r.APIFormat, Provider: r.Provider, RequestedModel: r.RequestedModel,
		ServedModel: r.ServedModel, Stream: r.Stream, Status: int(r.Status), ErrorCode: r.ErrorCode, Attempts: int(r.Attempts),
		TTFBms: int(r.TTFBms), LatencyMs: int(r.LatencyMs), UpstreamMs: int(r.UpstreamMs), UsageKnown: r.UsageKnown,
		InputTokens: int(r.InputTokens), OutputTokens: int(r.OutputTokens), CacheReadTokens: int(r.CacheReadTokens),
		CacheWriteTokens: int(r.CacheWriteTokens), ReasoningTokens: int(r.ReasoningTokens), CostMicros: r.CostMicros,
		SavingsMicros: r.Savings, SavingsReason: r.SavingsReason, Cache: r.Cache, Family: r.Family,
	}
}

// ListRequestsIn filters the request log.
type ListRequestsIn struct {
	platform.ProjectPath
	httpx.Page
	TimeRange
	Model     string `query:"model" doc:"Only calls served by this model"`
	Provider  string `query:"provider"`
	Route     string `query:"route"`
	Key       string `query:"key" doc:"Only this API key's calls (key_…)"`
	Developer string `query:"developer"`
	Errors    bool   `query:"errors" doc:"Only failed calls"`
}

// ListRequests lists a project's gateway calls, newest first.
func (h *Handlers) ListRequests(ctx *muzak.Context, in ListRequestsIn) (httpx.List[RequestOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(24 * time.Hour)
	if err != nil {
		return httpx.List[RequestOut]{}, err
	}
	f := analytics.Filter{From: from, To: to, Model: in.Model, Provider: in.Provider, Route: in.Route,
		Developer: in.Developer, Errors: in.Errors}
	if in.Key != "" {
		if f.Key, err = ids.Decode(ids.APIKey, in.Key); err != nil {
			return httpx.List[RequestOut]{}, httpx.NotFound("API key")
		}
	}
	var after *analytics.Cursor
	if in.Cursor != "" {
		c, err := analytics.DecodeCursor(in.Cursor)
		if err != nil {
			return httpx.List[RequestOut]{}, analyticsFail(err)
		}
		after = &c
	}
	rows, next, err := h.Analytics.Requests(ctx.Context(), pa.Project.ID, f, after, in.Limit)
	if err != nil {
		return httpx.List[RequestOut]{}, analyticsFail(err)
	}
	out := httpx.List[RequestOut]{Data: make([]RequestOut, len(rows))}
	for i, r := range rows {
		out.Data[i] = requestOut(r)
	}
	if next != nil {
		out.NextCursor = next.Encode()
	}
	return out, nil
}

// RequestPath names a logged call.
type RequestPath struct {
	platform.ProjectPath
	Request string `path:"request" doc:"The call's trace (req_…)"`
}

// GetRequest returns one logged call.
func (h *Handlers) GetRequest(ctx *muzak.Context, in RequestPath) (RequestOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	r, err := h.Analytics.Request(ctx.Context(), pa.Project.ID, in.Request)
	if err != nil {
		return RequestOut{}, analyticsFail(err)
	}
	return requestOut(r), nil
}

// SpendIn asks for a project's spend.
type SpendIn struct {
	platform.ProjectPath
	TimeRange
	GroupBy  string `query:"group_by" doc:"model, provider, key, route, developer or tag; omit for totals"`
	Interval string `query:"interval" default:"day" doc:"hour or day"`
}

// Validate implements muzak.Validatable.
func (in *SpendIn) Validate(v *muzak.Validation) {
	v.String(&in.Interval).OneOf("hour", "day")
}

// SumsOut are calls, errors, tokens and spend.
type SumsOut struct {
	Key          string `json:"key,omitempty" doc:"The group (a key's key_… ID when grouped by key)"`
	Requests     uint64 `json:"requests"`
	Errors       uint64 `json:"errors"`
	InputTokens  uint64 `json:"input_tokens"`
	OutputTokens uint64 `json:"output_tokens"`
	CostMicros   int64  `json:"cost_micros"`
}

// PointOut is one interval of the spend series.
type PointOut struct {
	Time       time.Time `json:"time"`
	Requests   uint64    `json:"requests"`
	Errors     uint64    `json:"errors"`
	CostMicros int64     `json:"cost_micros"`
}

// SpendOut is a project's spend over a range.
type SpendOut struct {
	From   time.Time  `json:"from"`
	To     time.Time  `json:"to"`
	Total  SumsOut    `json:"total"`
	Groups []SumsOut  `json:"groups" doc:"At most 100, by spend"`
	Series []PointOut `json:"series"`
}

func sumsOut(g analytics.Group, groupBy string) SumsOut {
	key := g.Key
	if groupBy == "key" {
		if id, err := uuid.Parse(key); err == nil {
			key = ids.Encode(ids.APIKey, id)
		}
	}
	return SumsOut{Key: key, Requests: g.Requests, Errors: g.Errors, InputTokens: g.InputTokens, OutputTokens: g.OutputTokens,
		CostMicros: g.CostMicros}
}

// GetSpend sums a project's calls and spend over a range.
func (h *Handlers) GetSpend(ctx *muzak.Context, in SpendIn) (SpendOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return SpendOut{}, err
	}
	s, err := h.Analytics.Spend(ctx.Context(), pa.Project.ID, from, to, in.GroupBy, in.Interval)
	if err != nil {
		return SpendOut{}, analyticsFail(err)
	}
	out := SpendOut{From: from, To: to, Total: sumsOut(s.Total, ""), Groups: make([]SumsOut, len(s.Groups)),
		Series: make([]PointOut, len(s.Series))}
	for i, g := range s.Groups {
		out.Groups[i] = sumsOut(g, in.GroupBy)
	}
	for i, p := range s.Series {
		out.Series[i] = PointOut{Time: p.T.UTC(), Requests: p.Requests, Errors: p.Errors, CostMicros: p.CostMicros}
	}
	return out, nil
}

// LatencyIn asks for latency percentiles.
type LatencyIn struct {
	platform.ProjectPath
	TimeRange
	GroupBy string `query:"group_by" default:"model" doc:"model or provider"`
}

// PercentilesOut are p50, p95 and p99 in milliseconds.
type PercentilesOut struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

// LatencyOut is a group's latency.
type LatencyOut struct {
	Key      string         `json:"key"`
	Requests uint64         `json:"requests"`
	Latency  PercentilesOut `json:"latency_ms"`
	TTFB     PercentilesOut `json:"ttfb_ms" doc:"Time to the first byte the client got"`
}

func percentiles(qs []float32) PercentilesOut {
	at := func(i int) float64 {
		if i >= len(qs) || math.IsNaN(float64(qs[i])) {
			return 0
		}
		return math.Round(float64(qs[i]))
	}
	return PercentilesOut{P50: at(0), P95: at(1), P99: at(2)}
}

// GetLatency returns latency percentiles per model or provider.
func (h *Handlers) GetLatency(ctx *muzak.Context, in LatencyIn) (httpx.List[LatencyOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(7 * 24 * time.Hour)
	if err != nil {
		return httpx.List[LatencyOut]{}, err
	}
	ls, err := h.Analytics.Latency(ctx.Context(), pa.Project.ID, from, to, in.GroupBy)
	if err != nil {
		return httpx.List[LatencyOut]{}, analyticsFail(err)
	}
	out := httpx.List[LatencyOut]{Data: make([]LatencyOut, len(ls))}
	for i, l := range ls {
		out.Data[i] = LatencyOut{Key: l.Key, Requests: l.Requests, Latency: percentiles(l.Total), TTFB: percentiles(l.TTFB)}
	}
	return out, nil
}

func analyticsFail(err error) error {
	switch {
	case errors.Is(err, analytics.ErrNotFound):
		return httpx.NotFound("request")
	case errors.Is(err, analytics.ErrRange):
		return muzak.UnprocessableEntity("the time range must start before it ends and cover at most 31 days of the " +
			"request log or 400 days of analytics").WithCode(CodeInvalidQuery)
	case errors.Is(err, analytics.ErrCursor):
		return muzak.UnprocessableEntity("the cursor is invalid").WithCode(CodeInvalidQuery)
	case errors.Is(err, analytics.ErrGroup):
		return muzak.UnprocessableEntity("unknown group_by").WithCode(CodeInvalidQuery)
	}
	return err
}

// SavingsIn asks for a project's savings.
type SavingsIn struct {
	platform.ProjectPath
	TimeRange
}

// SavingsSumsOut are calls' counterfactual cost, actual cost and savings.
type SavingsSumsOut struct {
	Key                  string `json:"key,omitempty" doc:"The reason or family"`
	Requests             uint64 `json:"requests"`
	CounterfactualMicros int64  `json:"counterfactual_micros" doc:"On the model asked for at list price, without any cache"`
	CostMicros           int64  `json:"cost_micros"`
	SavingsMicros        int64  `json:"savings_micros" doc:"Counterfactual minus cost: negative when a fallback or a cache write cost more"`
}

// SavingsPointOut is one day of savings.
type SavingsPointOut struct {
	Time          time.Time `json:"time"`
	CostMicros    int64     `json:"cost_micros"`
	SavingsMicros int64     `json:"savings_micros"`
}

// SavingsOut is a project's savings ledger over a range.
type SavingsOut struct {
	From     time.Time         `json:"from"`
	To       time.Time         `json:"to"`
	Total    SavingsSumsOut    `json:"total"`
	ByReason []SavingsSumsOut  `json:"by_reason" doc:"response_cache, routing, fallback, prompt_cache, or empty for calls nothing changed"`
	Families []SavingsSumsOut  `json:"families" doc:"The 20 families that saved most"`
	Series   []SavingsPointOut `json:"series" doc:"Per day"`
}

func savingsSums(s analytics.SavingsSums) SavingsSumsOut {
	return SavingsSumsOut{Key: s.Key, Requests: s.Requests, CounterfactualMicros: s.Counterfactual, CostMicros: s.Cost,
		SavingsMicros: s.Savings}
}

// GetSavings returns a project's savings ledger over a range.
func (h *Handlers) GetSavings(ctx *muzak.Context, in SavingsIn) (SavingsOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	from, to, err := in.bounds(30 * 24 * time.Hour)
	if err != nil {
		return SavingsOut{}, err
	}
	r, err := h.Analytics.Savings(ctx.Context(), pa.Project.ID, from, to)
	if err != nil {
		return SavingsOut{}, analyticsFail(err)
	}
	out := SavingsOut{From: from, To: to, Total: savingsSums(r.Total), ByReason: make([]SavingsSumsOut, len(r.ByReason)),
		Families: make([]SavingsSumsOut, len(r.Families)), Series: make([]SavingsPointOut, len(r.Series))}
	for i, s := range r.ByReason {
		out.ByReason[i] = savingsSums(s)
	}
	for i, s := range r.Families {
		out.Families[i] = savingsSums(s)
	}
	for i, p := range r.Series {
		out.Series[i] = SavingsPointOut{Time: p.T.UTC(), CostMicros: p.Cost, SavingsMicros: p.Savings}
	}
	return out, nil
}
