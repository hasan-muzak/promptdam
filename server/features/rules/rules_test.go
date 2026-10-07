package rules_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/llm"
	"github.com/hasan-muzak/keel-core/pkg/policy"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/features/prices"
	"github.com/hasan-muzak/promptdam/server/features/rules"
	"github.com/hasan-muzak/promptdam/server/migrations"
)

func env(t *testing.T) *policy.Env {
	t.Helper()
	e, err := rules.NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMatch(t *testing.T) {
	e := env(t)
	rs := []rules.Rule{
		{Name: "broken", When: `request.messages / 0 > 1`, ToModel: "x"},
		{Name: "support", Route: "support", When: `true`, ToModel: "support-model"},
		{Name: "short", When: `request.input_tokens < 1500 && !request.has_tools`, ToModel: "small"},
		{Name: "tagged", When: `"batch" in request.tags`, ToModel: "batch-model"},
	}
	for _, tc := range []struct {
		name  string
		route string
		req   rules.Request
		want  string
	}{
		{"short call", "default", rules.Request{InputTokens: 300}, "short"},
		{"tools", "default", rules.Request{InputTokens: 300, HasTools: true}, ""},
		{"long", "default", rules.Request{InputTokens: 9000}, ""},
		{"the route's rule", "support", rules.Request{InputTokens: 300}, "support"},
		{"tags", "default", rules.Request{InputTokens: 9000, Tags: []string{"batch"}}, "tagged"},
	} {
		got, ok := rules.Match(e, rs, tc.route, tc.req)
		if got.Name != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: %q %v", tc.name, got.Name, ok)
		}
	}
}

func TestCheck(t *testing.T) {
	s := rules.New(nil, env(t), nil)
	in := rules.Input{Name: "short", When: "request.input_tokens <\n  1500 &&", ToModel: "small", Status: rules.Draft}
	var bad *rules.InvalidError
	if err := s.Check(&in); !errors.As(err, &bad) || len(bad.Problems) == 0 || bad.Problems[0].Line != 2 {
		t.Fatalf("%v %+v", err, bad)
	}
	// Typed: a string isn't a condition, a misspelt field or a wrong type is
	// refused.
	for _, when := range []string{`request.model`, `request.input_token < 10`, `request.input_tokens < "10"`} {
		in.When = when
		if err := s.Check(&in); !errors.As(err, &bad) {
			t.Errorf("%s: %v", when, err)
		}
	}
	for _, mod := range []func(*rules.Input){
		func(in *rules.Input) { in.Name = "" },
		func(in *rules.Input) { in.ToModel = " " },
		func(in *rules.Input) { in.Status = "on" },
		func(in *rules.Input) { in.Priority = 5000 },
	} {
		in := rules.Input{Name: "short", When: "true", ToModel: "small", Status: rules.Active}
		mod(&in)
		if err := s.Check(&in); !errors.As(err, &bad) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	in = rules.Input{Name: " short ", When: "request.stream", ToModel: " small ", Status: rules.Active}
	if err := s.Check(&in); err != nil || in.Name != "short" || in.ToModel != "small" {
		t.Fatalf("%v %+v", err, in)
	}
}

// 100 short calls without tools and 50 long ones with tools, all on a
// model at $10 / $30 a million tokens; a rule sends the short ones to a
// model at $0.10 / $0.40.
func TestTry(t *testing.T) {
	ctx := context.Background()
	db := testkit.ClickHouse(t, migrations.ClickHouse())
	project := uuid.New()
	insert := func(n, est int, tools bool, route string, input, output uint32, cost int64) {
		t.Helper()
		err := db.Unscoped().Exec(ctx, `INSERT INTO requests (project_id, ts, trace, route, api_format, requested_model,
			est_input_tokens, messages, has_tools, usage_known, input_tokens, output_tokens, cost_micros, retention_days)
			SELECT ?, now64(3) - toIntervalSecond(number), concat(?, toString(number)), ?, 'openai.chat', 'big', ?, 2, ?,
			true, ?, ?, ?, 7 FROM numbers(?)`, project, route, route, uint32(est), tools, input, output, cost, n)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(100, 500, false, "default", 520, 100, 8_200) // 520 × $10 + 100 × $30
	insert(50, 6000, true, "default", 6100, 400, 73_000)

	list := prices.Static{Catalog: &llm.Catalog{Models: []llm.Model{
		{ID: "big", Input: 10, Output: 30}, {ID: "small", Input: 0.1, Output: 0.4},
	}}}
	e := env(t)
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now.Add(time.Minute)
	short := rules.Rule{Name: "short", When: `request.input_tokens < 1500 && !request.has_tools`, ToModel: "small"}

	d, err := rules.Try(ctx, db, e, list, nil, project, short, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// 520 × $0.10 + 100 × $0.40 = 92 micros a call.
	if d.Calls != 150 || d.Matched != 100 || d.Unpriced != 0 || d.CostMicros != 820_000 || d.EstimatedMicros != 9_200 ||
		d.SavingsMicros != 810_800 {
		t.Fatalf("%+v", d)
	}

	// The project's own price wins over the list's.
	own := prices.ProjectPrices([]prices.Override{{Model: "small", Input: 1, Output: 4}})
	if d, err := rules.Try(ctx, db, e, list, own, project, short, from, to); err != nil || d.EstimatedMicros != 92_000 {
		t.Fatalf("own prices: %+v %v", d, err)
	}
	// A model without a price: matched, not summed.
	unknown := short
	unknown.ToModel = "mystery"
	if d, err := rules.Try(ctx, db, e, list, nil, project, unknown, from, to); err != nil || d.Matched != 100 ||
		d.Unpriced != 100 || d.CostMicros != 0 {
		t.Fatalf("unpriced: %+v %v", d, err)
	}
	// Another route's rule matches nothing here.
	other := short
	other.Route = "support"
	if d, err := rules.Try(ctx, db, e, list, nil, project, other, from, to); err != nil || d.Matched != 0 || d.Calls != 150 {
		t.Fatalf("other route: %+v %v", d, err)
	}
	// Another project's calls aren't read.
	if d, err := rules.Try(ctx, db, e, list, nil, uuid.New(), short, from, to); err != nil || d.Calls != 0 {
		t.Fatalf("other project: %+v %v", d, err)
	}
	if _, err := rules.Try(ctx, db, e, list, nil, project, short, to, from); !errors.Is(err, rules.ErrRange) {
		t.Fatalf("range: %v", err)
	}
	var ce *policy.CompileError
	if _, err := rules.Try(ctx, db, e, list, nil, project, rules.Rule{When: "request.", ToModel: "small"}, from, to); !errors.As(err, &ce) {
		t.Fatalf("compile: %v", err)
	}
}
