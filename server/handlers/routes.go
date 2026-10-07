package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	muzak "muzak.dev/framework"

	"github.com/hasan-muzak/keel/pkg/httpx"
	"github.com/hasan-muzak/keel/pkg/ids"
	"github.com/hasan-muzak/keel/pkg/platform"
	"github.com/hasan-muzak/keel/pkg/tenancy"

	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/features/routes"
)

// RoutePrefix starts route IDs: rte_….
const RoutePrefix = "rte"

// Error codes of the route routes.
const (
	CodeInvalidRoute   = "invalid_route"
	CodeRouteNameTaken = "route_name_taken"
	CodeRouteLimit     = "route_limit"
)

// RouteOut is a route.
type RouteOut struct {
	ID          string    `json:"id" doc:"Route ID (rte_…)"`
	Name        string    `json:"name" doc:"Requests name it in X-Promptdam-Route; \"default\" applies when they name none"`
	Fallbacks   []string  `json:"fallbacks" doc:"Tried in order when the model asked for fails: model, or provider/model"`
	Retries     int       `json:"retries" doc:"Retries of a target, with jitter, before the next one"`
	CacheTTL    int       `json:"cache_ttl_seconds" doc:"Identical non-streamed requests are answered from the response cache for this long; 0: off"`
	PromptCache bool      `json:"prompt_cache" doc:"Anthropic requests without cache breakpoints get them at the end of their stable prefix (tools, system prompt)"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func routeOut(r *pdent.Route) RouteOut {
	return RouteOut{ID: ids.Encode(RoutePrefix, r.ID), Name: r.Name, Fallbacks: r.Fallbacks, Retries: r.Retries,
		CacheTTL: r.CacheTTLSeconds, PromptCache: r.PromptCache, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// RouteIn is a route's fields.
type RouteIn struct {
	Name        string   `json:"name"`
	Fallbacks   []string `json:"fallbacks"`
	Retries     *int     `json:"retries,omitempty" doc:"0 to 3 (default 1)"`
	CacheTTL    int      `json:"cache_ttl_seconds,omitempty" doc:"0 (off, default) to 604800"`
	PromptCache bool     `json:"prompt_cache,omitempty" doc:"Default false"`
}

func (in *RouteIn) validate(v *muzak.Validation) {
	v.String(&in.Name).Trim().Required().MaxLen(100)
	v.Slice(&in.Fallbacks).MaxItems(5)
}

func (in RouteIn) input() routes.Input {
	retries := 1
	if in.Retries != nil {
		retries = *in.Retries
	}
	return routes.Input{Name: in.Name, Fallbacks: in.Fallbacks, Retries: retries, CacheTTL: in.CacheTTL, PromptCache: in.PromptCache}
}

// CreateRouteIn adds a route.
type CreateRouteIn struct {
	platform.ProjectPath
	RouteIn
}

// Validate implements muzak.Validatable.
func (in *CreateRouteIn) Validate(v *muzak.Validation) { in.validate(v) }

// RoutePath names a route.
type RoutePath struct {
	platform.ProjectPath
	Route string `path:"route" doc:"Route ID (rte_…)"`
}

// UpdateRouteIn replaces a route.
type UpdateRouteIn struct {
	RoutePath
	RouteIn
}

// Validate implements muzak.Validatable.
func (in *UpdateRouteIn) Validate(v *muzak.Validation) { in.validate(v) }

// ListRoutes lists a project's routes.
func (h *Handlers) ListRoutes(ctx *muzak.Context, _ platform.ProjectPath) (httpx.List[RouteOut], error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	rs, err := h.Routes.List(ctx.Context(), pa.Project.ID)
	if err != nil {
		return httpx.List[RouteOut]{}, err
	}
	out := make([]RouteOut, len(rs))
	for i, r := range rs {
		out[i] = routeOut(r)
	}
	return httpx.List[RouteOut]{Data: out}, nil
}

// CreateRoute adds a route.
func (h *Handlers) CreateRoute(ctx *muzak.Context, in CreateRouteIn) (RouteOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	r, err := h.Routes.Create(ctx.Context(), pa.OrgID(), pa.Project.ID, in.input())
	if err != nil {
		return RouteOut{}, routeFail(err)
	}
	return routeOut(r), nil
}

// GetRoute returns a route.
func (h *Handlers) GetRoute(ctx *muzak.Context, in RoutePath) (RouteOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := routeID(in.Route)
	if err != nil {
		return RouteOut{}, err
	}
	r, err := h.Routes.Get(ctx.Context(), pa.Project.ID, id)
	if err != nil {
		return RouteOut{}, routeFail(err)
	}
	return routeOut(r), nil
}

// UpdateRoute replaces a route.
func (h *Handlers) UpdateRoute(ctx *muzak.Context, in UpdateRouteIn) (RouteOut, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := routeID(in.Route)
	if err != nil {
		return RouteOut{}, err
	}
	r, err := h.Routes.Update(ctx.Context(), pa.Project.ID, id, in.input())
	if err != nil {
		return RouteOut{}, routeFail(err)
	}
	return routeOut(r), nil
}

// DeleteRoute removes a route.
func (h *Handlers) DeleteRoute(ctx *muzak.Context, in RoutePath) (muzak.Empty, error) {
	pa := muzak.From[tenancy.ProjectAccess](ctx)
	id, err := routeID(in.Route)
	if err != nil {
		return muzak.Empty{}, err
	}
	return muzak.Empty{}, routeFail(h.Routes.Delete(ctx.Context(), pa.Project.ID, id))
}

func routeID(raw string) (uuid.UUID, error) {
	id, err := ids.Decode(RoutePrefix, raw)
	if err != nil {
		return id, httpx.NotFound("route")
	}
	return id, nil
}

func routeFail(err error) error {
	var bad *routes.InvalidError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &bad):
		return muzak.UnprocessableEntity(bad.Reason).WithCode(CodeInvalidRoute)
	case errors.Is(err, routes.ErrNotFound):
		return httpx.NotFound("route")
	case errors.Is(err, routes.ErrNameTaken):
		return muzak.Conflict("another route of the project has this name").WithCode(CodeRouteNameTaken)
	case errors.Is(err, routes.ErrLimit):
		return muzak.Conflict("a project has at most 100 routes").WithCode(CodeRouteLimit)
	}
	return err
}
