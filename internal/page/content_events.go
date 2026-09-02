package page

import (
	"context"
	"log/slog"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type contentUpdatedFieldSpec struct {
	path string
	kind managev1.ContentUpdatedFieldKind
}

type contentUpdatedLocaleState struct {
	locale         string
	exists         bool
	targetRevision *string
}

var pageContentUpdatedFieldSpecs = map[string]contentUpdatedFieldSpec{
	"title":               {path: "title", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"summary":             {path: "summary", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"content":             {path: "document.content", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"featuredImageFileId": {path: "media.featured_image", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA},
	"slug":                {path: "settings.slug", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"status":              {path: "state.status", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"showTitle":           {path: "settings.show_title", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"documentLayout":      {path: "settings.document_layout", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"sourceLocale":        {path: "settings.source_locale", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
}

func publishContentUpdatedEvent(ctx context.Context, publisher AsyncPublisher, event *managev1.ContentUpdatedEvent) error {
	if publisher == nil || event == nil {
		return nil
	}
	err := publisher.NotifyProtobuf(ctx, eventpkg.SignalContentUpdated, event)
	if err != nil {
		slog.Warn("Failed to publish Page content updated event", "error", err, "entityId", event.EntityId)
	}
	return err
}

func buildPageContentUpdatedEvent(
	pageID string,
	fields []string,
	revision string,
	contributors []string,
	source managev1.ContentUpdateSource,
	locale string,
	localeExists bool,
	targetRevision *string,
	documentStateChanged bool,
) *managev1.ContentUpdatedEvent {
	if pageID == "" {
		return nil
	}
	return buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		pageID,
		source,
		buildContentUpdatedFields(fields, pageContentUpdatedFieldSpecs),
		documentStateChanged,
		revision,
		contributors,
		&contentUpdatedLocaleState{locale: locale, exists: localeExists, targetRevision: targetRevision},
	)
}

func buildManagePageContentUpdatedEvent(request *managev1.UpdatePageRequest) *managev1.ContentUpdatedEvent {
	if request == nil {
		return nil
	}
	fields := make([]string, 0, 2)
	if request.Slug != nil {
		fields = append(fields, "slug")
	}
	if request.ShowTitle != nil {
		fields = append(fields, "showTitle")
	}
	return buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		request.Id,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		buildContentUpdatedFields(fields, pageContentUpdatedFieldSpecs),
		false, "", nil, nil,
	)
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
	return buildContentUpdatedEvent(entityType, entityID,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		buildContentUpdatedFields(paths, specs), false, "", nil, nil)
}

func buildManageMediaMutationContentUpdatedEvent(
	entityType managev1.ContentEntityType,
	entityID string,
	path string,
) *managev1.ContentUpdatedEvent {
	return buildContentUpdatedEvent(entityType, entityID,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		[]*managev1.ContentUpdatedField{{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA}},
		false, "", nil, nil)
}

func buildContentUpdatedEvent(
	entityType managev1.ContentEntityType,
	entityID string,
	source managev1.ContentUpdateSource,
	fields []*managev1.ContentUpdatedField,
	documentChanged bool,
	documentRevision string,
	contributors []string,
	localeState *contentUpdatedLocaleState,
) *managev1.ContentUpdatedEvent {
	if len(fields) == 0 && !documentChanged {
		return nil
	}
	if localeState != nil {
		localeState.locale = strings.TrimSpace(localeState.locale)
		if localeState.locale == "" || strings.TrimSpace(documentRevision) == "" {
			return nil
		}
		switch {
		case !localeState.exists:
			if localeState.targetRevision != nil || documentChanged {
				return nil
			}
		case localeState.targetRevision != nil:
			if strings.TrimSpace(*localeState.targetRevision) == "" || documentChanged {
				return nil
			}
		case !documentChanged:
			return nil
		}
	}
	event := &managev1.ContentUpdatedEvent{
		EntityType: entityType, EntityId: entityID, Source: source,
		ChangedFields: fields, ContributorMemberIds: normalizeContributorMemberIDs(contributors),
		DocumentStateChanged: documentChanged, TimestampMs: time.Now().UnixMilli(),
	}
	if documentRevision != "" {
		event.DocumentRevision = &documentRevision
	}
	if localeState != nil {
		event.Locale = &localeState.locale
		event.LocaleExists = &localeState.exists
		if localeState.exists && localeState.targetRevision != nil {
			event.TargetRevision = localeState.targetRevision
		}
	}
	return event
}

func buildContentUpdatedFields(fields []string, specs map[string]contentUpdatedFieldSpec) []*managev1.ContentUpdatedField {
	result := make([]*managev1.ContentUpdatedField, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		spec, exists := specs[field]
		if !exists {
			spec = contentUpdatedFieldSpec{path: field, kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION}
		}
		if _, exists := seen[spec.path]; exists {
			continue
		}
		seen[spec.path] = struct{}{}
		result = append(result, &managev1.ContentUpdatedField{Path: spec.path, Kind: spec.kind})
	}
	return result
}
