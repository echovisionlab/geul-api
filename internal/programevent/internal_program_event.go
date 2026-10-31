package programevent

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func applyProgramEventLocaleMetadataMutation(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	locale string,
	title *string,
	patchTitle bool,
	summary *string,
	patchSummary bool,
	now time.Time,
) (bool, bool, error) {
	var root model.ProgramEvent
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "title", "source_locale").
		Where("id = ?", eventID).
		Take(&root).Error; err != nil {
		return false, false, err
	}
	sourceState, err := loadProgramEventSourceLocale(ctx, tx, eventID, true)
	if err != nil {
		return false, false, err
	}
	if root.SourceLocale != sourceState.SourceLocale {
		return false, false, domainerr.Precondition("Program Event source locale changed; reload before saving")
	}
	if locale != sourceState.SourceLocale {
		return false, false, domainerr.Precondition("target Program Event locale metadata is read-only")
	}
	var localeRow model.ProgramEventTranslation
	loadLocaleRow := func() error {
		return tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("entity_id = ? AND locale = ?", eventID, locale).
			Take(&localeRow).Error
	}
	if err := loadLocaleRow(); err != nil {
		return false, false, err
	}
	changed := false
	if patchTitle {
		if title == nil || strings.TrimSpace(*title) == "" {
			return false, false, domainerr.Required("title")
		}
		normalized := strings.TrimSpace(*title)
		if normalized != root.Title {
			if err := tx.Model(&model.ProgramEvent{}).
				Where("id = ?", eventID).
				Updates(structured.Fields{"title": normalized, "updated_at": now}).Error; err != nil {
				return false, false, err
			}
			changed = true
		}
	}
	if patchSummary && !sameNullableString(localeRow.Summary, summary) {
		updates := structured.Fields{
			"summary":    summary,
			"updated_at": now,
		}
		if err := tx.Model(&model.ProgramEventTranslation{}).
			Where("entity_id = ? AND locale = ?", eventID, locale).
			Updates(updates).Error; err != nil {
			return false, false, err
		}
		changed = true
	}
	return changed, changed && locale == sourceState.SourceLocale, nil
}

func programEventSummaryMutationValue(input *intrav1.NullableStringMutation) (*string, error) {
	if input == nil {
		return nil, nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		value := operation.Set
		return &value, nil
	case *intrav1.NullableStringMutation_Clear:
		if !operation.Clear {
			return nil, domainerr.InvalidField("summary", "clear must be true")
		}
		return nil, nil
	default:
		return nil, domainerr.InvalidField("summary", "set or clear is required")
	}
}
