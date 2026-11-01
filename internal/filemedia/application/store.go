package application

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

type RegisterCompensation func(time.Time, func(context.Context) error) error

type DeletionStore interface {
	LockDeletionFile(context.Context, string) (filemedia.File, error)
	ActiveFileReferenceNames(context.Context, string) ([]string, error)
	PendingTranscodeResultCounts(context.Context, string) (int64, int64, error)
	FileGenerations(context.Context, string) ([]mediaasset.Generation, error)
	PendingMeshResultCount(context.Context, string) (int64, error)
	DeleteRequestedFile(context.Context, string) (bool, error)
	FileExists(context.Context, string) (bool, error)
}

type DeletionUnits interface {
	mutation.UnitOfWork[DeletionStore]
	// Compensated holds the authorization lock, registers at most one
	// relationship compensation, and compensates before rollback. An uncertain
	// commit is neither rolled back nor compensated.
	Compensated(context.Context, func(DeletionStore, RegisterCompensation) error) error
}
