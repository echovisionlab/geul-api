package programevent

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func publishContentUpdatedEvent(ctx context.Context, publisher AsyncPublisher, event *managev1.ContentUpdatedEvent) error {
	if publisher == nil || event == nil {
		return nil
	}
	err := publisher.NotifyProtobuf(ctx, eventpkg.SignalContentUpdated, event)
	if err != nil {
		slog.Warn("Failed to publish Program Event content update", "event_id", event.EntityId, "error", err)
	}
	return err
}

func buildProgramEventBlockContentUpdatedEvent(
	eventID string,
	fields []string,
	revision string,
	contributors []string,
	locale string,
	localeExists bool,
	targetRevision *string,
	documentStateChanged bool,
) *managev1.ContentUpdatedEvent {
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
	if !localeExists && targetRevision != nil {
		return nil
	}
	normalizedContributors := make([]string, 0, len(contributors))
	unique := make(map[string]struct{}, len(contributors))
	for _, contributor := range contributors {
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
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
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
	return buildProgramEventBlockContentUpdatedEvent(
		command.EventID,
		[]string{"content"},
		result.DocumentRevision,
		[]string{command.ContributorMemberID.String()},
		command.RequestedLocale,
		!command.DeleteTranslation,
		result.TargetRevision,
		command.RequestedLocale == command.ObservedSourceLocale,
	)
}
