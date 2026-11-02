package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"

	releasedomain "github.com/echovisionlab/geul-api/internal/release"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
)

const releaseManagementID = "11111111-1111-4111-8111-111111111111"
const releaseManagementFileID = "22222222-2222-4222-8222-222222222222"
const releaseManagementAssetID = "33333333-3333-4333-8333-333333333333"
const releaseManagementRevision = "44444444-4444-4444-8444-444444444444"

func TestReleaseManagementDescriptorsAndRequiredFields(t *testing.T) {
	var nilOwner *recordingReleaseManagement
	for _, owner := range []ReleaseManagementApplication{nil, nilOwner} {
		if _, err := NewReleaseManagementTools(owner); err == nil {
			t.Fatal("missing owner accepted")
		}
	}
	tools, err := NewReleaseManagementTools(newRecordingReleaseManagement())
	if err != nil {
		t.Fatal(err)
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || len(listed) != 9 {
		t.Fatalf("descriptors = %v, err=%v", listed, err)
	}
	want := []string{ToolReleaseCreate, ToolReleaseSettingsGet, ToolReleaseSettingsUpdate, ToolReleasePublish, ToolReleaseUnpublish, ToolReleaseDelete, ToolReleaseArtworkSet, ToolReleaseArtworkRemove, ToolReleaseSlugCheck}
	if !reflect.DeepEqual(tools.ToolNames(), want) {
		t.Fatalf("tool names = %v", tools.ToolNames())
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		for _, raw := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil || schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("%s schema = %s, err=%v", tool.Name, raw, err)
			}
		}
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(listed[0].InputSchema, &schema); err != nil || !reflect.DeepEqual(schema.Required, []string{"title", "source_locale", "type"}) {
		t.Fatalf("create required = %v, err=%v", schema.Required, err)
	}
}

func TestReleaseManagementCreateExactFieldsAndCompactSettings(t *testing.T) {
	owner := newRecordingReleaseManagement()
	owner.release.Document = &contentv1.RichTextDocument{Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT}
	owner.release.Artwork = &mediaasset.Reference{AssetID: releaseManagementAssetID, URL: "https://private-delivery.test/secret", DownloadFilename: proto.String("private filename")}
	tools, _ := NewReleaseManagementTools(owner)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseCreate, toolArguments(t, `{
  "title":"Album","source_locale":"ko","type":"ep","slug":"album","catalog_number":"CAT-1","release_date":"2026-10-10T00:00:00+09:00",
  "spotify_url":"https://example.test/spotify","apple_music_url":"https://example.test/apple","bandcamp_url":"https://example.test/bandcamp","youtube_music_url":"https://example.test/youtube"
}`))
	if err != nil {
		t.Fatal(err)
	}
	releaseDate := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	want := releasedomain.CreateReleaseCommand{
		Title: "Album", SourceLocale: "ko", AcceptLanguage: "ko", Type: releasedomain.TypeEP, Slug: proto.String("album"), CatalogNumber: proto.String("CAT-1"),
		ReleaseDate: &releaseDate,
		SpotifyURL:  proto.String("https://example.test/spotify"), AppleMusicURL: proto.String("https://example.test/apple"), BandcampURL: proto.String("https://example.test/bandcamp"), YoutubeMusicURL: proto.String("https://example.test/youtube"),
	}
	if !reflect.DeepEqual(owner.create, want) {
		t.Fatalf("native create = %+v; want %+v", owner.create, want)
	}
	if result.StructuredContent["changed"] != true || result.StructuredContent["status"] != "draft" || result.StructuredContent["document_revision"] != releaseManagementRevision || result.StructuredContent["artwork_asset_id"] != releaseManagementAssetID {
		t.Fatalf("created result = %#v", result.StructuredContent)
	}
	settings, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseSettingsGet, toolArguments(t, `{"document_id":"`+releaseManagementID+`"}`))
	if err != nil || owner.get.ID != releaseManagementID {
		t.Fatalf("settings result = %#v, err=%v", settings, err)
	}
	encoded, _ := json.Marshal(settings.StructuredContent)
	if settings.StructuredContent["document"] != nil || settings.StructuredContent["artwork_asset"] != nil || settings.StructuredContent["og_asset"] != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private filename") {
		t.Fatalf("settings exposed body or hydrated asset data: %s", encoded)
	}
}

func TestReleaseManagementSettingsNativePointersAndDateOneof(t *testing.T) {
	owner := newRecordingReleaseManagement()
	tools, _ := NewReleaseManagementTools(owner)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseSettingsUpdate, toolArguments(t, `{
  "document_id":"`+releaseManagementID+`","slug":"","type":"single","catalog_number":"","release_date":"2026-10-10T00:00:00+09:00",
  "spotify_url":"","apple_music_url":"","bandcamp_url":"","youtube_music_url":""
}`))
	if err != nil {
		t.Fatal(err)
	}
	single := releasedomain.TypeSingle
	releaseDate := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	want := releasedomain.UpdateReleaseCommand{
		ID: releaseManagementID, Slug: proto.String(""), Type: &single, CatalogNumber: proto.String(""),
		ReleaseDateChange: releasedomain.ReleaseDateSet, ReleaseDate: &releaseDate,
		SpotifyURL: proto.String(""), AppleMusicURL: proto.String(""), BandcampURL: proto.String(""), YoutubeMusicURL: proto.String(""),
	}
	if !reflect.DeepEqual(owner.update, want) {
		t.Fatalf("native settings = %+v; want %+v", owner.update, want)
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseSettingsUpdate, toolArguments(t, `{"document_id":"`+releaseManagementID+`","clear_release_date":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if owner.update.ReleaseDateChange != releasedomain.ReleaseDateCleared || owner.update.ReleaseDate != nil || owner.update.Slug != nil || owner.update.Type != nil {
		t.Fatalf("clear date/presence = %+v", owner.update)
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseSettingsUpdate, toolArguments(t, `{"document_id":"`+releaseManagementID+`","catalog_number":"new"}`))
	if err != nil || owner.update.ReleaseDateChange != releasedomain.ReleaseDateUnchanged {
		t.Fatalf("omitted date acquired mutation: %+v, err=%v", owner.update, err)
	}
}

func TestReleaseManagementArtworkNativeResponsesAndSlugRequest(t *testing.T) {
	owner := newRecordingReleaseManagement()
	tools, _ := NewReleaseManagementTools(owner)
	arguments := toolArguments(t, `{"document_id":"`+releaseManagementID+`","file_id":"`+releaseManagementFileID+`"}`)
	for _, asset := range []*mediaasset.Reference{
		{AssetID: releaseManagementAssetID, URL: "private media delivery"},
		nil, // An existing attachment is a successful native no-op with no AssetRef.
	} {
		owner.artworkAsset = asset
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseArtworkSet, arguments)
		if err != nil {
			t.Fatal(err)
		}
		if owner.artwork.ReleaseID != releaseManagementID || owner.artwork.FileID != releaseManagementFileID || result.StructuredContent["success"] != true || result.StructuredContent["changed"] != nil || result.StructuredContent["file_id"] != releaseManagementFileID || result.StructuredContent["og_generation_run_id"] != nil {
			t.Fatalf("artwork result invented mutation or lost request: %#v", result.StructuredContent)
		}
		encoded, _ := json.Marshal(result.StructuredContent)
		if strings.Contains(string(encoded), "private media delivery") {
			t.Fatalf("artwork delivery data exposed: %s", encoded)
		}
	}
	removed, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseArtworkRemove, toolArguments(t, `{"document_id":"`+releaseManagementID+`"}`))
	if err != nil || owner.removeArtwork.ReleaseID != releaseManagementID || removed.StructuredContent["success"] != true || removed.StructuredContent["og_generation_run_id"] != nil || removed.StructuredContent["changed"] != nil {
		t.Fatalf("artwork removal = %#v, err=%v", removed.StructuredContent, err)
	}
	checked, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseSlugCheck, toolArguments(t, `{"slug":"album","exclude_document_id":"`+releaseManagementID+`"}`))
	if err != nil || owner.slug.Slug != "album" || owner.slug.ExcludeReleaseID == nil || *owner.slug.ExcludeReleaseID != releaseManagementID || checked.StructuredContent["available"] != true {
		t.Fatalf("slug result = %#v, err=%v", checked.StructuredContent, err)
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseSlugCheck, toolArguments(t, `{"slug":""}`))
	if err != nil || owner.slug.Slug != "" || owner.slug.ExcludeReleaseID != nil {
		t.Fatalf("explicit empty slug did not preserve native request: %+v, err=%v", owner.slug, err)
	}
}

func TestReleaseManagementLifecyclePreservesNativeChangesAndStatuses(t *testing.T) {
	owner := newRecordingReleaseManagement()
	tools, _ := NewReleaseManagementTools(owner)
	arguments := toolArguments(t, `{"document_id":"`+releaseManagementID+`"}`)
	for _, test := range []struct {
		tool, status string
		changed      bool
	}{
		{ToolReleasePublish, "published", true}, {ToolReleasePublish, "published", false},
		{ToolReleaseUnpublish, "draft", true}, {ToolReleaseUnpublish, "draft", false},
	} {
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, arguments)
		if err != nil || result.StructuredContent["status"] != test.status || result.StructuredContent["changed"] != test.changed || result.StructuredContent["document_id"] != releaseManagementID {
			t.Fatalf("%s = %#v, err=%v", test.tool, result.StructuredContent, err)
		}
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseDelete, arguments)
	if err != nil || owner.deleted.ID != releaseManagementID || result.StructuredContent["deleted"] != true {
		t.Fatalf("delete = %#v, err=%v", result.StructuredContent, err)
	}
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "release_archive", arguments); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatalf("unsupported native lifecycle accepted: %v", err)
	}
}

func TestReleaseManagementInvalidInputsDoNotReachOwner(t *testing.T) {
	for _, test := range []struct{ tool, arguments string }{
		{ToolReleaseCreate, `{"title":"Album","source_locale":"ko","type":"unknown"}`},
		{ToolReleaseCreate, `{"title":"","source_locale":"ko","type":"album"}`},
		{ToolReleaseCreate, `{"title":"Album","type":"album"}`},
		{ToolReleaseCreate, `{"title":"Album","source_locale":"ko","type":"album","release_date":"2026-10-10"}`},
		{ToolReleaseCreate, `{"title":"Album","source_locale":"ko","type":"album","spotify_url":null}`},
		{ToolReleaseSettingsUpdate, `{"document_id":"` + releaseManagementID + `"}`},
		{ToolReleaseSettingsUpdate, `{"document_id":"` + releaseManagementID + `","type":"unknown"}`},
		{ToolReleaseSettingsUpdate, `{"document_id":"` + releaseManagementID + `","release_date":"2026-10-10T00:00:00Z","clear_release_date":true}`},
		{ToolReleaseSettingsUpdate, `{"document_id":"` + releaseManagementID + `","slug":"album","release_date":null}`},
		{ToolReleaseSettingsUpdate, `{"document_id":"` + releaseManagementID + `","slug":"album","clear_release_date":null}`},
		{ToolReleaseSettingsGet, `{"document_id":"album"}`},
		{ToolReleaseArtworkSet, `{"document_id":"` + releaseManagementID + `","file_id":"not-a-uuid"}`},
		{ToolReleaseSlugCheck, `{"slug":"album","exclude_document_id":"not-a-uuid"}`},
		{ToolReleaseSlugCheck, `{}`},
		{ToolReleaseSlugCheck, `{"slug":null}`},
		{ToolReleaseSlugCheck, `{"slug":"album","exclude_document_id":null}`},
	} {
		t.Run(test.tool+"/"+test.arguments, func(t *testing.T) {
			owner := newRecordingReleaseManagement()
			tools, _ := NewReleaseManagementTools(owner)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.arguments))
			var invalid *mcpserver.ToolExecutionError
			if !errors.As(err, &invalid) || len(owner.calls) != 0 {
				t.Fatalf("invalid input called owner: calls=%v, err=%v", owner.calls, err)
			}
		})
	}
}

func TestReleaseManagementPreservesAuthorityErrorsAndActorContext(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeNotFound, connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeAborted, connect.CodeInternal, connect.CodeUnavailable} {
		for _, name := range []string{ToolReleaseCreate, ToolReleaseSettingsGet, ToolReleaseSettingsUpdate, ToolReleasePublish, ToolReleaseUnpublish, ToolReleaseDelete, ToolReleaseArtworkSet, ToolReleaseArtworkRemove, ToolReleaseSlugCheck} {
			t.Run(code.String()+"/"+name, func(t *testing.T) {
				owner := newRecordingReleaseManagement()
				owner.err = connect.NewError(code, errors.New("owning decision"))
				tools, _ := NewReleaseManagementTools(owner)
				arguments := `{"document_id":"` + releaseManagementID + `"}`
				switch name {
				case ToolReleaseCreate:
					arguments = `{"title":"Album","source_locale":"ko","type":"album"}`
				case ToolReleaseSettingsUpdate:
					arguments = `{"document_id":"` + releaseManagementID + `","catalog_number":"new"}`
				case ToolReleaseArtworkSet:
					arguments = `{"document_id":"` + releaseManagementID + `","file_id":"` + releaseManagementFileID + `"}`
				case ToolReleaseSlugCheck:
					arguments = `{"slug":"album"}`
				}
				ctx := context.WithValue(t.Context(), releaseManagementContextKey{}, "authenticated actor")
				result, err := tools.CallTool(ctx, mcpserver.Principal{}, name, toolArguments(t, arguments))
				if len(owner.calls) != 1 || owner.ctx.Value(releaseManagementContextKey{}) != "authenticated actor" || result.StructuredContent != nil {
					t.Fatalf("authority bypass/leaked result: calls=%v, result=%#v, err=%v", owner.calls, result, err)
				}
				if code == connect.CodeInternal {
					if err != owner.err { //nolint:errorlint // asserts the error is returned unchanged
						t.Fatalf("internal error did not reach generic handler unchanged: %v", err)
					}
					return
				}
				want := "owning decision"
				if code == connect.CodeUnavailable {
					want = "The service is temporarily unavailable"
				}
				var safe *mcpserver.ToolExecutionError
				if !errors.As(err, &safe) || safe.Message != want {
					t.Fatalf("owning error = %v; want %q", err, want)
				}
			})
		}
	}
}

type releaseManagementContextKey struct{}

type recordingReleaseManagement struct {
	release       releasedomain.View
	err           error
	ctx           context.Context
	calls         []string
	create        releasedomain.CreateReleaseCommand
	get           releasedomain.GetReleaseQuery
	update        releasedomain.UpdateReleaseCommand
	deleted       releasedomain.DeleteReleaseCommand
	artwork       releasedomain.SetReleaseArtworkCommand
	removeArtwork releasedomain.DeleteReleaseArtworkCommand
	slug          releasedomain.CheckReleaseSlugQuery
	artworkAsset  *mediaasset.Reference
	status        releasedomain.Status
}

func newRecordingReleaseManagement() *recordingReleaseManagement {
	return &recordingReleaseManagement{
		release: releasedomain.View{Title: "Album", Revision: releaseManagementRevision, Release: releasedomain.Release{
			ID: releaseManagementID, SourceLocale: "ko", Type: releasedomain.TypeAlbum,
			Status: releasedomain.StatusDraft, UpdatedAt: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
		}},
		status: releasedomain.StatusDraft,
	}
}

func (owner *recordingReleaseManagement) record(ctx context.Context, call string) error {
	owner.ctx, owner.calls = ctx, append(owner.calls, call)
	return owner.err
}

func (owner *recordingReleaseManagement) CreateRelease(ctx context.Context, command releasedomain.CreateReleaseCommand) (releasedomain.View, error) {
	owner.create = command
	if err := owner.record(ctx, "create"); err != nil {
		return releasedomain.View{}, err
	}
	return owner.release, nil
}

func (owner *recordingReleaseManagement) GetRelease(ctx context.Context, query releasedomain.GetReleaseQuery) (releasedomain.View, error) {
	owner.get = query
	if err := owner.record(ctx, "get"); err != nil {
		return releasedomain.View{}, err
	}
	return owner.release, nil
}

func (owner *recordingReleaseManagement) UpdateRelease(ctx context.Context, command releasedomain.UpdateReleaseCommand) (releasedomain.ReleaseUpdate, error) {
	owner.update = command
	if err := owner.record(ctx, "update"); err != nil {
		return releasedomain.ReleaseUpdate{}, err
	}
	return releasedomain.ReleaseUpdate{ID: command.ID, Changed: true, UpdatedAt: owner.release.Release.UpdatedAt}, nil
}

func (owner *recordingReleaseManagement) transition(ctx context.Context, call string, id string, next releasedomain.Status) (releasedomain.LifecycleChange, error) {
	if err := owner.record(ctx, call); err != nil {
		return releasedomain.LifecycleChange{}, err
	}
	changed := owner.status != next
	owner.status = next
	return releasedomain.LifecycleChange{ID: id, Changed: changed, Status: next, UpdatedAt: owner.release.Release.UpdatedAt}, nil
}

func (owner *recordingReleaseManagement) PublishRelease(ctx context.Context, command releasedomain.PublishReleaseCommand) (releasedomain.LifecycleChange, error) {
	return owner.transition(ctx, "publish", command.ID, releasedomain.StatusPublished)
}

func (owner *recordingReleaseManagement) UnpublishRelease(ctx context.Context, command releasedomain.UnpublishReleaseCommand) (releasedomain.LifecycleChange, error) {
	return owner.transition(ctx, "unpublish", command.ID, releasedomain.StatusDraft)
}

func (owner *recordingReleaseManagement) DeleteRelease(ctx context.Context, command releasedomain.DeleteReleaseCommand) error {
	owner.deleted = command
	return owner.record(ctx, "delete")
}

func (owner *recordingReleaseManagement) SetReleaseArtwork(ctx context.Context, command releasedomain.SetReleaseArtworkCommand) (*mediaasset.Reference, error) {
	owner.artwork = command
	if err := owner.record(ctx, "artwork_set"); err != nil {
		return nil, err
	}
	return owner.artworkAsset, nil
}

func (owner *recordingReleaseManagement) DeleteReleaseArtwork(ctx context.Context, command releasedomain.DeleteReleaseArtworkCommand) error {
	owner.removeArtwork = command
	return owner.record(ctx, "artwork_remove")
}

func (owner *recordingReleaseManagement) CheckReleaseSlugAvailable(ctx context.Context, query releasedomain.CheckReleaseSlugQuery) (bool, error) {
	owner.slug = query
	if err := owner.record(ctx, "slug_check"); err != nil {
		return false, err
	}
	return true, nil
}
