package rules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/events"
	"github.com/hasan-muzak/keel/pkg/llm"
	"github.com/hasan-muzak/keel/pkg/policy"

	"github.com/hasan-muzak/keel/products/promptdam/server/features/prices"
)

// DryRunLimit bounds the calls a dry-run reads: the most recent of its
// range.
const DryRunLimit = 20_000

// MaxDryRunRange bounds a dry-run's range.
const MaxDryRunRange = 31 * 24 * time.Hour

// ErrRange refuses a range that is invalid or too long.
var ErrRange = errors.New("rules: the time range is invalid or too long")

// DryRun is what a rule would have done to recent calls, had it been
// active: how many it would have routed, what they cost, and what they
// would have cost on its model with the same tokens. It says nothing of
// the answers' quality: that takes trying the model on them.
type DryRun struct {
	// Calls is the calls read (at most DryRunLimit), Matched those the
	// rule holds for.
	Calls, Matched uint64
	// Unpriced counts matched calls whose cost on the rule's model is
	// unknown: they are left out of the sums.
	Unpriced uint64
	// CostMicros is what the matched calls cost; EstimatedMicros what they
	// would have cost on the rule's model.
	CostMicros, EstimatedMicros, SavingsMicros int64
}

type callRow struct {
	Model      string   `ch:"requested_model"`
	Format     string   `ch:"api_format"`
	EstInput   uint32   `ch:"est_input_tokens"`
	Messages   uint32   `ch:"messages"`
	MaxTokens  uint32   `ch:"max_tokens"`
	HasTools   bool     `ch:"has_tools"`
	JSON       bool     `ch:"json_output"`
	Stream     bool     `ch:"stream"`
	Family     string   `ch:"family_id"`
	Tags       []string `ch:"tags"`
	User       string   `ch:"end_user"`
	Developer  string   `ch:"developer"`
	Route      string   `ch:"route"`
	Input      uint32   `ch:"input_tokens"`
	Output     uint32   `ch:"output_tokens"`
	CacheRead  uint32   `ch:"cache_read_tokens"`
	CacheWrite uint32   `ch:"cache_write_tokens"`
	CostMicros int64    `ch:"cost_micros"`
}

// Try runs a rule (saved or not) over the project's calls in [from, to):
// the most recent DryRunLimit of them. own are the project's prices.
func Try(ctx context.Context, db *events.DB, env *policy.Env, list prices.Prices, own prices.Project,
	project uuid.UUID, rule Rule, from, to time.Time) (DryRun, error) {
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > MaxDryRunRange {
		return DryRun{}, ErrRange
	}
	if _, err := env.Compile(rule.When); err != nil {
		return DryRun{}, err
	}
	var out DryRun
	if db == nil {
		return out, nil
	}
	var rows []callRow
	err := db.Select(ctx, &rows, events.Project(project), `SELECT requested_model, api_format, est_input_tokens,
		messages, max_tokens, has_tools, json_output, stream, family_id, tags, end_user, developer, route,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros
		FROM requests
		WHERE {tenant} AND ts >= fromUnixTimestamp64Milli(?) AND ts < fromUnixTimestamp64Milli(?) AND usage_known
		ORDER BY ts DESC LIMIT ?`, from.UnixMilli(), to.UnixMilli(), DryRunLimit)
	if err != nil {
		return DryRun{}, fmt.Errorf("rules: dry-run: %w", err)
	}
	model := rule.ToModel
	provider := ""
	if p, m, ok := strings.Cut(model, "/"); ok {
		provider, model = p, m
	}
	price, priced := own.Lookup(provider, model)
	if !priced {
		price, priced = list.Current().Lookup("", rule.ToModel)
		if !priced {
			price, priced = list.Current().Lookup("", model)
		}
	}
	for _, r := range rows {
		out.Calls++
		req := Request{Model: r.Model, Format: r.Format, InputTokens: int(r.EstInput), Messages: int(r.Messages),
			MaxTokens: int(r.MaxTokens), HasTools: r.HasTools, JSON: r.JSON, Stream: r.Stream, Family: r.Family,
			Tags: r.Tags, User: r.User, Developer: r.Developer, Route: r.Route}
		if _, ok := Match(env, []Rule{rule}, r.Route, req); !ok {
			continue
		}
		out.Matched++
		if !priced {
			out.Unpriced++
			continue
		}
		out.CostMicros += r.CostMicros
		out.EstimatedMicros += int64(price.CostMicros(llm.Usage{Input: uint64(r.Input), CacheRead: uint64(r.CacheRead),
			CacheWrite: uint64(r.CacheWrite), Output: uint64(r.Output)}))
	}
	out.SavingsMicros = out.CostMicros - out.EstimatedMicros
	return out, nil
}
