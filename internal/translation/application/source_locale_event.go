package application

import (
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

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
