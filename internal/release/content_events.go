package release

import "context"

// FieldKind classifies a changed field of a content.updated signal.
type FieldKind uint8

const (
	FieldText FieldKind = iota + 1
	FieldConfiguration
	FieldState
	FieldRelation
	FieldMedia
)

// ChangedField is one changed field of a content.updated signal.
type ChangedField struct {
	Path string
	Kind FieldKind
}

// UpdateSource names the surface a content change came from.
type UpdateSource uint8

const (
	SourceManage UpdateSource = iota + 1
	SourceAI
)

// ContentUpdate is one content.updated signal of a Release the Outbox step
// schedules. The locale fields describe an AI document change.
type ContentUpdate struct {
	ReleaseID            string
	Source               UpdateSource
	Fields               []ChangedField
	ContributorMemberIDs []string
	DocumentStateChanged bool
	DocumentRevision     *string
	Locale               *string
	LocaleExists         *bool
	TargetRevision       *string
}

type releaseContentUpdatedFieldSpec struct {
	path string
	kind FieldKind
}

var releaseContentUpdatedFieldSpecs = map[string]releaseContentUpdatedFieldSpec{
	"title":           {path: "title", kind: FieldText},
	"description":     {path: "summary", kind: FieldText},
	"type":            {path: "settings.type", kind: FieldConfiguration},
	"releaseDate":     {path: "state.release_date", kind: FieldState},
	"catalogNumber":   {path: "settings.catalog_number", kind: FieldConfiguration},
	"spotifyUrl":      {path: "links.spotify", kind: FieldConfiguration},
	"appleMusicUrl":   {path: "links.apple_music", kind: FieldConfiguration},
	"bandcampUrl":     {path: "links.bandcamp", kind: FieldConfiguration},
	"youtubeMusicUrl": {path: "links.youtube_music", kind: FieldConfiguration},
	"artists":         {path: "relations.artists", kind: FieldRelation},
	"credits":         {path: "relations.credits", kind: FieldRelation},
	"labels":          {path: "relations.labels", kind: FieldRelation},
	"categoryIds":     {path: "relations.categories", kind: FieldRelation},
	"genreIds":        {path: "relations.genres", kind: FieldRelation},
	"styleIds":        {path: "relations.styles", kind: FieldRelation},
	"formats":         {path: "relations.formats", kind: FieldRelation},
	"tracks":          {path: "document.tracks", kind: FieldText},
	"slug":            {path: "settings.slug", kind: FieldConfiguration},
	"status":          {path: "state.status", kind: FieldState},
	"artworkUrl":      {path: "media.artwork", kind: FieldMedia},
}

// publishContentUpdated schedules content.updated in the caller's mutation
// transaction so the signal commits or rolls back with the data. A nil
// update schedules nothing.
func publishContentUpdated(ctx context.Context, store Store, update *ContentUpdate) error {
	if update == nil {
		return nil
	}
	return store.PublishContentUpdate(ctx, *update)
}

func buildReleaseStateTransitionContentUpdatedEvent(releaseID string, paths []string) *ContentUpdate {
	fields := make([]ChangedField, 0, len(paths))
	for _, path := range paths {
		fields = append(fields, ChangedField{Path: path, Kind: FieldState})
	}
	return newReleaseContentUpdatedEvent(releaseID, fields)
}

func buildReleaseMediaMutationContentUpdatedEvent(releaseID string, path string) *ContentUpdate {
	return newReleaseContentUpdatedEvent(releaseID, []ChangedField{{Path: path, Kind: FieldMedia}})
}

func buildReleaseRelationMutationContentUpdatedEvent(releaseID string, paths []string) *ContentUpdate {
	fields := make([]ChangedField, 0, len(paths))
	for _, path := range paths {
		fields = append(fields, ChangedField{Path: path, Kind: FieldRelation})
	}
	return newReleaseContentUpdatedEvent(releaseID, fields)
}

func buildReleaseContentUpdatedEvent(releaseID string, patchMask []string) *ContentUpdate {
	fields := make([]ChangedField, 0, len(patchMask))
	seen := make(map[string]struct{}, len(patchMask))
	for _, key := range patchMask {
		spec, ok := releaseContentUpdatedFieldSpecs[key]
		if !ok {
			spec = releaseContentUpdatedFieldSpec{path: key, kind: FieldConfiguration}
		}
		if _, exists := seen[spec.path]; exists {
			continue
		}
		seen[spec.path] = struct{}{}
		fields = append(fields, ChangedField{Path: spec.path, Kind: spec.kind})
	}
	return newReleaseContentUpdatedEvent(releaseID, fields)
}

func newReleaseContentUpdatedEvent(releaseID string, fields []ChangedField) *ContentUpdate {
	if releaseID == "" || len(fields) == 0 {
		return nil
	}
	return &ContentUpdate{ReleaseID: releaseID, Source: SourceManage, Fields: fields}
}

// releaseUpdatedFields names the settings an update supplies.
func releaseUpdatedFields(command UpdateReleaseCommand) []string {
	fields := make([]string, 0, 7)
	for _, field := range []struct {
		name     string
		supplied bool
	}{
		{"slug", command.Slug != nil},
		{"type", command.Type != nil},
		{"spotifyUrl", command.SpotifyURL != nil},
		{"appleMusicUrl", command.AppleMusicURL != nil},
		{"bandcampUrl", command.BandcampURL != nil},
		{"youtubeMusicUrl", command.YoutubeMusicURL != nil},
		{"catalogNumber", command.CatalogNumber != nil},
		{"releaseDate", command.ReleaseDateChange != ReleaseDateUnchanged},
	} {
		if field.supplied {
			fields = append(fields, field.name)
		}
	}
	return fields
}
