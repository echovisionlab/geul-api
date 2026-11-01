package maptheme

import (
	"context"
	stderrors "errors"
	"fmt"
	"reflect"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// mapThemeChanged answers a save whose expected revision is no longer the
// current one, reporting current as the revision to reload.
func mapThemeChanged(current string) error {
	return domainerr.CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED,
		"map theme changed; reload before saving", domainerr.CollaborationRevisions{Document: current},
	)
}

// UpdateMapThemeSnapshotCommand replaces the snapshot of a theme read at
// ExpectedRevision.
type UpdateMapThemeSnapshotCommand struct {
	ThemeID          string
	ExpectedRevision int64
	Snapshot         *SnapshotInput
}

// SnapshotUpdate is a theme after an update, and whether it changed.
type SnapshotUpdate struct {
	Theme   Theme
	Changed bool
}

// UpdateMapThemeSnapshot applies one complete management snapshot using the
// request actor. Collaboration contributor claims are not an admission path.
// Identical normalized values are a no-op that keeps the revision.
func (s *Service) UpdateMapThemeSnapshot(ctx context.Context, command UpdateMapThemeSnapshotCommand) (SnapshotUpdate, error) {
	id, err := normalizeMapThemeID(command.ThemeID, "theme_id")
	if err != nil {
		return SnapshotUpdate{}, err
	}
	if command.ExpectedRevision <= 0 {
		return SnapshotUpdate{}, domainerr.InvalidField("expected_revision", "must be positive")
	}
	snapshot, err := command.Snapshot.snapshot("")
	if err != nil {
		return SnapshotUpdate{}, err
	}
	can, err := policyv1.MapTheme.Edit(id)
	if err != nil {
		return SnapshotUpdate{}, domainerr.InvalidField("theme_id", err.Error())
	}
	var current Theme
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[SnapshotUpdate, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			current, err = lockThemeRoot(ctx, store, id)
			return err
		},
		// Authority is checked before the revision, so a caller without it
		// does not learn the conflict state.
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (SnapshotUpdate, error) {
			if current.Revision != command.ExpectedRevision {
				return SnapshotUpdate{}, mapThemeChanged(fmt.Sprint(current.Revision))
			}
			// Compare the persisted float32 scale, rather than the caller's
			// float64 spelling, so an identical roundtrip is a genuine no-op.
			snapshot.Settings.CalloutScale = float64(float32(snapshot.Settings.CalloutScale))
			if reflect.DeepEqual(current.Snapshot, *snapshot) {
				return SnapshotUpdate{Theme: current}, nil
			}
			if err := writeSnapshot(ctx, store, id, command.ExpectedRevision, *snapshot, current.Revision); err != nil {
				return SnapshotUpdate{}, err
			}
			updated, err := store.Theme(ctx, id)
			if err != nil {
				return SnapshotUpdate{}, err
			}
			return SnapshotUpdate{Theme: updated, Changed: true}, nil
		},
		Audit: func(result SnapshotUpdate) mutation.Audit {
			if !result.Changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMapThemeUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapThemeContentUpdatedAuditRecord(metadata, id)
			})
		},
	})
}

// writeSnapshot stores snapshot over the theme at expectedRevision and bumps
// its revision; a concurrent bump answers a revision conflict reporting
// observed.
func writeSnapshot(ctx context.Context, store Store, id string, expectedRevision int64, snapshot Snapshot, observed int64) error {
	err := store.UpdateSnapshot(ctx, id, expectedRevision, snapshot)
	if stderrors.Is(err, ErrRevisionChanged) {
		return mapThemeChanged(fmt.Sprint(observed))
	}
	return err
}
