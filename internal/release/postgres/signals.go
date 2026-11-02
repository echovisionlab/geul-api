package releasepostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/release"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) PublishContentUpdate(ctx context.Context, update release.ContentUpdate) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, ownerSignalPublisher(s.ports.Publisher), contentUpdatedEvent(update))
}

// contentUpdatedEvent renders a Release's content.updated signal.
func contentUpdatedEvent(update release.ContentUpdate) *managev1.ContentUpdatedEvent {
	fields := make([]*managev1.ContentUpdatedField, len(update.Fields))
	for i, field := range update.Fields {
		fields[i] = &managev1.ContentUpdatedField{Path: field.Path, Kind: fieldKind(field.Kind)}
	}
	return &managev1.ContentUpdatedEvent{
		EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE,
		EntityId:   update.ReleaseID, Source: updateSource(update.Source),
		ChangedFields: fields, ContributorMemberIds: update.ContributorMemberIDs,
		DocumentStateChanged: update.DocumentStateChanged, DocumentRevision: update.DocumentRevision,
		Locale: update.Locale, LocaleExists: update.LocaleExists, TargetRevision: update.TargetRevision,
		TimestampMs: time.Now().UnixMilli(),
	}
}

func fieldKind(kind release.FieldKind) managev1.ContentUpdatedFieldKind {
	switch kind {
	case release.FieldText:
		return managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT
	case release.FieldConfiguration:
		return managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION
	case release.FieldState:
		return managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE
	case release.FieldRelation:
		return managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION
	case release.FieldMedia:
		return managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA
	default:
		return managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_UNSPECIFIED
	}
}

func updateSource(source release.UpdateSource) managev1.ContentUpdateSource {
	switch source {
	case release.SourceManage:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE
	case release.SourceAI:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI
	default:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED
	}
}
