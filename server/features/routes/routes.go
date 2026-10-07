// Package routes keeps the named paths requests take through the gateway:
// for now, what to try when the model asked for fails (fallbacks) and how
// often to retry. Requests pick a route with X-Promptdam-Route, else their
// key's, else the project's route named Default.
package routes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/ent/route"
)

// Default is the route requests take when none is named.
const Default = "default"

// MaxPerProject bounds a project's routes: the gateway holds them all.
const MaxPerProject = 100

// Errors.
var (
	ErrNotFound  = errors.New("routes: not found")
	ErrNameTaken = errors.New("routes: the name is taken")
	ErrLimit     = errors.New("routes: too many routes")
)

// InvalidError is input that can't be saved; its message says why.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "routes: " + e.Reason }

// Input is a route's fields.
type Input struct {
	Name      string
	Fallbacks []string
	Retries   int
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

func check(in *Input) error {
	bad := func(format string, args ...any) error { return &InvalidError{Reason: fmt.Sprintf(format, args...)} }
	in.Name = strings.TrimSpace(in.Name)
	if !nameRE.MatchString(in.Name) {
		return bad("a route name is 1 to 100 lowercase letters, digits, dots, dashes or underscores")
	}
	if in.Retries < 0 || in.Retries > 3 {
		return bad("retries are 0 to 3")
	}
	out := make([]string, 0, len(in.Fallbacks))
	for _, f := range in.Fallbacks {
		f = strings.TrimSpace(f)
		if f == "" || len(f) > 300 || strings.HasPrefix(f, "/") || strings.HasSuffix(f, "/") {
			return bad("%q: a fallback is a model, or a provider's name and a model (provider/model)", f)
		}
		out = append(out, f)
	}
	if len(out) > 5 {
		return bad("a route has at most 5 fallbacks")
	}
	in.Fallbacks = out
	return nil
}

// Service keeps routes.
type Service struct {
	ent     *pdent.Client
	changed func(ctx context.Context, project uuid.UUID)
}

// New returns a Service; changed runs after a project's routes change.
func New(ent *pdent.Client, changed func(ctx context.Context, project uuid.UUID)) *Service {
	if changed == nil {
		changed = func(context.Context, uuid.UUID) {}
	}
	return &Service{ent: ent, changed: changed}
}

// List returns a project's routes by name.
func (s *Service) List(ctx context.Context, project uuid.UUID) ([]*pdent.Route, error) {
	return s.ent.Route.Query().Where(route.ProjectID(project)).Order(pdent.Asc(route.FieldName)).All(ctx)
}

// Get returns one of a project's routes.
func (s *Service) Get(ctx context.Context, project, id uuid.UUID) (*pdent.Route, error) {
	r, err := s.ent.Route.Query().Where(route.ID(id), route.ProjectID(project)).Only(ctx)
	if pdent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	return r, err
}

// Create adds a route.
func (s *Service) Create(ctx context.Context, org, project uuid.UUID, in Input) (*pdent.Route, error) {
	if err := check(&in); err != nil {
		return nil, err
	}
	n, err := s.ent.Route.Query().Where(route.ProjectID(project)).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n >= MaxPerProject {
		return nil, ErrLimit
	}
	r, err := s.ent.Route.Create().SetOrgID(org).SetProjectID(project).
		SetName(in.Name).SetFallbacks(in.Fallbacks).SetRetries(in.Retries).Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	s.changed(ctx, project)
	return r, nil
}

// Update replaces a route's fields.
func (s *Service) Update(ctx context.Context, project, id uuid.UUID, in Input) (*pdent.Route, error) {
	if err := check(&in); err != nil {
		return nil, err
	}
	cur, err := s.Get(ctx, project, id)
	if err != nil {
		return nil, err
	}
	r, err := s.ent.Route.UpdateOne(cur).SetName(in.Name).SetFallbacks(in.Fallbacks).SetRetries(in.Retries).Save(ctx)
	if pdent.IsConstraintError(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	s.changed(ctx, project)
	return r, nil
}

// Delete removes a route.
func (s *Service) Delete(ctx context.Context, project, id uuid.UUID) error {
	n, err := s.ent.Route.Delete().Where(route.ID(id), route.ProjectID(project)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.changed(ctx, project)
	return nil
}
