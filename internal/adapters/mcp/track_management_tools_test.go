package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

var _ TrackManagementApplication = (*releasedomain.TrackService)(nil)

const (
	trackTestID      = "11111111-1111-4111-8111-111111111111"
	trackTestRelease = "22222222-2222-4222-8222-222222222222"
	trackTestCredit  = "33333333-3333-4333-8333-333333333333"
	trackTestArtist  = "44444444-4444-4444-8444-444444444444"
)

type recordingTrackApplication struct {
	err     error
	calls   int
	ctx     context.Context
	list    *managev1.ListTracksByReleaseRequest
	create  *managev1.CreateTrackRequest
	update  *managev1.UpdateTrackRequest
	delete  *managev1.DeleteTrackRequest
	credits *managev1.SetTrackCreditsRequest
	reorder *managev1.ReorderTracksRequest
}

func (a *recordingTrackApplication) record(ctx context.Context) { a.calls++; a.ctx = ctx }
func (a *recordingTrackApplication) ListTracksByRelease(ctx context.Context, request *connect.Request[managev1.ListTracksByReleaseRequest]) (*connect.Response[managev1.ListTracksByReleaseResponse], error) {
	a.record(ctx)
	a.list = request.Msg
	return connect.NewResponse(&managev1.ListTracksByReleaseResponse{Tracks: []*managev1.TrackWithCredits{trackWithCreditsFixture()}}), a.err
}
func (a *recordingTrackApplication) CreateTrack(ctx context.Context, request *connect.Request[managev1.CreateTrackRequest]) (*connect.Response[managev1.Track], error) {
	a.record(ctx)
	a.create = request.Msg
	return connect.NewResponse(trackWithCreditsFixture().Track), a.err
}
func (a *recordingTrackApplication) UpdateTrack(ctx context.Context, request *connect.Request[managev1.UpdateTrackRequest]) (*connect.Response[managev1.Track], error) {
	a.record(ctx)
	a.update = request.Msg
	return connect.NewResponse(trackWithCreditsFixture().Track), a.err
}
func (a *recordingTrackApplication) DeleteTrack(ctx context.Context, request *connect.Request[managev1.DeleteTrackRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	a.record(ctx)
	a.delete = request.Msg
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), a.err
}
func (a *recordingTrackApplication) SetTrackCredits(ctx context.Context, request *connect.Request[managev1.SetTrackCreditsRequest]) (*connect.Response[managev1.TrackWithCredits], error) {
	a.record(ctx)
	a.credits = request.Msg
	return connect.NewResponse(trackWithCreditsFixture()), a.err
}
func (a *recordingTrackApplication) ReorderTracks(ctx context.Context, request *connect.Request[managev1.ReorderTracksRequest]) (*connect.Response[managev1.ListTracksByReleaseResponse], error) {
	a.record(ctx)
	a.reorder = request.Msg
	return connect.NewResponse(&managev1.ListTracksByReleaseResponse{Tracks: []*managev1.TrackWithCredits{trackWithCreditsFixture()}}), a.err
}

func trackWithCreditsFixture() *managev1.TrackWithCredits {
	artist, role, lyrics, status, fileID, private := trackTestArtist, "producer", "lyrics", "completed", trackTestArtist, "private hydrated value"
	duration := int32(0)
	metadata, _ := structpb.NewStruct(map[string]any{"private_metadata": private})
	return &managev1.TrackWithCredits{
		Track:   &managev1.Track{Id: trackTestID, ReleaseId: trackTestRelease, TrackNumber: 2, Title: "Track", DurationSeconds: &duration, Lyrics: &lyrics, ProcessingStatus: &status, AudioOriginalFileId: &fileID, Metadata: metadata},
		Credits: []*managev1.TrackCredit{{Id: trackTestCredit, ArtistId: &artist, ArtistName: &private, ArtistSlug: &private, CreditRole: &role, SortOrder: 0, Member: &commonv1.MemberSummary{Id: private}}},
	}
}

func mustTrackTools(t *testing.T, application *recordingTrackApplication) *TrackManagementTools {
	t.Helper()
	tools, err := NewTrackManagementTools(application)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestTrackManagementDispatchesExactOwningRPCs(t *testing.T) {
	tests := []struct {
		name, input string
		check       func(*testing.T, *recordingTrackApplication, mcpserver.ToolResult)
	}{
		{ToolTrackList, `{"release_id":"` + trackTestRelease + `"}`, func(t *testing.T, a *recordingTrackApplication, result mcpserver.ToolResult) {
			if a.list.ReleaseId != trackTestRelease || len(result.StructuredContent["tracks"].([]any)) != 1 {
				t.Fatalf("list=%+v result=%+v", a.list, result)
			}
		}},
		{ToolTrackCreate, `{"release_id":"` + trackTestRelease + `","title":"New","duration_seconds":0,"lyrics":""}`, func(t *testing.T, a *recordingTrackApplication, result mcpserver.ToolResult) {
			if a.create.ReleaseId != trackTestRelease || a.create.TrackNumber != 0 || a.create.Title != "New" || a.create.DurationSeconds == nil || *a.create.DurationSeconds != 0 || a.create.Lyrics == nil || *a.create.Lyrics != "" {
				t.Fatalf("create=%+v", a.create)
			}
			if result.StructuredContent["credits"] != nil || result.StructuredContent["observed"] != nil {
				t.Fatal("create invented a credit observation")
			}
		}},
		{ToolTrackSettingsUpdate, `{"track_id":"` + trackTestID + `","track_number":5,"title":"","duration_seconds":0,"processing_status":"","lyrics":"","clear_duration":true,"clear_audio_original":true,"clear_lyrics":true}`, func(t *testing.T, a *recordingTrackApplication, result mcpserver.ToolResult) {
			request := a.update
			if request.Id != trackTestID || request.TrackNumber == nil || *request.TrackNumber != 5 || request.Title == nil || *request.Title != "" || request.DurationSeconds == nil || *request.DurationSeconds != 0 || request.ProcessingStatus == nil || *request.ProcessingStatus != "" || request.Lyrics == nil || *request.Lyrics != "" || !request.ClearDuration || !request.ClearAudioOriginal || !request.ClearLyrics {
				t.Fatalf("update lost pointer/clear values: %+v", request)
			}
			if result.StructuredContent["observed"] != nil {
				t.Fatal("update invented a credit observation")
			}
		}},
		{ToolTrackDelete, `{"track_id":"` + trackTestID + `"}`, func(t *testing.T, a *recordingTrackApplication, result mcpserver.ToolResult) {
			if a.delete.Id != trackTestID || result.StructuredContent["success"] != true {
				t.Fatalf("delete=%+v result=%+v", a.delete, result)
			}
		}},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[],"observed":{"credits":[{"id":"` + trackTestCredit + `","artist_id":"` + trackTestArtist + `","member_id":null,"credited_name":null,"credit_role":"producer","sort_order":0}]}}`, func(t *testing.T, a *recordingTrackApplication, _ mcpserver.ToolResult) {
			request := a.credits
			if request.TrackId != trackTestID || len(request.Credits) != 0 || request.Observed == nil || len(request.Observed.Credits) != 1 || request.Observed.Credits[0].GetId() != trackTestCredit || request.Observed.Credits[0].GetArtistId() != trackTestArtist || request.Observed.Credits[0].GetCreditRole() != "producer" {
				t.Fatalf("credits=%+v", request)
			}
		}},
		{ToolTrackReorder, `{"track_ids":["` + trackTestCredit + `","` + trackTestID + `"]}`, func(t *testing.T, a *recordingTrackApplication, _ mcpserver.ToolResult) {
			if !reflect.DeepEqual(a.reorder.TrackIds, []string{trackTestCredit, trackTestID}) {
				t.Fatalf("reorder=%+v", a.reorder)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application := &recordingTrackApplication{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result, err := mustTrackTools(t, application).CallTool(ctx, mcpserver.Principal{}, test.name, toolArguments(t, test.input))
			if err != nil {
				t.Fatal(err)
			}
			if application.calls != 1 || application.ctx != ctx || result.IsError {
				t.Fatalf("dispatch calls=%d context preserved=%v result=%+v", application.calls, application.ctx == ctx, result)
			}
			test.check(t, application, result)
		})
	}
}

func TestTrackManagementSettingsPreserveOmittedValues(t *testing.T) {
	application := &recordingTrackApplication{}
	_, err := mustTrackTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolTrackSettingsUpdate, toolArguments(t, `{"track_id":"`+trackTestID+`","lyrics":""}`))
	if err != nil {
		t.Fatal(err)
	}
	request := application.update
	if request.Lyrics == nil || *request.Lyrics != "" || request.Title != nil || request.DurationSeconds != nil || request.ProcessingStatus != nil || request.TrackNumber != nil || request.ClearLyrics || request.ClearDuration || request.ClearAudioOriginal {
		t.Fatalf("omission became a mutation: %+v", request)
	}
}

func TestTrackListCreditSnapshotRoundTripsWithoutHydratedValues(t *testing.T) {
	application := &recordingTrackApplication{}
	tools := mustTrackTools(t, application)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTrackList, toolArguments(t, `{"release_id":"`+trackTestRelease+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	track := result.StructuredContent["tracks"].([]any)[0].(map[string]any)
	if track["duration_seconds"] != float64(0) || track["lyrics"] != "lyrics" || track["audio_original_file_id"] != trackTestArtist {
		t.Fatalf("editable settings lost: %v", track)
	}
	if !reflect.DeepEqual(track["credits"], track["observed"].(map[string]any)["credits"]) {
		t.Fatal("snapshot differs from observed native credits")
	}
	encoded := result.Content[0]["text"].(string)
	for _, private := range []string{"private hydrated value", "private_metadata", "artist_name", "artist_slug", `"member":`} {
		if strings.Contains(encoded, private) {
			t.Fatalf("projection leaked %s: %s", private, encoded)
		}
	}
	var parity map[string]any
	if err := json.Unmarshal([]byte(encoded), &parity); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parity, result.StructuredContent) {
		t.Fatal("text and structured output differ")
	}
	input, err := json.Marshal(map[string]any{"track_id": trackTestID, "credits": track["credits"], "observed": track["observed"]})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTrackCreditsSet, toolArguments(t, string(input)))
	if err != nil {
		t.Fatal(err)
	}
	if len(application.credits.Credits) != 1 || application.credits.Credits[0].GetId() != trackTestCredit || application.credits.Observed.Credits[0].GetId() != trackTestCredit || application.credits.Observed.Credits[0].MemberId != nil {
		t.Fatalf("snapshot roundtrip=%+v", application.credits)
	}
	empty := trackWithCreditsOutput(&managev1.TrackWithCredits{Track: &managev1.Track{Id: trackTestID, ReleaseId: trackTestRelease}})
	if len(empty["credits"].([]map[string]any)) != 0 || empty["observed"] == nil {
		t.Fatalf("empty snapshot lost: %v", empty)
	}
}

func TestTrackManagementRejectsInvalidInputBeforeOwningRPC(t *testing.T) {
	tests := []struct{ tool, input string }{
		{ToolTrackCreate, `{"release_id":"` + trackTestRelease + `","title":"New","track_number":2}`},
		{ToolTrackCreate, `{"release_id":"` + trackTestRelease + `","title":"New","duration_seconds":2147483648}`},
		{ToolTrackList, `{"release_id":"slug"}`},
		{ToolTrackDelete, `{"track_id":null}`},
		{ToolTrackSettingsUpdate, `{"track_id":"` + trackTestID + `"}`},
		{ToolTrackSettingsUpdate, `{"track_id":"` + trackTestID + `","title":"Changed","lyrics":null}`},
		{ToolTrackSettingsUpdate, `{"track_id":"` + trackTestID + `","track_number":-2147483649}`},
		{ToolTrackSettingsUpdate, `{"track_id":"` + trackTestID + `","audio_original_file_id":"` + trackTestArtist + `"}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[]}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[],"observed":null}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[],"observed":{}}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":null,"observed":{"credits":[]}}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[null],"observed":{"credits":[]}}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[],"observed":{"credits":[null]}}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[{"artist_id":"slug"}],"observed":{"credits":[]}}`},
		{ToolTrackCreditsSet, `{"track_id":"` + trackTestID + `","credits":[{"credited_name":"New","member":{"id":"private"}}],"observed":{"credits":[]}}`},
		{ToolTrackReorder, `{"track_ids":[]}`},
		{ToolTrackReorder, `{"track_ids":["` + trackTestID + `","` + trackTestID + `"]}`},
		{ToolTrackReorder, `{"track_ids":["slug"]}`},
	}
	for _, test := range tests {
		t.Run(test.tool+test.input, func(t *testing.T) {
			application := &recordingTrackApplication{}
			result, err := mustTrackTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.input))
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) || application.calls != 0 || result.Content != nil {
				t.Fatalf("invalid input reached owner: calls=%d result=%+v err=%v", application.calls, result, err)
			}
		})
	}
}

func TestTrackManagementPreservesOwningAuthorityErrors(t *testing.T) {
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeFailedPrecondition, connect.CodeInvalidArgument} {
		application := &recordingTrackApplication{err: connect.NewError(code, errors.New("owning Track outcome"))}
		result, err := mustTrackTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolTrackReorder, toolArguments(t, `{"track_ids":["`+trackTestID+`"]}`))
		var execution *mcpserver.ToolExecutionError
		if !errors.As(err, &execution) || execution.Message != "owning Track outcome" || application.calls != 1 || result.StructuredContent != nil {
			t.Fatalf("owning outcome lost: result=%+v err=%v", result, err)
		}
	}
}

func TestTrackManagementToolRegistryAndSchemas(t *testing.T) {
	var nilApplication *recordingTrackApplication
	if _, err := NewTrackManagementTools(nilApplication); err == nil {
		t.Fatal("typed nil accepted")
	}
	tools := mustTrackTools(t, &recordingTrackApplication{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ToolTrackList, ToolTrackCreate, ToolTrackSettingsUpdate, ToolTrackDelete, ToolTrackCreditsSet, ToolTrackReorder}
	if !reflect.DeepEqual(tools.ToolNames(), want) || len(listed) != len(want) {
		t.Fatal(tools.ToolNames())
	}
	for index, tool := range listed {
		if tool.Name != want[index] {
			t.Fatalf("tool order=%v", listed)
		}
		assertMCPToolOAuthSecurity(t, tool)
		assertMCPToolAnnotations(t, tool, toolAnnotations(index == 0, index > 1, false))
		for _, encoded := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			assertSchemaRootRefsResolve(t, schema, schema)
		}
	}
	if strings.Contains(string(listed[1].InputSchema), "track_number") || strings.Contains(string(listed[4].InputSchema), "expected_revision") {
		t.Fatal("creation number or invented CAS advertised")
	}
	listed[0].InputSchema[0] = '['
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || again[0].InputSchema[0] != '{' {
		t.Fatal("mutable registry schema")
	}
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "unknown", nil); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatal(err)
	}
}
