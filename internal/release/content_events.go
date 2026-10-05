package release

import (
	"context"
	"log/slog"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type releaseContentUpdatedFieldSpec struct {
	path string
	kind managev1.ContentUpdatedFieldKind
}

var releaseContentUpdatedFieldSpecs = map[string]releaseContentUpdatedFieldSpec{
	"title":           {path: "title", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"description":     {path: "summary", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"type":            {path: "settings.type", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"releaseDate":     {path: "state.release_date", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"catalogNumber":   {path: "settings.catalog_number", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"spotifyUrl":      {path: "links.spotify", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"appleMusicUrl":   {path: "links.apple_music", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"bandcampUrl":     {path: "links.bandcamp", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"youtubeMusicUrl": {path: "links.youtube_music", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"artists":         {path: "relations.artists", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"credits":         {path: "relations.credits", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"labels":          {path: "relations.labels", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"categoryIds":     {path: "relations.categories", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"genreIds":        {path: "relations.genres", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"styleIds":        {path: "relations.styles", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"formats":         {path: "relations.formats", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION},
	"tracks":          {path: "document.tracks", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT},
	"slug":            {path: "settings.slug", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION},
	"status":          {path: "state.status", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE},
	"artworkUrl":      {path: "media.artwork", kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA},
}

func publishContentUpdatedEvent(ctx context.Context, publisher AsyncPublisher, event *managev1.ContentUpdatedEvent) error {
	if publisher == nil || event == nil {
		return nil
	}
	err := publishSignalProto(ctx, publisher, eventpkg.SignalContentUpdated, event)
	if err != nil {
		slog.Warn("Failed to publish Release content updated event", "error", err, "releaseId", event.EntityId)
	}
	return err
}

func buildManageReleaseContentUpdatedEvent(request *managev1.UpdateReleaseRequest) *managev1.ContentUpdatedEvent {
	if request == nil {
		return nil
	}
	return buildReleaseContentUpdatedEvent(request.Id, releaseUpdatedFields(request))
}

func buildReleaseStateTransitionContentUpdatedEvent(releaseID string, paths []string) *managev1.ContentUpdatedEvent {
	fields := make([]*managev1.ContentUpdatedField, 0, len(paths))
	for _, path := range paths {
		fields = append(fields, &managev1.ContentUpdatedField{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE})
	}
	return newReleaseContentUpdatedEvent(releaseID, fields)
}

func buildReleaseMediaMutationContentUpdatedEvent(releaseID string, path string) *managev1.ContentUpdatedEvent {
	return newReleaseContentUpdatedEvent(releaseID, []*managev1.ContentUpdatedField{{
		Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA,
	}})
}

func buildReleaseRelationMutationContentUpdatedEvent(releaseID string, paths []string) *managev1.ContentUpdatedEvent {
	fields := make([]*managev1.ContentUpdatedField, 0, len(paths))
	for _, path := range paths {
		fields = append(fields, &managev1.ContentUpdatedField{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_RELATION})
	}
	return newReleaseContentUpdatedEvent(releaseID, fields)
}

func buildReleaseContentUpdatedEvent(releaseID string, patchMask []string) *managev1.ContentUpdatedEvent {
	fields := make([]*managev1.ContentUpdatedField, 0, len(patchMask))
	seen := make(map[string]struct{}, len(patchMask))
	for _, key := range patchMask {
		spec, ok := releaseContentUpdatedFieldSpecs[key]
		if !ok {
			spec = releaseContentUpdatedFieldSpec{path: key, kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION}
		}
		if _, exists := seen[spec.path]; exists {
			continue
		}
		seen[spec.path] = struct{}{}
		fields = append(fields, &managev1.ContentUpdatedField{Path: spec.path, Kind: spec.kind})
	}
	return newReleaseContentUpdatedEvent(releaseID, fields)
}

func newReleaseContentUpdatedEvent(releaseID string, fields []*managev1.ContentUpdatedField) *managev1.ContentUpdatedEvent {
	if releaseID == "" || len(fields) == 0 {
		return nil
	}
	return &managev1.ContentUpdatedEvent{
		EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE,
		EntityId:   releaseID, Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		ChangedFields: fields, TimestampMs: time.Now().UnixMilli(),
	}
}

func releaseUpdatedFields(request *managev1.UpdateReleaseRequest) []string {
	fields := make([]string, 0, 7)
	if request.Slug != nil {
		fields = append(fields, "slug")
	}
	if request.Type != nil {
		fields = append(fields, "type")
	}
	if request.SpotifyUrl != nil {
		fields = append(fields, "spotifyUrl")
	}
	if request.AppleMusicUrl != nil {
		fields = append(fields, "appleMusicUrl")
	}
	if request.BandcampUrl != nil {
		fields = append(fields, "bandcampUrl")
	}
	if request.YoutubeMusicUrl != nil {
		fields = append(fields, "youtubeMusicUrl")
	}
	if request.CatalogNumber != nil {
		fields = append(fields, "catalogNumber")
	}
	return fields
}
