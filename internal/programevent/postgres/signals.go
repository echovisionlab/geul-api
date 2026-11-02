package programeventpostgres

import (
	"sort"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"github.com/echovisionlab/geul-api/internal/programevent"
)

func updateSource(source programevent.UpdateSource) managev1.ContentUpdateSource {
	switch source {
	case programevent.SourceCollaboration:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB
	case programevent.SourceAI:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI
	default:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED
	}
}

// contentUpdatedEvent renders the exact locale fence of an event's
// content.updated signal; nil when the update describes no observable
// change.
func contentUpdatedEvent(update programevent.ContentUpdate) *managev1.ContentUpdatedEvent {
	eventID, fields, revision, locale := update.EventID, update.Fields, update.DocumentRevision, update.Locale
	localeExists, targetRevision, documentStateChanged := !update.LocaleDeleted, update.TargetRevision, update.DocumentStateChanged
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
	normalizedContributors := make([]string, 0, len(update.Contributors))
	unique := make(map[string]struct{}, len(update.Contributors))
	for _, contributor := range update.Contributors {
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
		Source:               updateSource(update.Source),
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
