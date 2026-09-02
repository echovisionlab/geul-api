package work

import (
	"sort"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/lib/pq"
)

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

func buildManageWorkContentUpdatedEvent(request *managev1.UpdateWorkRequest) *managev1.ContentUpdatedEvent {
	if request == nil {
		return nil
	}
	fields := make([]string, 0, 11)
	if request.Slug != nil {
		fields = append(fields, "slug")
	}
	if request.Type != nil {
		fields = append(fields, "type")
	}
	if request.Metadata != nil {
		fields = append(fields, "metadata")
	}
	if request.Featured != nil {
		fields = append(fields, "featured")
	}
	if request.Clients != nil {
		fields = append(fields, "clients")
	}
	if request.Year != nil {
		fields = append(fields, "year")
	}
	if request.Month != nil {
		fields = append(fields, "month")
	}
	if request.MapPlaceId != nil {
		fields = append(fields, "mapPlaceId")
	}
	if request.UntilYear != nil {
		fields = append(fields, "untilYear")
	}
	if request.UntilMonth != nil {
		fields = append(fields, "untilMonth")
	}
	if request.IsPresent != nil {
		fields = append(fields, "isPresent")
	}
	return buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
		request.Id,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		buildContentUpdatedFields(fields, workContentUpdatedFieldSpecs),
		false, "", nil,
	)
}

func buildWorkSourceContentUpdatedEvent(
	workID string,
	titleChanged bool,
	summaryChanged bool,
	documentChanged bool,
	revision string,
	contributors []string,
	source managev1.ContentUpdateSource,
	locale string,
	localeExists bool,
	targetRevision *string,
	documentStateChanged bool,
) *managev1.ContentUpdatedEvent {
	fields := make([]*managev1.ContentUpdatedField, 0, 3)
	if titleChanged {
		fields = append(fields, &managev1.ContentUpdatedField{Path: "title", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT})
	}
	if summaryChanged {
		fields = append(fields, &managev1.ContentUpdatedField{Path: "summary", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT})
	}
	if documentChanged {
		fields = append(fields, &managev1.ContentUpdatedField{Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT})
	}
	locale = strings.TrimSpace(locale)
	if locale == "" || strings.TrimSpace(revision) == "" {
		return nil
	}
	if !localeExists && (targetRevision != nil || documentStateChanged) {
		return nil
	}
	if targetRevision != nil && (strings.TrimSpace(*targetRevision) == "" || documentStateChanged) {
		return nil
	}
	if localeExists && targetRevision == nil && !documentStateChanged {
		return nil
	}
	event := buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
		workID, source, fields, documentStateChanged, revision, contributors,
	)
	if event == nil {
		return nil
	}
	event.Locale = &locale
	event.LocaleExists = &localeExists
	if localeExists && targetRevision != nil {
		event.TargetRevision = targetRevision
	}
	return event
}

func buildManageStateTransitionContentUpdatedEvent(
	entityType managev1.ContentEntityType,
	entityID string,
	paths []string,
) *managev1.ContentUpdatedEvent {
	specs := map[string]contentUpdatedFieldSpec{
		"state.status":       {path: "state.status", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
		"state.published_at": {path: "state.published_at", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	}
	return buildContentUpdatedEvent(
		entityType, entityID, managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
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
