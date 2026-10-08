package purge_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel-core/pkg/config"
	"github.com/hasan-muzak/keel-core/pkg/entx"
	"github.com/hasan-muzak/keel-core/pkg/identity"
	"github.com/hasan-muzak/keel-core/pkg/jobs"
	"github.com/hasan-muzak/keel-core/pkg/keel"
	"github.com/hasan-muzak/keel-core/pkg/testkit"

	"github.com/hasan-muzak/promptdam/server/core"
	pdent "github.com/hasan-muzak/promptdam/server/ent"
	"github.com/hasan-muzak/promptdam/server/ent/activity"
	"github.com/hasan-muzak/promptdam/server/ent/alertrule"
	"github.com/hasan-muzak/promptdam/server/ent/budget"
	"github.com/hasan-muzak/promptdam/server/ent/capturesetting"
	"github.com/hasan-muzak/promptdam/server/ent/familylabel"
	"github.com/hasan-muzak/promptdam/server/ent/firewallsetting"
	"github.com/hasan-muzak/promptdam/server/ent/keysetting"
	"github.com/hasan-muzak/promptdam/server/ent/modelrouter"
	"github.com/hasan-muzak/promptdam/server/ent/priceoverride"
	"github.com/hasan-muzak/promptdam/server/ent/provider"
	"github.com/hasan-muzak/promptdam/server/ent/route"
	"github.com/hasan-muzak/promptdam/server/ent/routerclassifier"
	"github.com/hasan-muzak/promptdam/server/ent/routingrule"
	"github.com/hasan-muzak/promptdam/server/features/classifier"
	"github.com/hasan-muzak/promptdam/server/features/purge"
	"github.com/hasan-muzak/promptdam/server/features/routers/tiers"
	"github.com/hasan-muzak/promptdam/server/migrations"
)

type world struct {
	t   *testing.T
	ctx context.Context
	c   *keel.Core
	app *core.App
}

func setup(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	d := testkit.Postgres(t, migrations.Postgres())
	if err := jobs.Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	cfg := keel.Config{Base: config.Base{Env: config.Development, APIKeyPepper: strings.Repeat("p", 32)}, MailURL: "smtp://localhost:1025"}
	c, err := keel.New(ctx, cfg, core.Product, d, testkit.Valkey(t))
	if err != nil {
		t.Fatal(err)
	}
	c.Events = testkit.ClickHouse(t, migrations.ClickHouse())
	return &world{t: t, ctx: ctx, c: c, app: core.NewApp(c)}
}

type tenant struct {
	org      uuid.UUID
	projects []uuid.UUID
}

func (w *world) tenant(name string, projects ...string) tenant {
	w.t.Helper()
	u, err := w.c.Tenancy.SignIn(w.ctx, identity.Claims{UID: "fb-" + name, Email: name + "@example.com", EmailVerified: true})
	if err != nil {
		w.t.Fatal(err)
	}
	org, err := w.c.Tenancy.CreateOrg(w.ctx, u.ID, name, "")
	if err != nil {
		w.t.Fatal(err)
	}
	tn := tenant{org: org.ID}
	for _, p := range projects {
		pr, err := w.c.Tenancy.CreateProject(w.ctx, org.ID, p, "")
		if err != nil {
			w.t.Fatal(err)
		}
		tn.projects = append(tn.projects, pr.ID)
	}
	return tn
}

// seed is one entity of Promptdam's graph and how to create a row of it.
type seed struct {
	name, table string
	create      func(ctx context.Context, org, project uuid.UUID) error
}

func seeds(c *pdent.Client) []seed {
	return []seed{
		{"Activity", activity.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.Activity.Create().SetOrgID(org).SetProjectID(p).SetKind("router.ready").SetTitle("x").Exec(ctx)
		}},
		{"AlertRule", alertrule.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.AlertRule.Create().SetOrgID(org).SetProjectID(p).SetName("errors").SetKind(alertrule.KindErrorRate).
				SetThreshold(0.1).SetWindowMinutes(15).Exec(ctx)
		}},
		{"CaptureSetting", capturesetting.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.CaptureSetting.Create().SetOrgID(org).SetProjectID(p).SetEnabled(true).Exec(ctx)
		}},
		{"Budget", budget.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.Budget.Create().SetOrgID(org).SetProjectID(p).SetName("month").SetScope(budget.ScopeProject).
				SetWindow(budget.WindowMonth).SetLimitMicros(1_000_000).Exec(ctx)
		}},
		{"FamilyLabel", familylabel.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.FamilyLabel.Create().SetOrgID(org).SetProjectID(p).SetFamilyID("0123456789abcdef").SetLabel("x").Exec(ctx)
		}},
		{"FirewallSetting", firewallsetting.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.FirewallSetting.Create().SetOrgID(org).SetProjectID(p).SetSettings([]byte(`{"enabled":true}`)).Exec(ctx)
		}},
		{"KeySetting", keysetting.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.KeySetting.Create().SetOrgID(org).SetProjectID(p).SetAPIKeyID(uuid.New()).Exec(ctx)
		}},
		{"Route", route.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.Route.Create().SetOrgID(org).SetProjectID(p).SetName("default").Exec(ctx)
		}},
		{"PriceOverride", priceoverride.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.PriceOverride.Create().SetOrgID(org).SetProjectID(p).SetModel("llama-3.3-70b").SetInput(0.5).
				SetCachedInput(0.5).SetCacheWrite(0.5).SetOutput(1).Exec(ctx)
		}},
		{"RoutingRule", routingrule.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.RoutingRule.Create().SetOrgID(org).SetProjectID(p).SetName("small").SetWhen("true").
				SetToModel("gpt-5-mini").Exec(ctx)
		}},
		{"ModelRouter", modelrouter.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.ModelRouter.Create().SetOrgID(org).SetProjectID(p).SetName("auto").
				SetTiers([]tiers.Tier{{Name: "fast", Model: "gpt-5-nano"}}).SetDefaultTier("fast").Exec(ctx)
		}},
		{"RouterClassifier", routerclassifier.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.RouterClassifier.Create().SetOrgID(org).SetProjectID(p).SetRouterID(uuid.New()).
				SetModel(classifier.Model{Default: "fast"}).SetTrainedAt(time.Now()).Exec(ctx)
		}},
		{"Provider", provider.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.Provider.Create().SetOrgID(org).SetProjectID(p).SetName("openai").SetKind("openai").
				SetAPIFormat(provider.APIFormatOpenai).SetBaseURL("https://api.openai.com/v1").Exec(ctx)
		}},
	}
}

// global entities are reference data, no organization's: nothing to purge.
var global = map[string]bool{"PriceList": true}

// Every entity of the graph is seeded, so a new one fails here until the
// purge (deleteRows) and this list cover it, or it is global.
func TestSeedsCoverTheGraph(t *testing.T) {
	byName := map[string]bool{}
	for _, s := range seeds(nil) {
		byName[s.name] = true
	}
	client := reflect.TypeFor[pdent.Client]()
	for i := range client.NumField() {
		f := client.Field(i)
		if f.Type.Kind() == reflect.Pointer && strings.HasSuffix(f.Type.Elem().Name(), "Client") && !byName[f.Name] &&
			!global[f.Name] {
			t.Errorf("%s is not seeded: add it here and to the purge (deleteRows)", f.Name)
		}
	}
}

func (w *world) fill(tn tenant) {
	ctx := entx.WithOrg(w.ctx, tn.org)
	for _, s := range seeds(w.app.Ent) {
		for _, p := range tn.projects {
			if err := s.create(ctx, tn.org, p); err != nil {
				w.t.Fatalf("%s: %v", s.name, err)
			}
		}
	}
	// The request log and, through its views, every rollup.
	for _, p := range tn.projects {
		err := w.c.Events.Unscoped().Exec(w.ctx, `INSERT INTO requests (project_id, ts, org_id, tags, developer, family_id,
			usage_known, retention_days) VALUES (?, now64(3), ?, ['t'], 'dev', '0123456789abcdef', true, 7)`, p, tn.org)
		if err != nil {
			w.t.Fatal(err)
		}
		if err := w.c.Events.Unscoped().Exec(w.ctx, `INSERT INTO router_evals (project_id, ts, org_id, router, retention_days)
			VALUES (?, now64(3), ?, 'auto', 7)`, p, tn.org); err != nil {
			w.t.Fatal(err)
		}
		if err := w.c.Events.Unscoped().Exec(w.ctx, `INSERT INTO firewall_events (project_id, ts, org_id, detector, retention_days)
			VALUES (?, now64(3), ?, 'email', 7)`, p, tn.org); err != nil {
			w.t.Fatal(err)
		}
	}
	// Valkey keys name the project they belong to (caches, counters).
	for _, p := range tn.projects {
		key := w.c.KV.Key("snapshot", p.String())
		if err := w.c.KV.Client.Do(w.ctx, w.c.KV.Client.B().Set().Key(key).Value("x").Build()).Error(); err != nil {
			w.t.Fatal(err)
		}
	}
}

// rows counts the rows of every table of the graph with column = id.
func (w *world) rows(column string, id uuid.UUID) int {
	w.t.Helper()
	total := 0
	for _, s := range seeds(w.app.Ent) {
		var n int
		if err := w.c.DB.SQL.QueryRowContext(w.ctx, `SELECT count(*) FROM `+s.table+` WHERE `+column+` = $1`, id).Scan(&n); err != nil {
			w.t.Fatal(err)
		}
		total += n
	}
	return total
}

// clickhouse counts a project's rows in every ClickHouse table.
func (w *world) clickhouse(project uuid.UUID) uint64 {
	w.t.Helper()
	var total uint64
	for _, table := range []string{"requests", "requests_hourly", "requests_hourly_developer", "requests_hourly_tag",
		"families_hourly", "prefixes_hourly", "savings_hourly", "router_evals",
		"firewall_events"} {
		var n uint64
		if err := w.c.Events.Unscoped().QueryRow(w.ctx, `SELECT count() FROM `+table+` WHERE project_id = ?`, project).Scan(&n); err != nil {
			w.t.Fatal(err)
		}
		total += n
	}
	return total
}

func (w *world) keys(id uuid.UUID) int {
	w.t.Helper()
	n, err := w.c.KV.Client.Do(w.ctx, w.c.KV.Client.B().Exists().Key(w.c.KV.Key("snapshot", id.String())).Build()).AsInt64()
	if err != nil {
		w.t.Fatal(err)
	}
	return int(n)
}

func (w *world) purges() int {
	w.t.Helper()
	var n int
	if err := w.c.DB.SQL.QueryRowContext(w.ctx, `SELECT count(*) FROM river_job WHERE kind = 'promptdam_purge'`).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func TestDeletingAProjectThenItsOrgLeavesNothing(t *testing.T) {
	w := setup(t)
	a := w.tenant("acme", "web", "api")
	b := w.tenant("globex", "shop")
	w.fill(a)
	w.fill(b)
	p, sibling := a.projects[0], a.projects[1]

	// The project: its rows go in the deletion's transaction, with a purge
	// and a final purge enqueued for the rest.
	if err := w.c.Tenancy.DeleteProject(w.ctx, a.org, p); err != nil {
		t.Fatal(err)
	}
	if n := w.rows("project_id", p); n != 0 {
		t.Fatalf("%d rows of the deleted project left", n)
	}
	if n := w.purges(); n != 2 {
		t.Fatalf("%d purges enqueued, want a run and a final run", n)
	}
	if err := w.app.Purge.Purge(w.ctx, purge.Args{Org: a.org, Projects: []uuid.UUID{p}, Final: true}); err != nil {
		t.Fatal(err)
	}
	if w.keys(p) != 0 {
		t.Fatal("the deleted project's Valkey keys are left")
	}
	if n := w.clickhouse(p); n != 0 {
		t.Fatalf("%d ClickHouse rows of the deleted project are left", n)
	}
	if w.clickhouse(sibling) != 9 { // a row in each table
		t.Fatal("the purge took ClickHouse rows of a project that wasn't deleted")
	}
	if w.rows("project_id", sibling) == 0 || w.keys(sibling) == 0 || w.rows("org_id", b.org) == 0 || w.keys(b.projects[0]) == 0 {
		t.Fatal("the purge took data of a project that wasn't deleted")
	}

	// The organization: everything of it.
	if err := w.c.Tenancy.DeleteOrg(w.ctx, a.org); err != nil {
		t.Fatal(err)
	}
	if n := w.rows("org_id", a.org); n != 0 {
		t.Fatalf("%d rows of the deleted organization left", n)
	}
	if err := w.app.Purge.Purge(w.ctx, purge.Args{Org: a.org, Whole: true, Projects: a.projects, Final: true}); err != nil {
		t.Fatal(err)
	}
	if w.keys(sibling) != 0 {
		t.Fatal("the deleted organization's Valkey keys are left")
	}
	if n := w.clickhouse(sibling); n != 0 {
		t.Fatalf("%d ClickHouse rows of the deleted organization are left", n)
	}
	if w.clickhouse(b.projects[0]) != 9 {
		t.Fatal("the purge took another organization's ClickHouse rows")
	}
	if w.rows("org_id", b.org) == 0 || w.keys(b.projects[0]) == 0 {
		t.Fatal("the purge took another organization's data")
	}
}
