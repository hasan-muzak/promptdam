package families

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hasan-muzak/keel/pkg/events"

	pdent "github.com/hasan-muzak/keel/products/promptdam/server/ent"
	"github.com/hasan-muzak/keel/products/promptdam/server/ent/familylabel"
)

// MaxRange bounds a query's time range: rollups are small.
const MaxRange = 400 * 24 * time.Hour

// Errors.
var (
	ErrRange   = errors.New("families: the time range is invalid or too long")
	ErrLabel   = errors.New("families: a label is 1 to 100 characters")
	ErrUnknown = errors.New("families: not a family ID")
)

// Service reports on a project's families from the rollups and keeps their
// labels.
type Service struct {
	ent *pdent.Client
	db  *events.DB
}

// NewService returns a Service; db may be nil (no ClickHouse).
func NewService(ent *pdent.Client, db *events.DB) *Service { return &Service{ent: ent, db: db} }

// Stats are a family's (or a model's) sums over a range.
type Stats struct {
	Key             string `ch:"grp"`
	Requests        uint64 `ch:"requests"`
	Errors          uint64 `ch:"errors"`
	InputTokens     uint64 `ch:"input_tokens"`
	OutputTokens    uint64 `ch:"output_tokens"`
	CacheReadTokens uint64 `ch:"cache_read_tokens"`
	CostMicros      int64  `ch:"cost_micros"`
}

// Family is a family with its sums and label.
type Family struct {
	ID    string
	Label string
	Stats
}

// ModelStats is a family's calls asked of one model and served by one.
type ModelStats struct {
	RequestedModel string    `ch:"requested_model"`
	ServedModel    string    `ch:"served_model"`
	Requests       uint64    `ch:"requests"`
	Errors         uint64    `ch:"errors"`
	InputTokens    uint64    `ch:"input_tokens"`
	OutputTokens   uint64    `ch:"output_tokens"`
	CostMicros     int64     `ch:"cost_micros"`
	Latency        []float32 `ch:"latency"`
}

// Detail is one family over a range.
type Detail struct {
	Family
	Models []ModelStats
	Series []Point
}

// Point is one day of a family.
type Point struct {
	T          time.Time `ch:"t"`
	Requests   uint64    `ch:"requests"`
	CostMicros int64     `ch:"cost_micros"`
}

const sums = `sum(requests) AS requests, sum(errors) AS errors, sum(input_tokens) AS input_tokens,
	sum(output_tokens) AS output_tokens, sum(cache_read_tokens) AS cache_read_tokens, sum(cost_micros) AS cost_micros`

const window = ` WHERE {tenant} AND hour >= toStartOfHour(fromUnixTimestamp64Milli(?)) AND hour < fromUnixTimestamp64Milli(?)`

func checkRange(from, to time.Time) error {
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > MaxRange {
		return ErrRange
	}
	return nil
}

// ValidID reports whether id can be a family's ID.
func ValidID(id string) bool {
	if name, ok := strings.CutPrefix(id, "name:"); ok {
		return nameRE.MatchString(name)
	}
	if len(id) != 16 {
		return false
	}
	for i := range len(id) {
		if !isHex(id[i]) || id[i] >= 'A' && id[i] <= 'F' {
			return false
		}
	}
	return true
}

// Top returns a project's families over [from, to), by spend, at most
// limit, with their labels.
func (s *Service) Top(ctx context.Context, project uuid.UUID, from, to time.Time, limit int) ([]Family, error) {
	if err := checkRange(from, to); err != nil {
		return nil, err
	}
	out := []Family{}
	if s.db == nil {
		return out, nil
	}
	var rows []Stats
	err := s.db.Select(ctx, &rows, events.Project(project), `SELECT family_id AS grp, `+sums+` FROM families_hourly`+window+
		` GROUP BY grp ORDER BY cost_micros DESC, requests DESC LIMIT ?`, from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("families: top: %w", err)
	}
	labels, err := s.labels(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, Family{ID: r.Key, Label: labels[r.Key], Stats: r})
	}
	return out, nil
}

// Get returns one family over [from, to): its sums, its calls per
// requested and served model, and a daily series.
func (s *Service) Get(ctx context.Context, project uuid.UUID, id string, from, to time.Time) (Detail, error) {
	if !ValidID(id) {
		return Detail{}, ErrUnknown
	}
	if err := checkRange(from, to); err != nil {
		return Detail{}, err
	}
	d := Detail{Family: Family{ID: id}, Models: []ModelStats{}, Series: []Point{}}
	labels, err := s.labels(ctx, project)
	if err != nil {
		return Detail{}, err
	}
	d.Label = labels[id]
	if s.db == nil {
		return d, nil
	}
	args := []any{from.UnixMilli(), to.UnixMilli(), id}
	var total []Stats
	if err := s.db.Select(ctx, &total, events.Project(project), `SELECT family_id AS grp, `+sums+` FROM families_hourly`+window+
		` AND family_id = ? GROUP BY grp`, args...); err != nil {
		return Detail{}, fmt.Errorf("families: family: %w", err)
	}
	if len(total) > 0 {
		d.Stats = total[0]
	}
	if err := s.db.Select(ctx, &d.Models, events.Project(project), `SELECT requested_model, served_model,
		sum(requests) AS requests, sum(errors) AS errors, sum(input_tokens) AS input_tokens,
		sum(output_tokens) AS output_tokens, sum(cost_micros) AS cost_micros,
		quantilesTDigestMerge(0.5, 0.95, 0.99)(latency_ms) AS latency
		FROM families_hourly`+window+` AND family_id = ?
		GROUP BY requested_model, served_model ORDER BY cost_micros DESC LIMIT 50`, args...); err != nil {
		return Detail{}, fmt.Errorf("families: models: %w", err)
	}
	if err := s.db.Select(ctx, &d.Series, events.Project(project), `SELECT toStartOfDay(hour) AS t,
		sum(requests) AS requests, sum(cost_micros) AS cost_micros FROM families_hourly`+window+` AND family_id = ?
		GROUP BY t ORDER BY t`, args...); err != nil {
		return Detail{}, fmt.Errorf("families: series: %w", err)
	}
	return d, nil
}

func (s *Service) labels(ctx context.Context, project uuid.UUID) (map[string]string, error) {
	rows, err := s.ent.FamilyLabel.Query().Where(familylabel.ProjectID(project)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.FamilyID] = r.Label
	}
	return out, nil
}

// SetLabel names a family; an empty label removes its name.
func (s *Service) SetLabel(ctx context.Context, org, project uuid.UUID, id, label string) error {
	if !ValidID(id) {
		return ErrUnknown
	}
	label = strings.TrimSpace(label)
	if label == "" {
		_, err := s.ent.FamilyLabel.Delete().Where(familylabel.ProjectID(project), familylabel.FamilyID(id)).Exec(ctx)
		return err
	}
	if len(label) > 100 {
		return ErrLabel
	}
	return s.ent.FamilyLabel.Create().SetOrgID(org).SetProjectID(project).SetFamilyID(id).SetLabel(label).
		OnConflictColumns(familylabel.FieldProjectID, familylabel.FieldFamilyID).UpdateLabel().UpdateUpdatedAt().Exec(ctx)
}
