package postpostgres

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/post"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) PublishContentUpdate(ctx context.Context, update post.ContentUpdate) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, s.deps.Signals, contentUpdatedEvent(update))
}

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

var updateSources = map[post.UpdateSource]managev1.ContentUpdateSource{
	post.SourceManage:        managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
	post.SourceCollaboration: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
	post.SourceAI:            managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
}

// contentUpdatedEvent renders update as its content.updated event, or nil
// when the change has nothing to signal.
func contentUpdatedEvent(update post.ContentUpdate) *managev1.ContentUpdatedEvent {
	postID, fields, revision, targetRevision := update.PostID, update.Fields, update.DocumentRevision, update.TargetRevision
	documentStateChanged := update.DocumentStateChanged
	locale := strings.TrimSpace(update.Locale)
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
	localeExists := !update.LocaleDeleted
	event := &managev1.ContentUpdatedEvent{
		EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		EntityId:   postID, Source: updateSources[update.Source], ChangedFields: changedFields,
		ContributorMemberIds: normalizedMemberIDs(update.Contributors),
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

// normalizedMemberIDs trims, deduplicates and sorts memberIDs.
func normalizedMemberIDs(memberIDs []string) []string {
	unique := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID = strings.TrimSpace(memberID); memberID != "" {
			unique[memberID] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(unique))
	for memberID := range unique {
		normalized = append(normalized, memberID)
	}
	sort.Strings(normalized)
	return normalized
}
