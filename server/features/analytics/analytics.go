// Package analytics answers questions about a project's gateway calls from
// ClickHouse: the request log (newest first, filtered, cursor-paged, or one
// call by its trace) and spend and latency from the hourly rollups, which
// raw rows are never scanned for.
package analytics

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/events"
	"github.com/hasan-muzak/keel-core/pkg/ids"
)

// TracePrefix starts call IDs (the gateway's traces): req_….
const TracePrefix = "req"

// Limits on what one query may cover.
const (
	// MaxLogRange bounds a request log query's time range.
	MaxLogRange = 31 * 24 * time.Hour
	// MaxRange bounds an analytics query's: rollups are small.
	MaxRange = 400 * 24 * time.Hour
)

// Errors.
var (
	ErrNotFound = errors.New("analytics: not found")
	ErrRange    = errors.New("analytics: the time range is invalid or too long")
	ErrCursor   = errors.New("analytics: the cursor is invalid")
	ErrGroup    = errors.New("analytics: unknown grouping")
)

// Service queries ClickHouse.
type Service struct {
	db *events.DB
}

// New returns a Service; db may be nil (no ClickHouse: nothing to show).
func New(db *events.DB) *Service { return &Service{db: db} }

// Request is one logged call.
type Request struct {
	TS               time.Time `ch:"ts"`
	Trace            string    `ch:"trace"`
	KeyID            uuid.UUID `ch:"key_id"`
	Developer        string    `ch:"developer"`
	User             string    `ch:"end_user"`
	Tags             []string  `ch:"tags"`
	Route            string    `ch:"route"`
	APIFormat        string    `ch:"api_format"`
	Provider         string    `ch:"provider"`
	RequestedModel   string    `ch:"requested_model"`
	ServedModel      string    `ch:"served_model"`
	Stream           bool      `ch:"stream"`
	Status           uint16    `ch:"status"`
	ErrorCode        string    `ch:"error_code"`
	Attempts         uint8     `ch:"attempts"`
	TTFBms           uint32    `ch:"ttfb_ms"`
	LatencyMs        uint32    `ch:"latency_ms"`
	UpstreamMs       uint32    `ch:"upstream_ms"`
	UsageKnown       bool      `ch:"usage_known"`
	InputTokens      uint32    `ch:"input_tokens"`
	OutputTokens     uint32    `ch:"output_tokens"`
	CacheReadTokens  uint32    `ch:"cache_read_tokens"`
	CacheWriteTokens uint32    `ch:"cache_write_tokens"`
	ReasoningTokens  uint32    `ch:"reasoning_tokens"`
	CostMicros       int64     `ch:"cost_micros"`
	Savings          int64     `ch:"savings_micros"`
	SavingsReason    string    `ch:"savings_reason"`
	Cache            string    `ch:"cache"`
	Family           string    `ch:"family_id"`
}

const requestColumns = `ts, trace, key_id, developer, end_user, tags, route, api_format, provider, requested_model,
	served_model, stream, status, error_code, attempts, ttfb_ms, latency_ms, upstream_ms, usage_known, input_tokens,
	output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, cost_micros, savings_micros,
	savings_reason, cache, family_id`

// Filter narrows the request log.
type Filter struct {
	From, To  time.Time
	Model     string
	Provider  string
	Route     string
	Key       uuid.UUID
	Developer string
	// Errors keeps only failed calls.
	Errors bool
}

func checkRange(from, to time.Time, most time.Duration) error {
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > most {
		return ErrRange
	}
	return nil
}

// Cursor is where a page of the request log ends.
type Cursor struct {
	TS    time.Time
	Trace string
}

// Encode makes a cursor opaque.
func (c Cursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.TS.UnixMilli(), 10) + ":" + c.Trace))
}

// DecodeCursor reads an opaque cursor.
func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, ErrCursor
	}
	ms, trace, ok := strings.Cut(string(raw), ":")
	n, err := strconv.ParseInt(ms, 10, 64)
	if !ok || err != nil || len(trace) > 100 {
		return Cursor{}, ErrCursor
	}
	return Cursor{TS: time.UnixMilli(n).UTC(), Trace: trace}, nil
}

// Requests returns up to limit calls of a project, newest first, after the
// cursor; the next cursor is set when more follow.
func (s *Service) Requests(ctx context.Context, project uuid.UUID, f Filter, after *Cursor, limit int) ([]Request, *Cursor, error) {
	if err := checkRange(f.From, f.To, MaxLogRange); err != nil {
		return nil, nil, err
	}
	if s.db == nil {
		return []Request{}, nil, nil
	}
	q := strings.Builder{}
	q.WriteString(`SELECT ` + requestColumns + ` FROM requests
		WHERE {tenant} AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?)`)
	args := []any{f.From.UnixMilli(), f.To.UnixMilli()}
	if after != nil {
		q.WriteString(` AND (ts, trace) < (fromUnixTimestamp64Milli(?), ?)`)
		args = append(args, after.TS.UnixMilli(), after.Trace)
	}
	for _, c := range []struct {
		column, value string
	}{{"served_model", f.Model}, {"provider", f.Provider}, {"route", f.Route}, {"developer", f.Developer}} {
		if c.value != "" {
			q.WriteString(" AND " + c.column + " = ?")
			args = append(args, c.value)
		}
	}
	if f.Key != uuid.Nil {
		q.WriteString(" AND key_id = ?")
		args = append(args, f.Key)
	}
	if f.Errors {
		q.WriteString(" AND error_code != ''")
	}
	q.WriteString(" ORDER BY ts DESC, trace DESC LIMIT ?")
	args = append(args, limit+1)

	var out []Request
	if err := s.db.Select(ctx, &out, events.Project(project), q.String(), args...); err != nil {
		return nil, nil, fmt.Errorf("analytics: requests: %w", err)
	}
	var next *Cursor
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = &Cursor{TS: last.TS, Trace: last.Trace}
	}
	if out == nil {
		out = []Request{}
	}
	return out, next, nil
}

// Request returns one call by its trace. The trace's TypeID holds the
// call's start, so only a moment of the project's rows is read.
func (s *Service) Request(ctx context.Context, project uuid.UUID, trace string) (Request, error) {
	id, err := ids.Decode(TracePrefix, trace)
	if err != nil || s.db == nil {
		return Request{}, ErrNotFound
	}
	sec, nsec := id.Time().UnixTime()
	start := time.Unix(sec, nsec)
	var out []Request
	err = s.db.Select(ctx, &out, events.Project(project), `SELECT `+requestColumns+` FROM requests
		WHERE {tenant} AND ts >= fromUnixTimestamp64Milli(?) AND ts <= fromUnixTimestamp64Milli(?) AND trace = ?
		LIMIT 1`, start.Add(-time.Second).UnixMilli(), start.Add(time.Second).UnixMilli(), trace)
	if err != nil {
		return Request{}, fmt.Errorf("analytics: request: %w", err)
	}
	if len(out) == 0 {
		return Request{}, ErrNotFound
	}
	return out[0], nil
}

// Group is one group's sums over a range.
type Group struct {
	Key          string `ch:"grp"`
	Requests     uint64 `ch:"requests"`
	Errors       uint64 `ch:"errors"`
	InputTokens  uint64 `ch:"input_tokens"`
	OutputTokens uint64 `ch:"output_tokens"`
	CostMicros   int64  `ch:"cost_micros"`
}

// Point is one interval of a series.
type Point struct {
	T          time.Time `ch:"t"`
	Requests   uint64    `ch:"requests"`
	Errors     uint64    `ch:"errors"`
	CostMicros int64     `ch:"cost_micros"`
}

// Spend is a project's calls and spend over a range: in total, per group
// and per interval.
type Spend struct {
	Total  Group
	Groups []Group
	Series []Point
}

// groupings map a grouping to its rollup table and column.
var groupings = map[string][2]string{
	"":          {"requests_hourly", "''"},
	"model":     {"requests_hourly", "served_model"},
	"provider":  {"requests_hourly", "provider"},
	"key":       {"requests_hourly", "toString(key_id)"},
	"route":     {"requests_hourly", "route"},
	"developer": {"requests_hourly_developer", "developer"},
	"tag":       {"requests_hourly_tag", "tag"},
}

// Groupings lists the ways spend can be grouped.
func Groupings() []string { return []string{"model", "provider", "key", "route", "developer", "tag"} }

// Spend sums a project's calls over [from, to) from the hourly rollups,
// grouped by groupBy ("" for none; at most 100 groups, by spend) and per
// interval (hour or day).
func (s *Service) Spend(ctx context.Context, project uuid.UUID, from, to time.Time, groupBy, interval string) (Spend, error) {
	g, ok := groupings[groupBy]
	if !ok {
		return Spend{}, ErrGroup
	}
	if err := checkRange(from, to, MaxRange); err != nil {
		return Spend{}, err
	}
	step := "1 HOUR"
	if interval == "day" {
		step = "1 DAY"
	}
	out := Spend{Groups: []Group{}, Series: []Point{}}
	if s.db == nil {
		return out, nil
	}
	table, column := g[0], g[1]
	where := ` FROM ` + table + ` WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < fromUnixTimestamp64Milli(?)`
	args := []any{from.UnixMilli(), to.UnixMilli()}
	sums := `sum(requests) AS requests, sum(errors) AS errors, sum(input_tokens) AS input_tokens,
		sum(output_tokens) AS output_tokens, sum(cost_micros) AS cost_micros`

	var total []Group
	if err := s.db.Select(ctx, &total, events.Project(project), `SELECT '' AS grp, `+sums+
		` FROM requests_hourly WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < fromUnixTimestamp64Milli(?)`,
		args...); err != nil {
		return Spend{}, fmt.Errorf("analytics: spend total: %w", err)
	}
	if len(total) > 0 {
		out.Total = total[0]
	}
	if groupBy != "" {
		if err := s.db.Select(ctx, &out.Groups, events.Project(project), `SELECT `+column+` AS grp, `+sums+where+
			` GROUP BY grp ORDER BY cost_micros DESC, requests DESC LIMIT 100`, args...); err != nil {
			return Spend{}, fmt.Errorf("analytics: spend groups: %w", err)
		}
	}
	if err := s.db.Select(ctx, &out.Series, events.Project(project), `SELECT toStartOfInterval(hour, INTERVAL `+step+`) AS t,
		sum(requests) AS requests, sum(errors) AS errors, sum(cost_micros) AS cost_micros FROM requests_hourly
		WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < fromUnixTimestamp64Milli(?)
		GROUP BY t ORDER BY t`, args...); err != nil {
		return Spend{}, fmt.Errorf("analytics: spend series: %w", err)
	}
	if out.Groups == nil {
		out.Groups = []Group{}
	}
	if out.Series == nil {
		out.Series = []Point{}
	}
	return out, nil
}

// Latency is a group's latency percentiles (p50, p95, p99), in ms.
type Latency struct {
	Key      string    `ch:"grp"`
	Requests uint64    `ch:"requests"`
	Total    []float32 `ch:"latency"`
	TTFB     []float32 `ch:"ttfb"`
}

// Latency returns latency and time-to-first-byte percentiles over [from,
// to) per model or provider, from the hourly rollups.
func (s *Service) Latency(ctx context.Context, project uuid.UUID, from, to time.Time, groupBy string) ([]Latency, error) {
	column := map[string]string{"model": "served_model", "provider": "provider", "": "''"}[groupBy]
	if column == "" {
		return nil, ErrGroup
	}
	if err := checkRange(from, to, MaxRange); err != nil {
		return nil, err
	}
	out := []Latency{}
	if s.db == nil {
		return out, nil
	}
	err := s.db.Select(ctx, &out, events.Project(project), `SELECT `+column+` AS grp, sum(requests) AS requests,
		quantilesTDigestMerge(0.5, 0.95, 0.99)(latency_ms) AS latency, quantilesTDigestMerge(0.5, 0.95, 0.99)(ttfb_ms) AS ttfb
		FROM requests_hourly
		WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < fromUnixTimestamp64Milli(?)
		GROUP BY grp ORDER BY requests DESC LIMIT 100`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("analytics: latency: %w", err)
	}
	return out, nil
}

// SavingsSums are calls' counterfactual cost (the model asked for at list
// price, without any cache), what they cost, and the difference.
type SavingsSums struct {
	Key            string `ch:"grp"`
	Requests       uint64 `ch:"requests"`
	Counterfactual int64  `ch:"counterfactual_micros"`
	Cost           int64  `ch:"cost_micros"`
	Savings        int64  `ch:"savings_micros"`
}

// SavingsPoint is one interval of savings.
type SavingsPoint struct {
	T       time.Time `ch:"t"`
	Cost    int64     `ch:"cost_micros"`
	Savings int64     `ch:"savings_micros"`
}

// SavingsReport is a project's savings ledger over a range.
type SavingsReport struct {
	Total    SavingsSums
	ByReason []SavingsSums
	Families []SavingsSums
	Series   []SavingsPoint
}

// Savings sums a project's savings ledger over [from, to) from the hourly
// rollup: in total, per reason, for the 20 families that saved most, and
// per day.
func (s *Service) Savings(ctx context.Context, project uuid.UUID, from, to time.Time) (SavingsReport, error) {
	if err := checkRange(from, to, MaxRange); err != nil {
		return SavingsReport{}, err
	}
	out := SavingsReport{ByReason: []SavingsSums{}, Families: []SavingsSums{}, Series: []SavingsPoint{}}
	if s.db == nil {
		return out, nil
	}
	sums := `sum(requests) AS requests, sum(counterfactual_micros) AS counterfactual_micros,
		sum(cost_micros) AS cost_micros, sum(savings_micros) AS savings_micros`
	where := ` FROM savings_hourly WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < fromUnixTimestamp64Milli(?)`
	args := []any{from.UnixMilli(), to.UnixMilli()}
	var total []SavingsSums
	if err := s.db.Select(ctx, &total, events.Project(project), `SELECT '' AS grp, `+sums+where, args...); err != nil {
		return SavingsReport{}, fmt.Errorf("analytics: savings: %w", err)
	}
	if len(total) > 0 {
		out.Total = total[0]
	}
	if err := s.db.Select(ctx, &out.ByReason, events.Project(project), `SELECT savings_reason AS grp, `+sums+where+
		` GROUP BY grp ORDER BY savings_micros DESC`, args...); err != nil {
		return SavingsReport{}, fmt.Errorf("analytics: savings by reason: %w", err)
	}
	if err := s.db.Select(ctx, &out.Families, events.Project(project), `SELECT family_id AS grp, `+sums+where+
		` AND family_id != '' GROUP BY grp ORDER BY savings_micros DESC LIMIT 20`, args...); err != nil {
		return SavingsReport{}, fmt.Errorf("analytics: savings by family: %w", err)
	}
	if err := s.db.Select(ctx, &out.Series, events.Project(project), `SELECT toStartOfDay(hour) AS t,
		sum(cost_micros) AS cost_micros, sum(savings_micros) AS savings_micros`+where+` GROUP BY t ORDER BY t`, args...); err != nil {
		return SavingsReport{}, fmt.Errorf("analytics: savings series: %w", err)
	}
	return out, nil
}
