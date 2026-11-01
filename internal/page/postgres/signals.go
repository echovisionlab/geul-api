package pagepostgres

import (
	"context"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/page"
)

func (s *store) PublishContentUpdate(ctx context.Context, update page.ContentUpdate) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, s.ports.Signals, contentUpdatedEvent(update))
}

// contentUpdatedEvent renders a Page's content.updated signal; nil when the
// update describes no observable change.
func contentUpdatedEvent(update page.ContentUpdate) *managev1.ContentUpdatedEvent {
	switch update.Kind {
	case page.SettingsUpdate:
		return buildManagePageContentUpdatedEvent(update.PageID, update.Fields)
	case page.StateUpdate:
		return buildManageStateTransitionContentUpdatedEvent(update.PageID, update.Fields)
	case page.FeaturedImageUpdate:
		return buildManageMediaMutationContentUpdatedEvent(
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE, update.PageID, "media.featured_image",
		)
	case page.LocaleUpdate:
		change := update.Locale
		return buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID: update.PageID, Fields: change.Fields, DocumentRevision: change.DocumentRevision,
			Contributors: change.Contributors, Source: updateSource(change.Source), Locale: change.Locale,
			LocaleDeleted: change.LocaleDeleted, TargetRevision: change.TargetRevision,
			DocumentStateChanged: change.DocumentStateChanged,
		})
	case page.LayoutUpdate:
		return buildContentUpdatedEvent(
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
			update.PageID,
			managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			buildContentUpdatedFields([]string{"documentLayout"}, pageContentUpdatedFieldSpecs),
			false,
			update.Locale.DocumentRevision,
			update.Locale.Contributors,
			nil,
		)
	default:
		return nil
	}
}

func updateSource(source page.UpdateSource) managev1.ContentUpdateSource {
	switch source {
	case page.SourceManage:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE
	case page.SourceCollaboration:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB
	case page.SourceAI:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI
	default:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED
	}
}

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
	"accessPolicy":        {path: "settings.access_policy", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"sourceLocale":        {path: "settings.source_locale", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
}

func buildPageLocaleContentUpdatedEvent(change contentsignal.LocaleChange) *managev1.ContentUpdatedEvent {
	if change.EntityID == "" {
		return nil
	}
	return buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		change.EntityID,
		change.Source,
		buildContentUpdatedFields(change.Fields, pageContentUpdatedFieldSpecs),
		change.DocumentStateChanged,
		change.DocumentRevision,
		change.Contributors,
		&contentUpdatedLocaleState{locale: change.Locale, exists: !change.LocaleDeleted, targetRevision: change.TargetRevision},
	)
}

func buildManagePageContentUpdatedEvent(pageID string, fields []string) *managev1.ContentUpdatedEvent {
	return buildContentUpdatedEvent(
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		pageID,
		managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		buildContentUpdatedFields(fields, pageContentUpdatedFieldSpecs),
		false, "", nil, nil,
	)
}

func buildManageStateTransitionContentUpdatedEvent(
	entityID string,
	paths []string,
) *managev1.ContentUpdatedEvent {
	specs := map[string]contentUpdatedFieldSpec{
		"state.status":       {path: "state.status", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
		"state.published_at": {path: "state.published_at", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	}
	return buildContentUpdatedEvent(managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE, entityID,
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
		ChangedFields: fields, ContributorMemberIds: page.NormalizeContributorMemberIDs(contributors),
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
