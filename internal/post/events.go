package post

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/lib/pq"
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

func publishContentUpdatedEvent(ctx context.Context, publisher AsyncPublisher, event *managev1.ContentUpdatedEvent) error {
	if publisher == nil || event == nil {
		return nil
	}
	if err := publisher.NotifyProtobuf(ctx, eventpkg.SignalContentUpdated, event); err != nil {
		slog.Warn("Failed to publish Post content updated event", "error", err, "postId", event.EntityId)
		return err
	}
	return nil
}

func buildPostBlockContentUpdatedEvent(
	postID string,
	fields []string,
	revision string,
	contributorMemberIDs []string,
	source managev1.ContentUpdateSource,
	locale string,
	localeExists bool,
	targetRevision *string,
	documentStateChanged bool,
) *managev1.ContentUpdatedEvent {
	locale = strings.TrimSpace(locale)
	if postID == "" || locale == "" || strings.TrimSpace(revision) == "" {
		return nil
	}
	changedFields := buildContentUpdatedFields(fields)
	if len(changedFields) == 0 && !documentStateChanged {
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
	event := &managev1.ContentUpdatedEvent{
		EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		EntityId:   postID, Source: source, ChangedFields: changedFields,
		ContributorMemberIds: normalizeContributorMemberIDs(contributorMemberIDs),
		DocumentStateChanged: documentStateChanged,
		TimestampMs:          time.Now().UnixMilli(),
		Locale:               &locale,
		LocaleExists:         &localeExists,
	}
	if localeExists && targetRevision != nil {
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

func buildManagePostContentUpdatedEvent(request *managev1.UpdatePostRequest) *managev1.ContentUpdatedEvent {
	if request == nil {
		return nil
	}
	// Manage metadata fields do not alter the typed translation source. Keep
	// the wake-up empty unless a source-owned field is added to this request.
	return nil
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
