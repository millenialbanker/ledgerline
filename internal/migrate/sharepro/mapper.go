package sharepro

import (
	"context"
	"errors"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

var errTodo = errors.New("the SharePro reader is not built yet")

// Mapper reads SharePro databases.
type Mapper struct{}

// New returns the SharePro mapper.
func New() *Mapper { return &Mapper{} }

func (*Mapper) Name() string { return "SharePro" }

func (*Mapper) Classify(ctx context.Context, q migrate.Querier) (migrate.Kind, error) {
	return migrate.Kind{}, errTodo
}

func (*Mapper) Firms(ctx context.Context, admin, company migrate.Querier) ([]pkg.Firm, error) {
	return nil, errTodo
}

func (*Mapper) Current(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	return errTodo
}

func (*Mapper) History(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	return errTodo
}

func (*Mapper) Documents(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	return errTodo
}
