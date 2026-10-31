package maptheme

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
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
	Theme   *managev1.MapTheme
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
	type updated struct {
		theme   *model.MapTheme
		changed bool
	}
	var current *model.MapTheme
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[updated]{
		Lock: func(ctx context.Context, tx *gorm.DB) (err error) {
			current, err = lockMapThemeRoot(ctx, tx, id)
			return err
		},
		// Authority is checked before the revision, so a caller without it
		// does not learn the conflict state.
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (updated, error) {
			if current.EditVersion != command.ExpectedRevision {
				return updated{}, mapThemeChanged(fmt.Sprint(current.EditVersion))
			}
			// Compare the persisted float32 scale, rather than the caller's
			// float64 spelling, so an identical roundtrip is a genuine no-op.
			snapshot.Settings.CalloutScale = float64(float32(snapshot.Settings.CalloutScale))
			stored := Snapshot{Name: current.Name, Settings: settingsFromModel(current), LightVariant: variantFromModel(current.LightVariant), DarkVariant: variantFromModel(current.DarkVariant)}
			if reflect.DeepEqual(stored, *snapshot) {
				return updated{theme: current}, nil
			}
			if err := writeSnapshot(tx, id, command.ExpectedRevision, snapshot, current.EditVersion); err != nil {
				return updated{}, err
			}
			if err := tx.Where("id = ?", id).Take(current).Error; err != nil {
				return updated{}, err
			}
			return updated{theme: current, changed: true}, nil
		},
		Audit: func(result updated) mutation.Audit {
			if !result.changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMapThemeUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMapThemeContentUpdatedAuditRecord(metadata, id)
			})
		},
	})
	if err != nil {
		return SnapshotUpdate{}, err
	}
	return SnapshotUpdate{Theme: toProto(result.theme), Changed: result.changed}, nil
}

// writeSnapshot stores snapshot over the row at expectedRevision and bumps
// its revision; a concurrent bump answers a revision conflict reporting
// observed.
func writeSnapshot(tx *gorm.DB, id string, expectedRevision int64, snapshot *Snapshot, observed int64) error {
	updates := snapshotUpdates(snapshot)
	updates["edit_version"] = gorm.Expr("edit_version + 1")
	updates["updated_at"] = time.Now()
	result := tx.Model(&model.MapTheme{}).Where("id = ? AND edit_version = ?", id, expectedRevision).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return mapThemeChanged(fmt.Sprint(observed))
	}
	return nil
}
