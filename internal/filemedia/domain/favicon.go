package domain

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
)

type FaviconStore interface {
	FaviconSource(context.Context, string, bool) (File, error)
	FaviconSet(context.Context, string, string) (*favicon.Set, error)
	AttachFaviconSource(context.Context, string, string, time.Time) (bool, error)
	InsertFaviconDerivative(context.Context, string, string, string, string) error
	ScheduleStagedFaviconDeletion(context.Context, []string, time.Time) error
}

type FaviconCommitStage int

const (
	FaviconCommitted FaviconCommitStage = iota
	FaviconBeginFailed
	FaviconChangeFailed
	FaviconCommitFailed
)

type FaviconCommitResult struct {
	Stage FaviconCommitStage
	Err   error
}

// FaviconCommits separates known rollback from an uncertain commit so the
// caller can reconcile the winning bundle before scheduling cleanup.
type FaviconCommits interface {
	Commit(context.Context, func(Store) error) FaviconCommitResult
}
