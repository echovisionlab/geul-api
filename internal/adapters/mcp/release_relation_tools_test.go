package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const releaseRelationTestID = "11111111-1111-4111-8111-111111111111"
const releaseRelationItemID = "22222222-2222-4222-8222-222222222222"
const releaseRelationPeerID = "33333333-3333-4333-8333-333333333333"

type recordingReleaseRelationApplication struct {
	managev1connect.UnimplementedReleaseServiceHandler
	request  proto.Message
	ctx      context.Context
	calls    int
	getCalls int
	snapshot *managev1.GetReleaseRelationsResponse
	success  bool
	err      error
}

func (application *recordingReleaseRelationApplication) record(ctx context.Context, request proto.Message) (*connect.Response[managev1.SuccessResponse], error) {
	application.ctx, application.request = ctx, request
	application.calls++
	return connect.NewResponse(&managev1.SuccessResponse{Success: application.success}), application.err
}
func (application *recordingReleaseRelationApplication) GetReleaseRelations(ctx context.Context, request *connect.Request[managev1.GetReleaseRelationsRequest]) (*connect.Response[managev1.GetReleaseRelationsResponse], error) {
	application.ctx, application.request = ctx, request.Msg
	application.calls++
	application.getCalls++
	return connect.NewResponse(application.snapshot), application.err
}
func (application *recordingReleaseRelationApplication) SetReleaseArtists(ctx context.Context, request *connect.Request[managev1.SetReleaseArtistsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return application.record(ctx, request.Msg)
}
func (application *recordingReleaseRelationApplication) SetReleaseLabels(ctx context.Context, request *connect.Request[managev1.SetReleaseLabelsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return application.record(ctx, request.Msg)
}
func (application *recordingReleaseRelationApplication) SetReleaseCategories(ctx context.Context, request *connect.Request[managev1.SetReleaseCategoriesRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return application.record(ctx, request.Msg)
}
func (application *recordingReleaseRelationApplication) SetReleaseGenres(ctx context.Context, request *connect.Request[managev1.SetReleaseGenresRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return application.record(ctx, request.Msg)
}
func (application *recordingReleaseRelationApplication) SetReleaseStyles(ctx context.Context, request *connect.Request[managev1.SetReleaseStylesRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return application.record(ctx, request.Msg)
}
func (application *recordingReleaseRelationApplication) SetReleaseFormats(ctx context.Context, request *connect.Request[managev1.SetReleaseFormatsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return application.record(ctx, request.Msg)
}
func (application *recordingReleaseRelationApplication) SetReleaseCredits(ctx context.Context, request *connect.Request[managev1.SetReleaseCreditsRequest]) (*connect.Response[managev1.SuccessResponse], error) {
	return application.record(ctx, request.Msg)
}

func TestReleaseRelationToolsDescriptors(t *testing.T) {
	_, err := NewReleaseRelationTools(nil)
	require.Error(t, err)
	var missing *recordingReleaseRelationApplication
	_, err = NewReleaseRelationTools(missing)
	require.Error(t, err)
	tools, err := NewReleaseRelationTools(&recordingReleaseRelationApplication{})
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 8)
	require.Equal(t, toolDefinitionNames(releaseRelationTools), tools.ToolNames())
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		require.Equal(t, tool.Name == ToolReleaseRelationsGet, tool.Annotations["readOnlyHint"])
		var input, output map[string]any
		require.NoError(t, json.Unmarshal(tool.InputSchema, &input))
		require.NoError(t, json.Unmarshal(tool.OutputSchema, &output))
		require.Equal(t, false, input["additionalProperties"])
		require.Equal(t, false, output["additionalProperties"])
		if tool.Name != ToolReleaseRelationsGet {
			require.Len(t, input["required"], 3)
			require.NotContains(t, output["properties"], "changed")
			require.Contains(t, tool.Description, "preserving concurrent additions")
			require.Contains(t, tool.Description, "call release_relations_get separately")
		}
	}
	listed[0].OutputSchema[0] = '['
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Equal(t, byte('{'), again[0].OutputSchema[0])
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, "release_relation_unknown", nil)
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
}

func TestReleaseRelationSettersForwardEmptyDesiredAndExactObserved(t *testing.T) {
	id, catalog, description, credited, role := releaseRelationItemID, "Original catalog", "Original format", "Original credit", "Producer"
	intent := &managev1.RelationOrderIntent{ItemId: id, PreviousItemId: proto.String(releaseRelationPeerID), NextItemId: proto.String(releaseRelationTestID)}
	order := `,"order_intent":{"item_id":"` + id + `","previous_item_id":"` + releaseRelationPeerID + `","next_item_id":"` + releaseRelationTestID + `"}`
	for _, test := range []struct {
		name, values string
		want         proto.Message
	}{
		{ToolReleaseArtistsSet, `"artists":[],"observed_artists":[{"artist_id":"` + id + `","sort_order":5}]` + order, &managev1.SetReleaseArtistsRequest{ReleaseId: releaseRelationTestID, Observed: &managev1.ReleaseArtistsSnapshot{Artists: []*managev1.ReleaseArtistInput{{ArtistId: id, SortOrder: 5}}}, OrderIntent: intent}},
		{ToolReleaseLabelsSet, `"labels":[],"observed_labels":[{"label_id":"` + id + `","sort_order":5,"catalog_number":"` + catalog + `"}]` + order, &managev1.SetReleaseLabelsRequest{ReleaseId: releaseRelationTestID, Observed: &managev1.ReleaseLabelsSnapshot{Labels: []*managev1.ReleaseLabelInput{{LabelId: id, SortOrder: 5, CatalogNumber: &catalog}}}, OrderIntent: intent}},
		{ToolReleaseCategoriesSet, `"category_ids":[],"observed_category_ids":["` + id + `"]`, &managev1.SetReleaseCategoriesRequest{ReleaseId: releaseRelationTestID, Observed: &managev1.StringIdSnapshot{Ids: []string{id}}}},
		{ToolReleaseGenresSet, `"genre_ids":[],"observed_genre_ids":["` + id + `"]`, &managev1.SetReleaseGenresRequest{ReleaseId: releaseRelationTestID, Observed: &managev1.StringIdSnapshot{Ids: []string{id}}}},
		{ToolReleaseStylesSet, `"style_ids":[],"observed_style_ids":["` + id + `"]`, &managev1.SetReleaseStylesRequest{ReleaseId: releaseRelationTestID, Observed: &managev1.StringIdSnapshot{Ids: []string{id}}}},
		{ToolReleaseFormatsSet, `"formats":[],"observed_formats":[{"format_id":"` + id + `","format_description":"` + description + `"}]`, &managev1.SetReleaseFormatsRequest{ReleaseId: releaseRelationTestID, Observed: &managev1.ReleaseFormatsSnapshot{Formats: []*managev1.ReleaseFormatInput{{FormatId: id, FormatDescription: &description}}}}},
		{ToolReleaseCreditsSet, `"credits":[],"observed_credits":[{"id":"` + id + `","artist_id":"` + releaseRelationPeerID + `","member_id":null,"credited_name":"` + credited + `","credit_role":"` + role + `","sort_order":5}]` + order, &managev1.SetReleaseCreditsRequest{ReleaseId: releaseRelationTestID, Observed: &managev1.ReleaseCreditsSnapshot{Credits: []*managev1.ReleaseCreditInput{{Id: &id, ArtistId: proto.String(releaseRelationPeerID), CreditedName: &credited, CreditRole: &role, SortOrder: 5}}}, OrderIntent: intent}},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := &recordingReleaseRelationApplication{success: true}
			tools, err := NewReleaseRelationTools(application)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`",`+test.values+`}`))
			require.NoError(t, err)
			require.Equal(t, t.Context(), application.ctx)
			require.True(t, proto.Equal(test.want, application.request), "request=%v, want=%v", application.request, test.want)
			require.Equal(t, 1, application.calls)
			require.Zero(t, application.getCalls, "a committed write must not be followed by an automatic fallible read")
			require.Equal(t, true, result.StructuredContent["success"])
			require.NotContains(t, result.StructuredContent, "changed")
			require.Contains(t, result.StructuredContent["next_step"], ToolReleaseRelationsGet)
			var text map[string]any
			require.NoError(t, json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text))
			require.Equal(t, result.StructuredContent, text)
		})
	}
}

func TestReleaseRelationReadSnapshotsRoundTripWithoutPresentationFields(t *testing.T) {
	catalog, description, credited, role := "ABC-001", "Blue vinyl", "Credited artist", "Producer"
	application := &recordingReleaseRelationApplication{success: true, snapshot: &managev1.GetReleaseRelationsResponse{
		ReleaseId:  releaseRelationTestID,
		Artists:    []*managev1.ReleaseArtistEditorItem{{ArtistId: releaseRelationItemID, ArtistName: "Display artist", SortOrder: 0}},
		Labels:     []*managev1.ReleaseLabelEditorItem{{LabelId: releaseRelationItemID, LabelName: "Display label", CatalogNumber: &catalog, SortOrder: 3}},
		Categories: []*managev1.ReleaseReferenceEditorItem{{Id: releaseRelationItemID, Name: "Display category"}},
		Genres:     []*managev1.ReleaseReferenceEditorItem{{Id: releaseRelationItemID}}, Styles: []*managev1.ReleaseReferenceEditorItem{{Id: releaseRelationItemID}},
		Formats: []*managev1.ReleaseFormatEditorItem{{Id: releaseRelationItemID, Name: "Display format", FormatDescription: &description}},
		Credits: []*managev1.ReleaseCreditEditorItem{{Id: releaseRelationItemID, ArtistId: proto.String(releaseRelationPeerID), ArtistName: proto.String("Display credit artist"), CreditedName: &credited, CreditRole: &role, SortOrder: 4}},
	}}
	tools, err := NewReleaseRelationTools(application)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseRelationsGet, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`"}`))
	require.NoError(t, err)
	require.True(t, proto.Equal(&managev1.GetReleaseRelationsRequest{ReleaseId: releaseRelationTestID}, application.request))
	require.NotContains(t, result.Content[0]["text"], "Display")
	for _, test := range []struct {
		name, field string
		want        proto.Message
	}{
		{ToolReleaseArtistsSet, "artists", &managev1.SetReleaseArtistsRequest{ReleaseId: releaseRelationTestID, Artists: []*managev1.ReleaseArtistInput{{ArtistId: releaseRelationItemID}}, Observed: &managev1.ReleaseArtistsSnapshot{Artists: []*managev1.ReleaseArtistInput{{ArtistId: releaseRelationItemID}}}}},
		{ToolReleaseLabelsSet, "labels", &managev1.SetReleaseLabelsRequest{ReleaseId: releaseRelationTestID, Labels: []*managev1.ReleaseLabelInput{{LabelId: releaseRelationItemID, CatalogNumber: &catalog, SortOrder: 3}}, Observed: &managev1.ReleaseLabelsSnapshot{Labels: []*managev1.ReleaseLabelInput{{LabelId: releaseRelationItemID, CatalogNumber: &catalog, SortOrder: 3}}}}},
		{ToolReleaseCategoriesSet, "category_ids", &managev1.SetReleaseCategoriesRequest{ReleaseId: releaseRelationTestID, CategoryIds: []string{releaseRelationItemID}, Observed: &managev1.StringIdSnapshot{Ids: []string{releaseRelationItemID}}}},
		{ToolReleaseGenresSet, "genre_ids", &managev1.SetReleaseGenresRequest{ReleaseId: releaseRelationTestID, GenreIds: []string{releaseRelationItemID}, Observed: &managev1.StringIdSnapshot{Ids: []string{releaseRelationItemID}}}},
		{ToolReleaseStylesSet, "style_ids", &managev1.SetReleaseStylesRequest{ReleaseId: releaseRelationTestID, StyleIds: []string{releaseRelationItemID}, Observed: &managev1.StringIdSnapshot{Ids: []string{releaseRelationItemID}}}},
		{ToolReleaseFormatsSet, "formats", &managev1.SetReleaseFormatsRequest{ReleaseId: releaseRelationTestID, Formats: []*managev1.ReleaseFormatInput{{FormatId: releaseRelationItemID, FormatDescription: &description}}, Observed: &managev1.ReleaseFormatsSnapshot{Formats: []*managev1.ReleaseFormatInput{{FormatId: releaseRelationItemID, FormatDescription: &description}}}}},
		{ToolReleaseCreditsSet, "credits", &managev1.SetReleaseCreditsRequest{ReleaseId: releaseRelationTestID, Credits: []*managev1.ReleaseCreditInput{{Id: proto.String(releaseRelationItemID), ArtistId: proto.String(releaseRelationPeerID), CreditedName: &credited, CreditRole: &role, SortOrder: 4}}, Observed: &managev1.ReleaseCreditsSnapshot{Credits: []*managev1.ReleaseCreditInput{{Id: proto.String(releaseRelationItemID), ArtistId: proto.String(releaseRelationPeerID), CreditedName: &credited, CreditRole: &role, SortOrder: 4}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]any{"release_id": releaseRelationTestID, test.field: result.StructuredContent[test.field], "observed_" + test.field: result.StructuredContent[test.field]})
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, string(encoded)))
			require.NoError(t, err)
			require.True(t, proto.Equal(test.want, application.request), "request=%v, want=%v", application.request, test.want)
		})
	}
	application.snapshot = &managev1.GetReleaseRelationsResponse{ReleaseId: releaseRelationTestID}
	empty, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseRelationsGet, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`"}`))
	require.NoError(t, err)
	for _, field := range []string{"artists", "labels", "category_ids", "genre_ids", "style_ids", "formats", "credits"} {
		require.Equal(t, []any{}, empty.StructuredContent[field], field)
	}
}

func TestReleaseRelationNullableAttributesAndNativeSuccessArePreserved(t *testing.T) {
	application := &recordingReleaseRelationApplication{success: false}
	tools, err := NewReleaseRelationTools(application)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseCreditsSet, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`","credits":[{"id":null,"artist_id":null,"member_id":"`+releaseRelationPeerID+`","credited_name":null,"credit_role":""}],"observed_credits":[],"order_intent":{"item_id":"`+releaseRelationItemID+`","previous_item_id":null}}`))
	require.NoError(t, err)
	request := application.request.(*managev1.SetReleaseCreditsRequest)
	require.NotNil(t, request.Observed)
	require.Empty(t, request.Observed.Credits)
	require.Nil(t, request.Credits[0].Id)
	require.Nil(t, request.Credits[0].ArtistId)
	require.Equal(t, releaseRelationPeerID, request.Credits[0].GetMemberId())
	require.NotNil(t, request.Credits[0].CreditRole)
	require.Empty(t, *request.Credits[0].CreditRole)
	require.Nil(t, request.OrderIntent.PreviousItemId)
	require.Equal(t, false, result.StructuredContent["success"])
	require.NotContains(t, result.StructuredContent, "changed")
}

func TestReleaseRelationInvalidSnapshotsNeverReachOwningMutation(t *testing.T) {
	for _, family := range []struct{ name, field string }{
		{ToolReleaseArtistsSet, "artists"}, {ToolReleaseLabelsSet, "labels"},
		{ToolReleaseCategoriesSet, "category_ids"}, {ToolReleaseGenresSet, "genre_ids"}, {ToolReleaseStylesSet, "style_ids"},
		{ToolReleaseFormatsSet, "formats"}, {ToolReleaseCreditsSet, "credits"},
	} {
		for _, values := range []string{
			`"` + family.field + `":[]`, `"observed_` + family.field + `":[]`,
			`"` + family.field + `":null,"observed_` + family.field + `":[]`, `"` + family.field + `":[],"observed_` + family.field + `":null`,
			`"` + family.field + `":[null],"observed_` + family.field + `":[]`, `"` + family.field + `":[],"observed_` + family.field + `":[null]`,
			`"` + family.field + `":[],"observed_` + family.field + `":[],"extra":true`,
		} {
			t.Run(family.name+values, func(t *testing.T) {
				application := &recordingReleaseRelationApplication{}
				tools, err := NewReleaseRelationTools(application)
				require.NoError(t, err)
				_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, family.name, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`",`+values+`}`))
				var execution *mcpserver.ToolExecutionError
				require.ErrorAs(t, err, &execution)
				require.Zero(t, application.calls)
			})
		}
	}
	for _, test := range []struct{ name, values string }{
		{ToolReleaseArtistsSet, `"artists":[{"artist_id":"slug"}],"observed_artists":[]`},
		{ToolReleaseArtistsSet, `"artists":[{"artist_id":"` + releaseRelationItemID + `","sort_order":null}],"observed_artists":[]`},
		{ToolReleaseArtistsSet, `"artists":[{"artist_id":"` + releaseRelationItemID + `","sort_order":2147483648}],"observed_artists":[]`},
		{ToolReleaseArtistsSet, `"artists":[],"observed_artists":[],"order_intent":null`},
		{ToolReleaseLabelsSet, `"labels":[],"observed_labels":[],"order_intent":{}`},
		{ToolReleaseCreditsSet, `"credits":[],"observed_credits":[{}]`},
		{ToolReleaseCreditsSet, `"credits":[],"observed_credits":[{"id":null}]`},
		{ToolReleaseCreditsSet, `"credits":[{"member_id":"not-a-member"}],"observed_credits":[]`},
		{ToolReleaseFormatsSet, `"formats":[],"observed_formats":[],"order_intent":{"item_id":"` + releaseRelationItemID + `"}`},
	} {
		t.Run(test.name+test.values, func(t *testing.T) {
			application := &recordingReleaseRelationApplication{}
			tools, err := NewReleaseRelationTools(application)
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`",`+test.values+`}`))
			require.Error(t, err)
			require.Zero(t, application.calls)
		})
	}
}

func TestReleaseRelationErrorsDoNotReturnSnapshotsOrSuccess(t *testing.T) {
	for _, failure := range []error{
		connect.NewError(connect.CodePermissionDenied, errors.New("owning permission denied")),
		connect.NewError(connect.CodeNotFound, errors.New("release missing")),
		connect.NewError(connect.CodeUnavailable, errors.New("private dependency failure")),
		connect.NewError(connect.CodeInternal, errors.New("private database detail")),
	} {
		for _, call := range []struct{ name, values string }{{ToolReleaseRelationsGet, ""}, {ToolReleaseArtistsSet, `,"artists":[],"observed_artists":[]`}} {
			t.Run(call.name+failure.Error(), func(t *testing.T) {
				application := &recordingReleaseRelationApplication{err: failure}
				tools, err := NewReleaseRelationTools(application)
				require.NoError(t, err)
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, call.name, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`"`+call.values+`}`))
				require.Error(t, err)
				require.Empty(t, result.Content)
				require.Nil(t, result.StructuredContent)
				require.Equal(t, 1, application.calls)
				var execution *mcpserver.ToolExecutionError
				if connect.CodeOf(failure) == connect.CodeInternal {
					require.ErrorIs(t, err, failure)
					require.False(t, errors.As(err, &execution))
				} else {
					require.ErrorAs(t, err, &execution)
					require.NotContains(t, execution.Message, "private")
				}
			})
		}
	}
}
