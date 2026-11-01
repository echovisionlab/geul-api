package translationpostgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) PublishSourceLocaleChanged(ctx context.Context, entityType, entityID, documentRevision string) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, s.ports.Signals, buildTypedSourceLocaleUpdatedEvent(
		entityType, entityID, documentRevision,
	))
}

// PublishTranslationApplied answers an inconsistent locale fence before it
// schedules anything.
func (s *store) PublishTranslationApplied(ctx context.Context, job application.Job, target application.AppliedTranslationTarget, now time.Time) error {
	event, err := buildAppliedTranslationContentUpdated(job, target, now)
	if err != nil {
		return err
	}
	if err := contentsignal.PublishInTransaction(ctx, s.tx, s.ports.Signals, event); err != nil {
		return fmt.Errorf("publish applied translation content update: %w", err)
	}
	return nil
}

func buildTypedSourceLocaleUpdatedEvent(
	entityType string,
	entityID string,
	revision string,
) *managev1.ContentUpdatedEvent {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok || definition.ContentEntityType == managev1.ContentEntityType_CONTENT_ENTITY_TYPE_UNSPECIFIED {
		return nil
	}
	event := &managev1.ContentUpdatedEvent{
		EntityType: definition.ContentEntityType,
		EntityId:   entityID,
		Source:     managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		ChangedFields: []*managev1.ContentUpdatedField{{
			Path: "settings.source_locale",
			Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION,
		}},
		DocumentStateChanged: true,
		TimestampMs:          time.Now().UnixMilli(),
	}
	if revision != "" {
		event.DocumentRevision = &revision
	}
	return event
}

func buildAppliedTranslationContentUpdated(
	job application.Job,
	target application.AppliedTranslationTarget,
	now time.Time,
) (*managev1.ContentUpdatedEvent, error) {
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
	if err := application.ValidateJobRequester(&job); err != nil {
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
