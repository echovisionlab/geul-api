package post

import (
	"sort"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/lib/pq"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
)

type contentUpdatedFieldSpec struct {
	path string
	kind managev1.ContentUpdatedFieldKind
}

var postContentUpdatedFieldSpecs = map[string]contentUpdatedFieldSpec{
	"title":               {path: "title", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"summary":             {path: "summary", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"content":             {path: "document.content", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"featuredImageFileId": {path: "media.featured_image", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA},
	"slug":                {path: "settings.slug", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"status":              {path: "state.status", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"categoryIds":         {path: "relations.categories", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"tagIds":              {path: "relations.tags", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"seriesId":            {path: "relations.series", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"seriesOrder":         {path: "relations.series_order", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"mapPlaceId":          {path: "relations.map_place", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"commentsEnabled":     {path: "settings.comments_enabled", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"documentLayout":      {path: "settings.document_layout", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"sourceLocale":        {path: "settings.source_locale", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
}

// publishContentUpdatedEvent schedules content.updated in the caller's
// mutation transaction so the signal commits or rolls back with the data.
func buildPostLocaleContentUpdatedEvent(change contentsignal.LocaleChange) *managev1.ContentUpdatedEvent {
	postID, fields, revision, targetRevision := change.EntityID, change.Fields, change.DocumentRevision, change.TargetRevision
	documentStateChanged := change.DocumentStateChanged
	locale := strings.TrimSpace(change.Locale)
	if postID == "" || locale == "" || strings.TrimSpace(revision) == "" {
		return nil
	}
	changedFields := buildContentUpdatedFields(fields)
	if len(changedFields) == 0 && !documentStateChanged {
		return nil
	}
	if targetRevision != nil && (strings.TrimSpace(*targetRevision) == "" || documentStateChanged) {
		return nil
	}
	if targetRevision == nil && !documentStateChanged {
		return nil
	}
	localeExists := !change.LocaleDeleted
	event := &managev1.ContentUpdatedEvent{
		EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		EntityId:   postID, Source: change.Source, ChangedFields: changedFields,
		ContributorMemberIds: normalizeContributorMemberIDs(change.Contributors),
		DocumentStateChanged: documentStateChanged,
		TimestampMs:          time.Now().UnixMilli(),
		Locale:               &locale,
		LocaleExists:         &localeExists,
	}
	if targetRevision != nil {
		event.TargetRevision = targetRevision
	}
	event.DocumentRevision = &revision
	return event
}

func postTargetRevisionSignal(locale, sourceLocale, revision string) *string {
	if locale == sourceLocale || strings.TrimSpace(revision) == "" {
		return nil
	}
	return &revision
}

func buildContentUpdatedFields(keys []string) []*managev1.ContentUpdatedField {
	fields := make([]*managev1.ContentUpdatedField, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		spec, ok := postContentUpdatedFieldSpecs[key]
		if !ok {
			spec = contentUpdatedFieldSpec{path: key, kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION}
		}
		if _, ok := seen[spec.path]; ok {
			continue
		}
		seen[spec.path] = struct{}{}
		fields = append(fields, &managev1.ContentUpdatedField{Path: spec.path, Kind: spec.kind})
	}
	return fields
}

func normalizeContributorMemberIDs(memberIDs []string) pq.StringArray {
	unique := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID = strings.TrimSpace(memberID); memberID != "" {
			unique[memberID] = struct{}{}
		}
	}
	normalized := make(pq.StringArray, 0, len(unique))
	for memberID := range unique {
		normalized = append(normalized, memberID)
	}
	sort.Strings(normalized)
	return normalized
}
