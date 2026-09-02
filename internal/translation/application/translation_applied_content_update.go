package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"gorm.io/gorm"
)

func (m *TranslationJobManager) publishAppliedTranslationContentUpdated(
	ctx context.Context,
	tx *gorm.DB,
	job *model.TranslationJob,
	target AppliedTranslationTarget,
	now time.Time,
) error {
	if !target.Changed {
		return nil
	}
	event, err := buildAppliedTranslationContentUpdated(job, target, now)
	if err != nil {
		return err
	}
	if m == nil || m.publisher == nil {
		return fmt.Errorf("translation content updated publisher is required")
	}
	if tx == nil || tx.Statement == nil || tx.Statement.ConnPool == nil {
		return fmt.Errorf("translation apply transaction is required")
	}
	executor, ok := tx.Statement.ConnPool.(eventpkg.DBTX)
	if !ok {
		return fmt.Errorf("translation apply transaction cannot publish PostgreSQL signals")
	}
	if err := m.publisher.PublishContentUpdatedWithExecutor(ctx, executor, event); err != nil {
		return fmt.Errorf("publish applied translation content update: %w", err)
	}
	return nil
}

func buildAppliedTranslationContentUpdated(
	job *model.TranslationJob,
	target AppliedTranslationTarget,
	now time.Time,
) (*managev1.ContentUpdatedEvent, error) {
	if job == nil {
		return nil, fmt.Errorf("translation job is required")
	}
	definition, ok := translation.DefinitionForKind(job.EntityType)
	if !ok || definition.ContentEntityType == managev1.ContentEntityType_CONTENT_ENTITY_TYPE_UNSPECIFIED {
		return nil, fmt.Errorf("unsupported translation entity type %q", job.EntityType)
	}
	entityID := strings.TrimSpace(job.EntityID)
	locale := strings.TrimSpace(job.TargetLocale)
	documentRevision := strings.TrimSpace(target.DocumentRevision)
	if entityID == "" || entityID != job.EntityID {
		return nil, fmt.Errorf("translation entity id is not canonical")
	}
	if locale == "" || locale != job.TargetLocale {
		return nil, fmt.Errorf("translation target locale is not canonical")
	}
	if documentRevision == "" || documentRevision != target.DocumentRevision {
		return nil, fmt.Errorf("translation document revision is not canonical")
	}
	var targetRevision *string
	trimmedTargetRevision := strings.TrimSpace(target.TargetRevision)
	if target.DocumentStateChanged {
		if target.TargetRevision != "" {
			return nil, fmt.Errorf("source translation must not carry a target revision")
		}
	} else {
		if trimmedTargetRevision == "" || trimmedTargetRevision != target.TargetRevision {
			return nil, fmt.Errorf("translation target revision is not canonical")
		}
		targetRevision = &trimmedTargetRevision
	}
	if err := validateTranslationJobRequester(job); err != nil {
		return nil, err
	}
	localeExists := true
	memberID := job.RequestedByMemberID
	return &managev1.ContentUpdatedEvent{
		EntityType: definition.ContentEntityType,
		EntityId:   entityID,
		Source:     managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		ChangedFields: []*managev1.ContentUpdatedField{{
			Path: "locale_content",
			Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT,
		}},
		DocumentRevision:     &documentRevision,
		ContributorMemberIds: []string{memberID},
		DocumentStateChanged: target.DocumentStateChanged,
		TimestampMs:          now.UTC().UnixMilli(),
		Locale:               &locale,
		LocaleExists:         &localeExists,
		TargetRevision:       targetRevision,
	}, nil
}
