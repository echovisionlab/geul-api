package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const releaseRelationTestID = "11111111-1111-4111-8111-111111111111"
const releaseRelationItemID = "22222222-2222-4222-8222-222222222222"
const releaseRelationPeerID = "33333333-3333-4333-8333-333333333333"

type recordingReleaseRelationApplication struct {
	request  any
	ctx      context.Context
	calls    int
	getCalls int
	snapshot *managev1.GetReleaseRelationsResponse
	err      error
}

func (application *recordingReleaseRelationApplication) record(ctx context.Context, command any) error {
	application.ctx, application.request = ctx, command
	application.calls++
	return application.err
}
func (application *recordingReleaseRelationApplication) GetReleaseRelations(ctx context.Context, query releasedomain.GetReleaseRelationsQuery) (*managev1.GetReleaseRelationsResponse, error) {
	application.ctx, application.request = ctx, query
	application.calls++
	application.getCalls++
	return application.snapshot, application.err
}
func (application *recordingReleaseRelationApplication) SetReleaseArtists(ctx context.Context, command releasedomain.SetReleaseArtistsCommand) error {
	return application.record(ctx, command)
}
func (application *recordingReleaseRelationApplication) SetReleaseLabels(ctx context.Context, command releasedomain.SetReleaseLabelsCommand) error {
	return application.record(ctx, command)
}
func (application *recordingReleaseRelationApplication) SetReleaseCategories(ctx context.Context, command releasedomain.SetReleaseCategoriesCommand) error {
	return application.record(ctx, command)
}
func (application *recordingReleaseRelationApplication) SetReleaseGenres(ctx context.Context, command releasedomain.SetReleaseGenresCommand) error {
	return application.record(ctx, command)
}
func (application *recordingReleaseRelationApplication) SetReleaseStyles(ctx context.Context, command releasedomain.SetReleaseStylesCommand) error {
	return application.record(ctx, command)
}
func (application *recordingReleaseRelationApplication) SetReleaseFormats(ctx context.Context, command releasedomain.SetReleaseFormatsCommand) error {
	return application.record(ctx, command)
}
func (application *recordingReleaseRelationApplication) SetReleaseCredits(ctx context.Context, command releasedomain.SetReleaseCreditsCommand) error {
	return application.record(ctx, command)
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
	intent := &releasedomain.RelationOrderIntent{ItemID: id, PreviousItemID: proto.String(releaseRelationPeerID), NextItemID: proto.String(releaseRelationTestID)}
	order := `,"order_intent":{"item_id":"` + id + `","previous_item_id":"` + releaseRelationPeerID + `","next_item_id":"` + releaseRelationTestID + `"}`
	for _, test := range []struct {
		name, values string
		want         any
	}{
		{ToolReleaseArtistsSet, `"artists":[],"observed_artists":[{"artist_id":"` + id + `","sort_order":5}]` + order, releasedomain.SetReleaseArtistsCommand{ReleaseID: releaseRelationTestID, Artists: []*releasedomain.ReleaseArtistInput{}, Observed: &releasedomain.Snapshot[*releasedomain.ReleaseArtistInput]{Items: []*releasedomain.ReleaseArtistInput{{ArtistID: id}}}, OrderIntent: intent}},
		{ToolReleaseLabelsSet, `"labels":[],"observed_labels":[{"label_id":"` + id + `","sort_order":5,"catalog_number":"` + catalog + `"}]` + order, releasedomain.SetReleaseLabelsCommand{ReleaseID: releaseRelationTestID, Labels: []*releasedomain.ReleaseLabelInput{}, Observed: &releasedomain.Snapshot[*releasedomain.ReleaseLabelInput]{Items: []*releasedomain.ReleaseLabelInput{{LabelID: id, CatalogNumber: &catalog}}}, OrderIntent: intent}},
		{ToolReleaseCategoriesSet, `"category_ids":[],"observed_category_ids":["` + id + `"]`, releasedomain.SetReleaseCategoriesCommand{ReleaseID: releaseRelationTestID, CategoryIDs: []string{}, Observed: &releasedomain.Snapshot[string]{Items: []string{id}}}},
		{ToolReleaseGenresSet, `"genre_ids":[],"observed_genre_ids":["` + id + `"]`, releasedomain.SetReleaseGenresCommand{ReleaseID: releaseRelationTestID, GenreIDs: []string{}, Observed: &releasedomain.Snapshot[string]{Items: []string{id}}}},
		{ToolReleaseStylesSet, `"style_ids":[],"observed_style_ids":["` + id + `"]`, releasedomain.SetReleaseStylesCommand{ReleaseID: releaseRelationTestID, StyleIDs: []string{}, Observed: &releasedomain.Snapshot[string]{Items: []string{id}}}},
		{ToolReleaseFormatsSet, `"formats":[],"observed_formats":[{"format_id":"` + id + `","format_description":"` + description + `"}]`, releasedomain.SetReleaseFormatsCommand{ReleaseID: releaseRelationTestID, Formats: []*releasedomain.ReleaseFormatInput{}, Observed: &releasedomain.Snapshot[*releasedomain.ReleaseFormatInput]{Items: []*releasedomain.ReleaseFormatInput{{FormatID: id, Description: &description}}}}},
		{ToolReleaseCreditsSet, `"credits":[],"observed_credits":[{"id":"` + id + `","artist_id":"` + releaseRelationPeerID + `","member_id":null,"credited_name":"` + credited + `","credit_role":"` + role + `","sort_order":5}]` + order, releasedomain.SetReleaseCreditsCommand{ReleaseID: releaseRelationTestID, Credits: []*releasedomain.CreditInput{}, Observed: &releasedomain.Snapshot[*releasedomain.CreditInput]{Items: []*releasedomain.CreditInput{{ID: &id, ArtistID: proto.String(releaseRelationPeerID), CreditedName: &credited, CreditRole: &role, SortOrder: 5}}}, OrderIntent: intent}},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := &recordingReleaseRelationApplication{}
			tools, err := NewReleaseRelationTools(application)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`",`+test.values+`}`))
			require.NoError(t, err)
			require.Equal(t, t.Context(), application.ctx)
			require.Equal(t, test.want, application.request)
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
	application := &recordingReleaseRelationApplication{snapshot: &managev1.GetReleaseRelationsResponse{
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
	require.Equal(t, releasedomain.GetReleaseRelationsQuery{ReleaseID: releaseRelationTestID}, application.request)
	require.NotContains(t, result.Content[0]["text"], "Display")
	artists := func() []*releasedomain.ReleaseArtistInput {
		return []*releasedomain.ReleaseArtistInput{{ArtistID: releaseRelationItemID}}
	}
	labels := func() []*releasedomain.ReleaseLabelInput {
		return []*releasedomain.ReleaseLabelInput{{LabelID: releaseRelationItemID, CatalogNumber: &catalog}}
	}
	formats := func() []*releasedomain.ReleaseFormatInput {
		return []*releasedomain.ReleaseFormatInput{{FormatID: releaseRelationItemID, Description: &description}}
	}
	credits := func() []*releasedomain.CreditInput {
		return []*releasedomain.CreditInput{{ID: proto.String(releaseRelationItemID), ArtistID: proto.String(releaseRelationPeerID), CreditedName: &credited, CreditRole: &role, SortOrder: 4}}
	}
	ids := []string{releaseRelationItemID}
	for _, test := range []struct {
		name, field string
		want        any
	}{
		{ToolReleaseArtistsSet, "artists", releasedomain.SetReleaseArtistsCommand{ReleaseID: releaseRelationTestID, Artists: artists(), Observed: &releasedomain.Snapshot[*releasedomain.ReleaseArtistInput]{Items: artists()}}},
		{ToolReleaseLabelsSet, "labels", releasedomain.SetReleaseLabelsCommand{ReleaseID: releaseRelationTestID, Labels: labels(), Observed: &releasedomain.Snapshot[*releasedomain.ReleaseLabelInput]{Items: labels()}}},
		{ToolReleaseCategoriesSet, "category_ids", releasedomain.SetReleaseCategoriesCommand{ReleaseID: releaseRelationTestID, CategoryIDs: ids, Observed: &releasedomain.Snapshot[string]{Items: ids}}},
		{ToolReleaseGenresSet, "genre_ids", releasedomain.SetReleaseGenresCommand{ReleaseID: releaseRelationTestID, GenreIDs: ids, Observed: &releasedomain.Snapshot[string]{Items: ids}}},
		{ToolReleaseStylesSet, "style_ids", releasedomain.SetReleaseStylesCommand{ReleaseID: releaseRelationTestID, StyleIDs: ids, Observed: &releasedomain.Snapshot[string]{Items: ids}}},
		{ToolReleaseFormatsSet, "formats", releasedomain.SetReleaseFormatsCommand{ReleaseID: releaseRelationTestID, Formats: formats(), Observed: &releasedomain.Snapshot[*releasedomain.ReleaseFormatInput]{Items: formats()}}},
		{ToolReleaseCreditsSet, "credits", releasedomain.SetReleaseCreditsCommand{ReleaseID: releaseRelationTestID, Credits: credits(), Observed: &releasedomain.Snapshot[*releasedomain.CreditInput]{Items: credits()}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]any{"release_id": releaseRelationTestID, test.field: result.StructuredContent[test.field], "observed_" + test.field: result.StructuredContent[test.field]})
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, string(encoded)))
			require.NoError(t, err)
			require.Equal(t, test.want, application.request)
		})
	}
	application.snapshot = &managev1.GetReleaseRelationsResponse{ReleaseId: releaseRelationTestID}
	empty, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseRelationsGet, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`"}`))
	require.NoError(t, err)
	for _, field := range []string{"artists", "labels", "category_ids", "genre_ids", "style_ids", "formats", "credits"} {
		require.Equal(t, []any{}, empty.StructuredContent[field], field)
	}
}

func TestReleaseRelationNullableAttributesArePreserved(t *testing.T) {
	application := &recordingReleaseRelationApplication{}
	tools, err := NewReleaseRelationTools(application)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReleaseCreditsSet, toolArguments(t, `{"release_id":"`+releaseRelationTestID+`","credits":[{"id":null,"artist_id":null,"member_id":"`+releaseRelationPeerID+`","credited_name":null,"credit_role":""}],"observed_credits":[],"order_intent":{"item_id":"`+releaseRelationItemID+`","previous_item_id":null}}`))
	require.NoError(t, err)
	request := application.request.(releasedomain.SetReleaseCreditsCommand)
	require.NotNil(t, request.Observed)
	require.Empty(t, request.Observed.Items)
	require.Nil(t, request.Credits[0].ID)
	require.Nil(t, request.Credits[0].ArtistID)
	require.Equal(t, releaseRelationPeerID, *request.Credits[0].MemberID)
	require.NotNil(t, request.Credits[0].CreditRole)
	require.Empty(t, *request.Credits[0].CreditRole)
	require.Nil(t, request.OrderIntent.PreviousItemID)
	require.Equal(t, true, result.StructuredContent["success"])
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
