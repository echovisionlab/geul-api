package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/optional"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) PublishContentUpdate(ctx context.Context, update artist.ContentUpdate) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, s.deps.Publisher, contentUpdatedEvent(update))
}

// contentUpdatedEvent renders an Artist's content.updated signal; nil when
// the update describes no observable change.
func contentUpdatedEvent(update artist.ContentUpdate) *managev1.ContentUpdatedEvent {
	switch update.Kind {
	case artist.MediaUpdate:
		return buildManageMediaMutationContentUpdatedEvent(managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST, update.ArtistID, "media.images")
	case artist.StateUpdate:
		return buildManageStateTransitionContentUpdatedEvent(managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST, update.ArtistID, update.StatePaths)
	case artist.LocaleUpdate:
		change := update.Locale
		return buildArtistLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID: update.ArtistID, Fields: change.Fields, DocumentRevision: change.DocumentRevision,
			Contributors: change.Contributors, Source: updateSource(change.Source), Locale: change.Locale,
			LocaleDeleted: change.LocaleDeleted, TargetRevision: change.TargetRevision,
			DocumentStateChanged: change.DocumentStateChanged,
		})
	default:
		return nil
	}
}

func updateSource(source artist.UpdateSource) managev1.ContentUpdateSource {
	switch source {
	case artist.SourceCollaboration:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB
	case artist.SourceAI:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI
	default:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED
	}
}

// buildArtistLocaleContentUpdatedEvent emits the exact locale fence consumed by the
// collaboration runtime. The shared Content Document revision advances only
// for source-locale mutations; target-only changes carry their opaque target
// revision and leave DocumentStateChanged false.
func buildArtistLocaleContentUpdatedEvent(change contentsignal.LocaleChange) *managev1.ContentUpdatedEvent {
	artistID, fields, revision, locale := change.EntityID, change.Fields, change.DocumentRevision, change.Locale
	localeExists, targetRevision, documentStateChanged := !change.LocaleDeleted, change.TargetRevision, change.DocumentStateChanged
	if strings.TrimSpace(artistID) == "" || strings.TrimSpace(revision) == "" || strings.TrimSpace(locale) == "" || len(fields) == 0 {
		return nil
	}
	if !localeExists {
		if targetRevision != nil || documentStateChanged {
			return nil
		}
	} else if targetRevision != nil {
		if strings.TrimSpace(*targetRevision) == "" || documentStateChanged {
			return nil
		}
	} else if !documentStateChanged {
		return nil
	}
	locale = strings.TrimSpace(locale)
	revision = strings.TrimSpace(revision)
	changedFields := artistContentUpdatedFields(fields)
	if len(changedFields) == 0 {
		return nil
	}
	return &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST,
		EntityId:             artistID,
		Source:               change.Source,
		ChangedFields:        changedFields,
		ContributorMemberIds: append([]string(nil), change.Contributors...),
		DocumentStateChanged: documentStateChanged,
		DocumentRevision:     &revision,
		Locale:               &locale,
		LocaleExists:         &localeExists,
		TargetRevision:       optional.Clone(targetRevision),
		TimestampMs:          time.Now().UnixMilli(),
	}
}

func artistContentUpdatedFields(fields []string) []*managev1.ContentUpdatedField {
	result := make([]*managev1.ContentUpdatedField, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		path := strings.TrimSpace(field)
		if path == "" {
			continue
		}
		if path == "content" {
			path = "document.content"
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		kind := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT
		if path != "document.content" && path != "title" {
			kind = managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION
		}
		result = append(result, &managev1.ContentUpdatedField{Path: path, Kind: kind})
	}
	return result
}

func buildContentUpdatedEvent(entityType managev1.ContentEntityType, entityID string, fields []*managev1.ContentUpdatedField) *managev1.ContentUpdatedEvent {
	if entityID == "" || len(fields) == 0 {
		return nil
	}
	return &managev1.ContentUpdatedEvent{EntityType: entityType, EntityId: entityID, Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, ChangedFields: fields, TimestampMs: time.Now().UnixMilli()}
}

func buildManageStateTransitionContentUpdatedEvent(entityType managev1.ContentEntityType, entityID string, paths []string) *managev1.ContentUpdatedEvent {
	fields := make([]*managev1.ContentUpdatedField, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		fields = append(fields, &managev1.ContentUpdatedField{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE})
	}
	return buildContentUpdatedEvent(entityType, entityID, fields)
}

func buildManageMediaMutationContentUpdatedEvent(entityType managev1.ContentEntityType, entityID, path string) *managev1.ContentUpdatedEvent {
	return buildContentUpdatedEvent(entityType, entityID, []*managev1.ContentUpdatedField{{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA}})
}
