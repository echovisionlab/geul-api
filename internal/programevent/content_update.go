package programevent

import (
	"context"
	"sort"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
)

// publishContentUpdatedEvent schedules content.updated in the caller's
// mutation transaction so the signal commits or rolls back with the data.
func publishContentUpdatedEvent(
	ctx context.Context,
	tx *gorm.DB,
	publisher AsyncPublisher,
	event *managev1.ContentUpdatedEvent,
) error {
	return contentsignal.PublishInTransaction(ctx, tx, publisher, event)
}

func buildProgramEventLocaleContentUpdatedEvent(change contentsignal.LocaleChange) *managev1.ContentUpdatedEvent {
	eventID, fields, revision, locale := change.EntityID, change.Fields, change.DocumentRevision, change.Locale
	localeExists, targetRevision, documentStateChanged := !change.LocaleDeleted, change.TargetRevision, change.DocumentStateChanged
	if strings.TrimSpace(eventID) == "" {
		return nil
	}
	fieldSpecs := map[string]*managev1.ContentUpdatedField{
		"title":   {Path: "title", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
		"summary": {Path: "summary", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
		"content": {Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	}
	changed := make([]*managev1.ContentUpdatedField, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		spec, ok := fieldSpecs[field]
		if !ok {
			spec = &managev1.ContentUpdatedField{Path: field, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION}
		}
		if _, ok := seen[spec.Path]; ok {
			continue
		}
		seen[spec.Path] = struct{}{}
		changed = append(changed, spec)
	}
	if len(changed) == 0 && !documentStateChanged {
		return nil
	}
	if strings.TrimSpace(locale) == "" {
		return nil
	}
	if !localeExists {
		// A deleted locale has no target revision; the resulting document
		// revision is the authority for the deletion.
		targetRevision = nil
	}
	normalizedContributors := make([]string, 0, len(change.Contributors))
	unique := make(map[string]struct{}, len(change.Contributors))
	for _, contributor := range change.Contributors {
		contributor = strings.TrimSpace(contributor)
		if contributor == "" {
			continue
		}
		unique[contributor] = struct{}{}
	}
	for contributor := range unique {
		normalizedContributors = append(normalizedContributors, contributor)
	}
	sort.Strings(normalizedContributors)
	event := &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT,
		EntityId:             eventID,
		Source:               change.Source,
		ChangedFields:        changed,
		ContributorMemberIds: normalizedContributors,
		DocumentStateChanged: documentStateChanged,
		TimestampMs:          time.Now().UnixMilli(),
	}
	if revision != "" {
		event.DocumentRevision = &revision
	}
	event.Locale = &locale
	event.LocaleExists = &localeExists
	if localeExists && !documentStateChanged {
		event.TargetRevision = targetRevision
	}
	return event
}

func buildProgramEventAIDocumentContentUpdatedEvent(
	command AIDocumentCommand,
	result AIDocumentResult,
) *managev1.ContentUpdatedEvent {
	if !result.Changed {
		return nil
	}
	fields := make([]string, 0, 3)
	if command.Metadata.SetTitle {
		fields = append(fields, "title")
	}
	if command.Metadata.SetSummary {
		fields = append(fields, "summary")
	}
	if command.Batch == nil || len(command.Batch.Upserts) != 0 || len(command.Batch.Deletes) != 0 ||
		len(command.Batch.Reorders) != 0 || len(command.Batch.LocaleGroups) != 0 {
		fields = append(fields, "content")
	}
	return buildProgramEventLocaleContentUpdatedEvent(contentsignal.LocaleChange{
		EntityID:             command.EventID,
		Fields:               fields,
		DocumentRevision:     result.DocumentRevision,
		Contributors:         []string{command.ContributorMemberID.String()},
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		Locale:               command.RequestedLocale,
		LocaleDeleted:        command.DeleteTranslation,
		TargetRevision:       result.TargetRevision,
		DocumentStateChanged: command.RequestedLocale == command.ObservedSourceLocale,
	})
}
