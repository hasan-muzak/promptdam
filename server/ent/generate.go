// Package ent is Promptdam's own Ent graph (ADR 0004): providers, key
// settings, routes, budgets and the other tables only Promptdam has, in the
// same database as the core graph but without edges to it. Rows carry
// org_id and project_id. Everything except this file and schema/ is
// generated; run `task gen` after changing a schema, then author a
// migration (ADR 0003, `task promptdam:migration -- <name>`).
//
// Tenant isolation is on: every entity's queries and mutations are scoped
// to the organization in their context (entx.WithOrg), and fail without one
// unless the context opts out with entx.SkipTenant.
package ent

// The runtime package registers the schemas' defaults and the tenant
// interceptor and hook. Importing it here, rather than in each main, means
// no client can be built without them.
import _ "github.com/hasan-muzak/keel/products/promptdam/server/ent/runtime"

//go:generate go tool ent generate --feature sql/upsert,sql/execquery,sql/lock,intercept --template ../../../../pkg/entx/template ./schema
