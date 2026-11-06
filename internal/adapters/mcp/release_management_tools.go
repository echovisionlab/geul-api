package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolReleaseCreate         = "release_create"
	ToolReleaseSettingsUpdate = "release_settings_update"
	ToolReleaseArtworkSet     = "release_artwork_set"
	ToolReleaseArtworkRemove  = "release_artwork_remove"
)

var releaseManagementTools = toolRegistry[ReleaseManagementTools]{
	defineTool(oauthTool(ToolReleaseCreate, "Create Release", "Create a draft Release with an empty typed document in the explicit source locale. Use document_read before adding body text with document_apply. Edit the locale-owned title with document_metadata_update. Resolve related artist, label, genre, style, format, and credit references before using the Release relation tools.", releaseCreateInputJSONSchema, releaseSettingsOutputJSONSchema, false, false), (*ReleaseManagementTools).create),
	defineTool(oauthTool(ToolReleaseSettingsUpdate, "Update Release settings", "Update Release slug, type, catalog number, release date, or streaming URLs through the existing Release service. Read the locale-owned title with document_read and edit it with document_metadata_update using the exact current revision. Use Release relation tools for relations. The content document revision is not a settings compare-and-set token.", releaseSettingsUpdateInputJSONSchema, releaseMutationOutputJSONSchema, false, true), (*ReleaseManagementTools).updateSettings),
	defineTool(oauthTool(ToolReleaseArtworkSet, "Set Release artwork", "Reuse an existing attachable File as Release artwork through the existing Release service. Resolve file_id with file_list. This changes the artwork relation without uploading or deleting File bytes. success reports an accepted native operation; it does not distinguish a changed relation from an existing attachment.", releaseArtworkSetInputJSONSchema, releaseArtworkOutputJSONSchema, false, true), (*ReleaseManagementTools).setArtwork),
	defineTool(oauthTool(ToolReleaseArtworkRemove, "Remove Release artwork", "Remove the Release artwork relation through the existing Release service without deleting File bytes. success also covers an already absent artwork relation.", contentIDInputJSONSchema, releaseArtworkOutputJSONSchema, false, true), (*ReleaseManagementTools).removeArtwork),
}

type ReleaseManagementApplication interface {
	CreateRelease(context.Context, releasedomain.CreateReleaseCommand) (releasedomain.View, error)
	UpdateRelease(context.Context, releasedomain.UpdateReleaseCommand) (releasedomain.ReleaseUpdate, error)
	SetReleaseArtwork(context.Context, releasedomain.SetReleaseArtworkCommand) (*mediaasset.Reference, error)
	DeleteReleaseArtwork(context.Context, releasedomain.DeleteReleaseArtworkCommand) error
}

type ReleaseManagementTools struct {
	application ReleaseManagementApplication
}

func NewReleaseManagementTools(application ReleaseManagementApplication) (*ReleaseManagementTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Release management application is required")
	}
	return &ReleaseManagementTools{application: application}, nil
}

func (*ReleaseManagementTools) ToolNames() []string { return releaseManagementTools.names() }

func (*ReleaseManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return releaseManagementTools.list(), nil
}

func (tools *ReleaseManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return releaseManagementTools.call(tools, ctx, name, arguments)
}

type releaseCreateArguments struct {
	Title           string  `json:"title"`
	SourceLocale    string  `json:"source_locale"`
	Type            string  `json:"type"`
	Slug            *string `json:"slug,omitempty"`
	CatalogNumber   *string `json:"catalog_number,omitempty"`
	ReleaseDate     *string `json:"release_date,omitempty"`
	SpotifyURL      *string `json:"spotify_url,omitempty"`
	AppleMusicURL   *string `json:"apple_music_url,omitempty"`
	BandcampURL     *string `json:"bandcamp_url,omitempty"`
	YoutubeMusicURL *string `json:"youtube_music_url,omitempty"`
}

func (tools *ReleaseManagementTools) create(ctx context.Context, _ string, call toolCall[releaseCreateArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input releaseCreateArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "slug", "catalog_number", "release_date", "spotify_url", "apple_music_url", "bandcamp_url", "youtube_music_url"); err != nil {
		return executionError(err)
	}
	if strings.TrimSpace(input.Title) == "" {
		return executionError(errors.New("title is required"))
	}
	if err := validateLocaleCode(core.Locale(input.SourceLocale)); err != nil {
		return executionError(fmt.Errorf("source_locale: %w", err))
	}
	releaseType, err := parseReleaseType(input.Type)
	if err != nil {
		return executionError(err)
	}
	releaseDate, err := parseReleaseDate(input.ReleaseDate)
	if err != nil {
		return executionError(err)
	}
	created, err := tools.application.CreateRelease(ctx, releasedomain.CreateReleaseCommand{
		Title: input.Title, SourceLocale: input.SourceLocale, AcceptLanguage: input.SourceLocale, Type: releaseType,
		Slug: input.Slug, CatalogNumber: input.CatalogNumber, ReleaseDate: releaseDate,
		SpotifyURL: input.SpotifyURL, AppleMusicURL: input.AppleMusicURL, BandcampURL: input.BandcampURL, YoutubeMusicURL: input.YoutubeMusicURL,
	})
	if err != nil {
		return expectedToolError(err)
	}
	output := releaseSettingsOutput(releasedomain.ReleaseMessage(created))
	output["changed"] = true
	return contentResult(output)
}

type releaseSettingsArguments struct {
	DocumentID       string  `json:"document_id"`
	Slug             *string `json:"slug,omitempty"`
	Type             *string `json:"type,omitempty"`
	CatalogNumber    *string `json:"catalog_number,omitempty"`
	ReleaseDate      *string `json:"release_date,omitempty"`
	ClearReleaseDate bool    `json:"clear_release_date,omitempty"`
	SpotifyURL       *string `json:"spotify_url,omitempty"`
	AppleMusicURL    *string `json:"apple_music_url,omitempty"`
	BandcampURL      *string `json:"bandcamp_url,omitempty"`
	YoutubeMusicURL  *string `json:"youtube_music_url,omitempty"`
}

func (tools *ReleaseManagementTools) updateSettings(ctx context.Context, _ string, call toolCall[releaseSettingsArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input releaseSettingsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "slug", "type", "catalog_number", "release_date", "clear_release_date", "spotify_url", "apple_music_url", "bandcamp_url", "youtube_music_url"); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if input.Slug == nil && input.Type == nil && input.CatalogNumber == nil && input.ReleaseDate == nil && !input.ClearReleaseDate && input.SpotifyURL == nil && input.AppleMusicURL == nil && input.BandcampURL == nil && input.YoutubeMusicURL == nil {
		return executionError(errors.New("at least one Release setting is required"))
	}
	if input.ReleaseDate != nil && input.ClearReleaseDate {
		return executionError(errors.New("release_date and clear_release_date cannot both be set"))
	}
	command := releasedomain.UpdateReleaseCommand{
		ID: input.DocumentID, Slug: input.Slug, CatalogNumber: input.CatalogNumber,
		SpotifyURL: input.SpotifyURL, AppleMusicURL: input.AppleMusicURL, BandcampURL: input.BandcampURL, YoutubeMusicURL: input.YoutubeMusicURL,
	}
	if input.Type != nil {
		parsed, err := parseReleaseType(*input.Type)
		if err != nil {
			return executionError(err)
		}
		command.Type = &parsed
	}
	date, err := parseReleaseDate(input.ReleaseDate)
	if err != nil {
		return executionError(err)
	}
	if date != nil {
		command.ReleaseDateChange, command.ReleaseDate = releasedomain.ReleaseDateSet, date
	} else if input.ClearReleaseDate {
		command.ReleaseDateChange = releasedomain.ReleaseDateCleared
	}
	update, err := tools.application.UpdateRelease(ctx, command)
	if err != nil {
		return expectedToolError(err)
	}
	output := map[string]any{"document_type": "release", "document_id": update.ID, "changed": update.Changed, "updated_at": timestampString(timestamppb.New(update.UpdatedAt))}
	if update.ReleaseDate != nil {
		output["release_date"] = timestampString(timestamppb.New(*update.ReleaseDate))
	}
	return contentResult(output)
}

type releaseArtworkArguments struct {
	DocumentID string `json:"document_id"`
	FileID     string `json:"file_id"`
}

func (tools *ReleaseManagementTools) setArtwork(ctx context.Context, _ string, call toolCall[releaseArtworkArguments]) (mcpserver.ToolResult, error) {
	var input releaseArtworkArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if err := validateUUID("file_id", input.FileID); err != nil {
		return executionError(err)
	}
	asset, err := tools.application.SetReleaseArtwork(ctx, releasedomain.SetReleaseArtworkCommand{ReleaseID: input.DocumentID, FileID: input.FileID})
	if err != nil {
		return expectedToolError(err)
	}
	output := map[string]any{"document_type": "release", "document_id": input.DocumentID, "file_id": input.FileID, "success": true}
	if asset != nil {
		output["artwork_asset_id"] = asset.AssetID
	}
	return contentResult(output)
}

func (tools *ReleaseManagementTools) removeArtwork(ctx context.Context, _ string, call toolCall[contentIDArguments]) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if err := tools.application.DeleteReleaseArtwork(ctx, releasedomain.DeleteReleaseArtworkCommand{ReleaseID: input.DocumentID}); err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "release", "document_id": input.DocumentID, "success": true})
}

func parseReleaseType(value string) (releasedomain.Type, error) {
	switch value {
	case "album":
		return releasedomain.TypeAlbum, nil
	case "ep":
		return releasedomain.TypeEP, nil
	case "single":
		return releasedomain.TypeSingle, nil
	case "compilation":
		return releasedomain.TypeCompilation, nil
	default:
		return "", errors.New("type must be album, ep, single, or compilation")
	}
}

// parseReleaseDate reads an RFC 3339 instant as the UTC instant the Release
// stores.
func parseReleaseDate(value *string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	date, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil, fmt.Errorf("release_date must be an RFC 3339 instant with an explicit offset: %w", err)
	}
	date = date.UTC()
	return &date, nil
}

func releaseSettingsOutput(release *managev1.Release) map[string]any {
	output := map[string]any{
		"document_type": "release", "document_id": release.Id, "title": release.Title, "slug": optionalStringValue(release.Slug),
		"source_locale": release.SourceLocale, "type": contentStatus(release.Type.String(), "RELEASE_TYPE_"),
		"status": contentStatus(release.Status, "RELEASE_STATUS_"), "document_revision": release.Revision,
		"catalog_number": optionalStringValue(release.CatalogNumber), "spotify_url": optionalStringValue(release.SpotifyUrl),
		"apple_music_url": optionalStringValue(release.AppleMusicUrl), "bandcamp_url": optionalStringValue(release.BandcampUrl), "youtube_music_url": optionalStringValue(release.YoutubeMusicUrl),
		"updated_at": timestampString(release.UpdatedAt),
	}
	if release.ReleaseDate != nil {
		output["release_date"] = timestampString(release.ReleaseDate)
	}
	if release.PublishedAt != nil {
		output["published_at"] = timestampString(release.PublishedAt)
	}
	if release.ArtworkAsset != nil {
		output["artwork_asset_id"] = release.ArtworkAsset.AssetId
	}
	if release.OgAsset != nil {
		output["og_asset_id"] = release.OgAsset.AssetId
	}
	return output
}

var _ ToolProvider = (*ReleaseManagementTools)(nil)
