package purge_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/config"
	"github.com/hasan-muzak/keel/pkg/entx"
	"github.com/hasan-muzak/keel/pkg/identity"
	"github.com/hasan-muzak/keel/pkg/jobs"
	"github.com/hasan-muzak/keel/pkg/keel"
	"github.com/hasan-muzak/keel/pkg/testkit"

	"github.com/hasan-muzak/keel/products/promptdam/server/core"
	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/ent/budget"
	"github.com/hasan-muzak/keel/products/promptdam/server/ent/keysetting"
	"github.com/hasan-muzak/keel/products/promptdam/server/ent/provider"
	"github.com/hasan-muzak/keel/products/promptdam/server/ent/route"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/purge"
	"github.com/hasan-muzak/keel/products/promptdam/server/migrations"
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
		{"Budget", budget.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.Budget.Create().SetOrgID(org).SetProjectID(p).SetName("month").SetScope(budget.ScopeProject).
				SetWindow(budget.WindowMonth).SetLimitMicros(1_000_000).Exec(ctx)
		}},
		{"KeySetting", keysetting.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.KeySetting.Create().SetOrgID(org).SetProjectID(p).SetAPIKeyID(uuid.New()).Exec(ctx)
		}},
		{"Route", route.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.Route.Create().SetOrgID(org).SetProjectID(p).SetName("default").Exec(ctx)
		}},
		{"Provider", provider.Table, func(ctx context.Context, org, p uuid.UUID) error {
			return c.Provider.Create().SetOrgID(org).SetProjectID(p).SetName("openai").SetKind("openai").
				SetAPIFormat(provider.APIFormatOpenai).SetBaseURL("https://api.openai.com/v1").Exec(ctx)
		}},
	}
}

// Every entity of the graph is seeded, so a new one fails here until the
// purge (deleteRows) and this list cover it.
func TestSeedsCoverTheGraph(t *testing.T) {
	byName := map[string]bool{}
	for _, s := range seeds(nil) {
		byName[s.name] = true
	}
	client := reflect.TypeFor[pdent.Client]()
	for i := range client.NumField() {
		f := client.Field(i)
		if f.Type.Kind() == reflect.Pointer && strings.HasSuffix(f.Type.Elem().Name(), "Client") && !byName[f.Name] {
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
	if w.rows("org_id", b.org) == 0 || w.keys(b.projects[0]) == 0 {
		t.Fatal("the purge took another organization's data")
	}
}
