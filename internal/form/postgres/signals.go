package formpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/form"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) PublishContentUpdate(ctx context.Context, update form.ContentUpdate) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, ownerSignalPublisher(s.ports.Signals), contentUpdatedEvent(update))
}

// contentUpdatedEvent renders the content.updated signal of an AI document
// edit; nil when it names no changed field.
func contentUpdatedEvent(update form.ContentUpdate) *managev1.ContentUpdatedEvent {
	if len(update.Fields) == 0 {
		return nil
	}
	textKind := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT
	fields := make([]*managev1.ContentUpdatedField, len(update.Fields))
	for i, path := range update.Fields {
		fields[i] = &managev1.ContentUpdatedField{Path: path, Kind: textKind}
	}
	revision := update.DocumentRevision
	locale := update.Locale
	localeExists := !update.LocaleDeleted
	event := &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM,
		EntityId:             update.FormID,
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		ChangedFields:        fields,
		ContributorMemberIds: []string{update.Contributor},
		DocumentRevision:     &revision,
		DocumentStateChanged: update.SourceLocale,
		Locale:               &locale,
		LocaleExists:         &localeExists,
		TimestampMs:          time.Now().UnixMilli(),
	}
	if update.TargetRevision != nil {
		targetRevision := *update.TargetRevision
		event.TargetRevision = &targetRevision
	}
	return event
}
