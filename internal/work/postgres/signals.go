package workpostgres

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/work"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/lib/pq"
)

func (s *store) PublishContentUpdate(ctx context.Context, update work.ContentUpdate) error {
	if update.Kind == work.InteractiveUpdate {
		change := update.Interactive
		return contentsignal.PublishInteractive(ctx, s.tx, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
			update.WorkID, change.Locale, change.DocumentRevision, change.TargetRevision,
			change.Changed, change.LocaleDeleted, change.ContributorID)
	}
	return contentsignal.PublishInTransaction(ctx, s.tx, ownerSignalPublisher(s.ports.Signals), contentUpdatedEvent(update))
}

// contentUpdatedEvent renders a Work's content.updated signal; nil when the
// update describes no observable change.
func contentUpdatedEvent(update work.ContentUpdate) *managev1.ContentUpdatedEvent {
	switch update.Kind {
	case work.SettingsUpdate:
		return buildManageWorkContentUpdatedEvent(update.WorkID, update.Fields)
	case work.StateUpdate:
		return buildManageStateTransitionContentUpdatedEvent(update.WorkID, update.Fields)
	case work.MediaUpdate:
		return buildManageMediaMutationContentUpdatedEvent(managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK, update.WorkID, "media.featured_image")
	case work.LocaleUpdate:
		change := update.Locale
		return buildWorkLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID: update.WorkID, Fields: change.Fields, DocumentRevision: change.DocumentRevision,
			Contributors: change.Contributors, Source: updateSource(change.Source), Locale: change.Locale,
			TargetRevision: change.TargetRevision, DocumentStateChanged: change.DocumentStateChanged,
		})
	default:
		return nil
	}
}

func updateSource(source work.UpdateSource) managev1.ContentUpdateSource {
	switch source {
	case work.SourceManage:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE
	case work.SourceCollaboration:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB
	default:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED
	}
}

type contentUpdatedFieldSpec struct {
	path string
	kind managev1.ContentUpdatedFieldKind
}

var workContentUpdatedFieldSpecs = map[string]contentUpdatedFieldSpec{
	"type":       {path: "settings.type", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"metadata":   {path: "settings.metadata", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"featured":   {path: "state.featured", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"year":       {path: "state.year", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"month":      {path: "state.month", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"untilYear":  {path: "state.until_year", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"untilMonth": {path: "state.until_month", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"isPresent":  {path: "state.is_present", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"clients":    {path: "relations.clients", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"mapPlaceId": {path: "relations.map_place", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"slug":       {path: "settings.slug", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
}

func buildManageWorkContentUpdatedEvent(workID string, fields []string) *managev1.ContentUpdatedEvent {
	return buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
		workID,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		buildContentUpdatedFields(fields, workContentUpdatedFieldSpecs),
		false, "", nil,
	)
}

var workLocaleContentUpdatedFieldSpecs = map[string]contentUpdatedFieldSpec{
	"title":   {path: "title", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"summary": {path: "summary", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"content": {path: "document.content", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"sourceLocale": {
		path: "settings.source_locale", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION,
	},
}

func buildWorkLocaleContentUpdatedEvent(change contentsignal.LocaleChange) *managev1.ContentUpdatedEvent {
	locale := strings.TrimSpace(change.Locale)
	if locale == "" || strings.TrimSpace(change.DocumentRevision) == "" {
		return nil
	}
	targetRevision := change.TargetRevision
	if targetRevision != nil && (strings.TrimSpace(*targetRevision) == "" || change.DocumentStateChanged) {
		return nil
	}
	if targetRevision == nil && !change.DocumentStateChanged {
		return nil
	}
	event := buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK, change.EntityID, change.Source,
		buildContentUpdatedFields(change.Fields, workLocaleContentUpdatedFieldSpecs),
		change.DocumentStateChanged, change.DocumentRevision, change.Contributors,
	)
	if event == nil {
		return nil
	}
	localeExists := !change.LocaleDeleted
	event.Locale = &locale
	event.LocaleExists = &localeExists
	event.TargetRevision = targetRevision
	return event
}

func buildManageStateTransitionContentUpdatedEvent(
	entityID string,
	paths []string,
) *managev1.ContentUpdatedEvent {
	specs := map[string]contentUpdatedFieldSpec{
		"state.status":       {path: "state.status", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
		"state.published_at": {path: "state.published_at", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	}
	return buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK, entityID, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		buildContentUpdatedFields(paths, specs), false, "", nil,
	)
}

func buildManageMediaMutationContentUpdatedEvent(
	entityType managev1.ContentEntityType,
	entityID string,
	path string,
) *managev1.ContentUpdatedEvent {
	return buildContentUpdatedEvent(
		entityType,
		entityID,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		[]*managev1.ContentUpdatedField{{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA}},
		false, "", nil,
	)
}

func buildContentUpdatedEvent(
	entityType managev1.ContentEntityType,
	entityID string,
	source managev1.ContentUpdateSource,
	changedFields []*managev1.ContentUpdatedField,
	documentStateChanged bool,
	documentRevision string,
	contributors []string,
) *managev1.ContentUpdatedEvent {
	if len(changedFields) == 0 && !documentStateChanged {
		return nil
	}
	event := &managev1.ContentUpdatedEvent{
		EntityType: entityType, EntityId: entityID, Source: source,
		ChangedFields: changedFields, ContributorMemberIds: normalizeContributorMemberIDs(contributors),
		DocumentStateChanged: documentStateChanged, TimestampMs: time.Now().UnixMilli(),
	}
	if documentRevision != "" {
		event.DocumentRevision = &documentRevision
	}
	return event
}

func buildContentUpdatedFields(fields []string, specs map[string]contentUpdatedFieldSpec) []*managev1.ContentUpdatedField {
	result := make([]*managev1.ContentUpdatedField, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		spec, ok := specs[field]
		if !ok {
			spec = contentUpdatedFieldSpec{path: field, kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION}
		}
		if _, ok := seen[spec.path]; ok {
			continue
		}
		seen[spec.path] = struct{}{}
		result = append(result, &managev1.ContentUpdatedField{Path: spec.path, Kind: spec.kind})
	}
	return result
}

func normalizeContributorMemberIDs(memberIDs []string) pq.StringArray {
	unique := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID = strings.TrimSpace(memberID); memberID != "" {
			unique[memberID] = struct{}{}
		}
	}
	result := make(pq.StringArray, 0, len(unique))
	for memberID := range unique {
		result = append(result, memberID)
	}
	sort.Strings(result)
	return result
}
