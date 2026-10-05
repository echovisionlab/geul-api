package maptheme

import (
	"context"
	stderrors "errors"
	"reflect"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// UpdateMapThemeSnapshot applies one complete management snapshot using the
// request actor. Collaboration contributor claims are not an admission path.
func (s *MapThemeService) UpdateMapThemeSnapshot(ctx context.Context, themeID string, expectedRevision int64, input *managev1.CreateMapThemeRequest) (*managev1.MapTheme, bool, error) {
	id, err := normalizeMapThemeID(themeID, "theme_id")
	if err != nil {
		return nil, false, err
	}
	if expectedRevision <= 0 {
		return nil, false, errs.InvalidArgument("expected_revision", "must be positive")
	}
	if input == nil {
		return nil, false, errs.Required("snapshot")
	}
	snapshot, err := manageCreateSnapshot(input)
	if err != nil {
		return nil, false, err
	}
	can, err := policyv1.MapTheme.Edit(id)
	if err != nil {
		return nil, false, errs.InvalidArgument("theme_id", err.Error())
	}
	var theme *model.MapTheme
	changed := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockMapThemeRoot(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.requireLockedMapThemeCan(ctx, tx, can); err != nil {
			return err
		}
		if current.EditVersion != expectedRevision {
			return errMapThemeRevisionConflict
		}
		// Compare the persisted float32 scale, rather than the caller's float64
		// spelling, so an identical roundtrip is a genuine no-op.
		snapshot.Settings.CalloutScale = float64(float32(snapshot.Settings.CalloutScale))
		stored := mapThemeSnapshot{Name: current.Name, Settings: mapThemeSettingsFromModel(current), LightVariant: mapThemeVariantSnapshotFromModel(current.LightVariant), DarkVariant: mapThemeVariantSnapshotFromModel(current.DarkVariant)}
		if reflect.DeepEqual(stored, *snapshot) {
			theme = current
			return nil
		}
		updates := mapThemeSnapshotUpdates(snapshot)
		updates["edit_version"] = gorm.Expr("edit_version + 1")
		updates["updated_at"] = time.Now()
		result := tx.Model(&model.MapTheme{}).Where("id = ? AND edit_version = ?", id, expectedRevision).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errMapThemeRevisionConflict
		}
		if s.auditWriter != nil {
			if err := domainaudit.AppendRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditMapThemeUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMapThemeContentUpdatedAuditRecord(metadata, id)
				}); err != nil {
				return err
			}
		}
		if err := tx.Where("id = ?", id).Take(current).Error; err != nil {
			return err
		}
		theme, changed = current, true
		return nil
	})
	if err != nil {
		if stderrors.Is(err, errMapThemeRevisionConflict) {
			return nil, false, errs.CollaborationConflict(intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, "map theme changed; reload before saving")
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, false, err
		}
		return nil, false, errs.Internal(err)
	}
	response, err := s.toProto(theme)
	if err != nil {
		return nil, false, errs.Internal(err)
	}
	return response, changed, nil
}
