package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolReleaseCreate         = "release_create"
	ToolReleaseSettingsGet    = "release_settings_get"
	ToolReleaseSettingsUpdate = "release_settings_update"
	ToolReleasePublish        = "release_publish"
	ToolReleaseUnpublish      = "release_unpublish"
	ToolReleaseDelete         = "release_delete"
	ToolReleaseArtworkSet     = "release_artwork_set"
	ToolReleaseArtworkRemove  = "release_artwork_remove"
	ToolReleaseSlugCheck      = "release_slug_check"
)

var releaseManagementTools = []mcpserver.Tool{
	contentToolWithOutput(ToolReleaseCreate, "Create Release", "Create a draft Release with an empty typed document in the explicit source locale. Use document_open and document_read before adding body text with document_apply. Edit the locale-owned title with document_metadata_update. Resolve related artist, label, genre, style, format, and credit references before using the Release relation tools.", releaseCreateInputJSONSchema, releaseSettingsOutputJSONSchema, false),
	oauthTool(ToolReleaseSettingsGet, "Get Release settings", "Read authorized Release root settings and its current content document revision without returning the body or media delivery URLs. Use document_open and document_read for localized title and body editing, and release_relations_get for relation observations.", contentIDInputJSONSchema, releaseSettingsOutputJSONSchema, true, false),
	contentToolWithOutput(ToolReleaseSettingsUpdate, "Update Release settings", "Update Release slug, type, catalog number, release date, or streaming URLs through the existing Release service. Read the locale-owned title with document_read and edit it with document_metadata_update using the exact current revision. Use Release relation tools for relations. The content document revision is not a settings compare-and-set token.", releaseSettingsUpdateInputJSONSchema, releaseMutationOutputJSONSchema, true),
	contentToolWithOutput(ToolReleasePublish, "Publish Release", "Publish a draft Release through the existing lifecycle. Publishing an already published Release returns changed=false.", contentIDInputJSONSchema, releaseMutationOutputJSONSchema, true),
	contentToolWithOutput(ToolReleaseUnpublish, "Unpublish Release", "Move a published Release back to draft through the existing lifecycle. A Release already in draft returns changed=false.", contentIDInputJSONSchema, releaseMutationOutputJSONSchema, true),
	contentToolWithOutput(ToolReleaseDelete, "Delete Release", "Permanently delete a Release and its owned state when allowed by the existing Release service.", contentIDInputJSONSchema, releaseMutationOutputJSONSchema, true),
	contentToolWithOutput(ToolReleaseArtworkSet, "Set Release artwork", "Reuse an existing attachable File as Release artwork through the existing Release service. Resolve file_id with file_list. This changes the artwork relation without uploading or deleting File bytes. success reports an accepted native operation; it does not distinguish a changed relation from an existing attachment.", releaseArtworkSetInputJSONSchema, releaseArtworkOutputJSONSchema, true),
	contentToolWithOutput(ToolReleaseArtworkRemove, "Remove Release artwork", "Remove the Release artwork relation through the existing Release service without deleting File bytes. success also covers an already absent artwork relation.", contentIDInputJSONSchema, releaseArtworkOutputJSONSchema, true),
	oauthTool(ToolReleaseSlugCheck, "Check Release slug", "Check Release route availability through the existing authorized service. Supply exclude_document_id when checking the current Release's slug. Availability is advisory; creation or update checks again when writing.", releaseSlugCheckInputJSONSchema, releaseSlugCheckOutputJSONSchema, true, false),
}

type ReleaseManagementApplication interface {
	GetRelease(context.Context, *connect.Request[managev1.GetReleaseRequest]) (*connect.Response[managev1.Release], error)
	CreateRelease(context.Context, *connect.Request[managev1.CreateReleaseRequest]) (*connect.Response[managev1.Release], error)
	UpdateRelease(context.Context, *connect.Request[managev1.UpdateReleaseRequest]) (*connect.Response[managev1.UpdateReleaseResponse], error)
	PublishRelease(context.Context, *connect.Request[managev1.PublishReleaseRequest]) (*connect.Response[managev1.ReleaseLifecycleMutationResponse], error)
	UnpublishRelease(context.Context, *connect.Request[managev1.UnpublishReleaseRequest]) (*connect.Response[managev1.ReleaseLifecycleMutationResponse], error)
	DeleteRelease(context.Context, *connect.Request[managev1.DeleteReleaseRequest]) (*connect.Response[managev1.DeleteResponse], error)
	SetReleaseArtwork(context.Context, *connect.Request[managev1.SetReleaseArtworkRequest]) (*connect.Response[managev1.SetReleaseArtworkResponse], error)
	DeleteReleaseArtwork(context.Context, *connect.Request[managev1.DeleteReleaseArtworkRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error)
	CheckReleaseSlugAvailable(context.Context, *connect.Request[managev1.CheckReleaseSlugAvailableRequest]) (*connect.Response[managev1.CheckReleaseSlugAvailableResponse], error)
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

func (*ReleaseManagementTools) ToolNames() []string {
	return toolDefinitionNames(releaseManagementTools)
}

func (*ReleaseManagementTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(releaseManagementTools), nil
}

func (tools *ReleaseManagementTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolReleaseCreate:
		return tools.create(ctx, arguments)
	case ToolReleaseSettingsGet:
		return tools.getSettings(ctx, arguments)
	case ToolReleaseSettingsUpdate:
		return tools.updateSettings(ctx, arguments)
	case ToolReleasePublish, ToolReleaseUnpublish, ToolReleaseDelete:
		return tools.mutateLifecycle(ctx, name, arguments)
	case ToolReleaseArtworkSet:
		return tools.setArtwork(ctx, arguments)
	case ToolReleaseArtworkRemove:
		return tools.removeArtwork(ctx, arguments)
	case ToolReleaseSlugCheck:
		return tools.checkSlug(ctx, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
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

func (tools *ReleaseManagementTools) create(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input releaseCreateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "slug", "catalog_number", "release_date", "spotify_url", "apple_music_url", "bandcamp_url", "youtube_music_url"); err != nil {
		return executionError(err)
	}
	if strings.TrimSpace(input.Title) == "" {
		return executionError(errors.New("title is required"))
	}
	if err := validateCompactLocale(core.Locale(input.SourceLocale)); err != nil {
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
	request := connect.NewRequest(&managev1.CreateReleaseRequest{
		Title: input.Title, SourceLocale: input.SourceLocale, Type: releaseType, Slug: input.Slug,
		CatalogNumber: input.CatalogNumber, ReleaseDate: releaseDate,
		SpotifyUrl: input.SpotifyURL, AppleMusicUrl: input.AppleMusicURL, BandcampUrl: input.BandcampURL, YoutubeMusicUrl: input.YoutubeMusicURL,
	})
	request.Header().Set("Accept-Language", input.SourceLocale)
	created, err := tools.application.CreateRelease(ctx, request)
	if err != nil {
		return expectedToolError(err)
	}
	output := releaseSettingsOutput(created.Msg)
	output["changed"] = true
	return contentResult(output)
}

func (tools *ReleaseManagementTools) getSettings(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	response, err := tools.application.GetRelease(ctx, connect.NewRequest(&managev1.GetReleaseRequest{Id: input.DocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(releaseSettingsOutput(response.Msg))
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

func (tools *ReleaseManagementTools) updateSettings(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input releaseSettingsArguments
	if err := decodeArguments(arguments, &input); err != nil {
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
	request := &managev1.UpdateReleaseRequest{
		Id: input.DocumentID, Slug: input.Slug, CatalogNumber: input.CatalogNumber,
		SpotifyUrl: input.SpotifyURL, AppleMusicUrl: input.AppleMusicURL, BandcampUrl: input.BandcampURL, YoutubeMusicUrl: input.YoutubeMusicURL,
	}
	if input.Type != nil {
		parsed, err := parseReleaseType(*input.Type)
		if err != nil {
			return executionError(err)
		}
		request.Type = &parsed
	}
	date, err := parseReleaseDate(input.ReleaseDate)
	if err != nil {
		return executionError(err)
	}
	if date != nil {
		request.ReleaseDateChange = &managev1.UpdateReleaseRequest_SetReleaseDate{SetReleaseDate: date}
	} else if input.ClearReleaseDate {
		request.ReleaseDateChange = &managev1.UpdateReleaseRequest_ClearReleaseDate{ClearReleaseDate: &emptypb.Empty{}}
	}
	response, err := tools.application.UpdateRelease(ctx, connect.NewRequest(request))
	if err != nil {
		return expectedToolError(err)
	}
	output := map[string]any{"document_type": "release", "document_id": response.Msg.Id, "changed": response.Msg.Changed, "updated_at": timestampString(response.Msg.UpdatedAt)}
	if response.Msg.ReleaseDate != nil {
		output["release_date"] = timestampString(response.Msg.ReleaseDate)
	}
	return contentResult(output)
}

func (tools *ReleaseManagementTools) mutateLifecycle(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if name == ToolReleaseDelete {
		response, err := tools.application.DeleteRelease(ctx, connect.NewRequest(&managev1.DeleteReleaseRequest{Id: input.DocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "release", "document_id": input.DocumentID, "changed": response.Msg.Success, "deleted": response.Msg.Success})
	}
	var response *connect.Response[managev1.ReleaseLifecycleMutationResponse]
	var err error
	if name == ToolReleasePublish {
		response, err = tools.application.PublishRelease(ctx, connect.NewRequest(&managev1.PublishReleaseRequest{Id: input.DocumentID}))
	} else {
		response, err = tools.application.UnpublishRelease(ctx, connect.NewRequest(&managev1.UnpublishReleaseRequest{Id: input.DocumentID}))
	}
	if err != nil {
		return expectedToolError(err)
	}
	output := map[string]any{
		"document_type": "release", "document_id": response.Msg.Id, "changed": response.Msg.Changed,
		"status": contentStatus(response.Msg.Status.String(), "RELEASE_STATUS_"), "updated_at": timestampString(response.Msg.UpdatedAt),
	}
	if response.Msg.PublishedAt != nil {
		output["published_at"] = timestampString(response.Msg.PublishedAt)
	}
	return contentResult(output)
}

type releaseArtworkArguments struct {
	DocumentID string `json:"document_id"`
	FileID     string `json:"file_id"`
}

func (tools *ReleaseManagementTools) setArtwork(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input releaseArtworkArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if err := validateUUID("file_id", input.FileID); err != nil {
		return executionError(err)
	}
	response, err := tools.application.SetReleaseArtwork(ctx, connect.NewRequest(&managev1.SetReleaseArtworkRequest{ReleaseId: input.DocumentID, FileId: input.FileID}))
	if err != nil {
		return expectedToolError(err)
	}
	output := map[string]any{"document_type": "release", "document_id": input.DocumentID, "file_id": input.FileID, "success": true, "og_generation_run_id": optionalStringValue(response.Msg.OgGenerationRunId)}
	if response.Msg.ArtworkAsset != nil {
		output["artwork_asset_id"] = response.Msg.ArtworkAsset.AssetId
	}
	return contentResult(output)
}

func (tools *ReleaseManagementTools) removeArtwork(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	response, err := tools.application.DeleteReleaseArtwork(ctx, connect.NewRequest(&managev1.DeleteReleaseArtworkRequest{ReleaseId: input.DocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "release", "document_id": input.DocumentID, "success": response.Msg.Success, "og_generation_run_id": optionalStringValue(response.Msg.OgGenerationRunId)})
}

type releaseSlugArguments struct {
	Slug              string  `json:"slug"`
	ExcludeDocumentID *string `json:"exclude_document_id,omitempty"`
}

func (tools *ReleaseManagementTools) checkSlug(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input releaseSlugArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if _, supplied := arguments["slug"]; !supplied {
		return executionError(errors.New("slug is required"))
	}
	if err := rejectNullArguments(arguments, "slug", "exclude_document_id"); err != nil {
		return executionError(err)
	}
	if input.ExcludeDocumentID != nil {
		if err := validateUUID("exclude_document_id", *input.ExcludeDocumentID); err != nil {
			return executionError(err)
		}
	}
	response, err := tools.application.CheckReleaseSlugAvailable(ctx, connect.NewRequest(&managev1.CheckReleaseSlugAvailableRequest{Slug: input.Slug, ExcludeReleaseId: input.ExcludeDocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": "release", "slug": input.Slug, "available": response.Msg.Available})
}

func parseReleaseType(value string) (managev1.ReleaseType, error) {
	switch value {
	case "album":
		return managev1.ReleaseType_RELEASE_TYPE_ALBUM, nil
	case "ep":
		return managev1.ReleaseType_RELEASE_TYPE_EP, nil
	case "single":
		return managev1.ReleaseType_RELEASE_TYPE_SINGLE, nil
	case "compilation":
		return managev1.ReleaseType_RELEASE_TYPE_COMPILATION, nil
	default:
		return 0, errors.New("type must be album, ep, single, or compilation")
	}
}

func parseReleaseDate(value *string) (*timestamppb.Timestamp, error) {
	if value == nil {
		return nil, nil
	}
	date, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return nil, fmt.Errorf("release_date must be an RFC 3339 instant with an explicit offset: %w", err)
	}
	return timestamppb.New(date), nil
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
