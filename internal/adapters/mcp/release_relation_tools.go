package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolReleaseRelationsGet  = "release_relations_get"
	ToolReleaseArtistsSet    = "release_artists_set"
	ToolReleaseLabelsSet     = "release_labels_set"
	ToolReleaseCategoriesSet = "release_categories_set"
	ToolReleaseGenresSet     = "release_genres_set"
	ToolReleaseStylesSet     = "release_styles_set"
	ToolReleaseFormatsSet    = "release_formats_set"
	ToolReleaseCreditsSet    = "release_credits_set"
)

const releaseRelationSetGuidance = "Supply the unchanged observed snapshot from release_relations_get and the intended desired array. An empty desired array removes observed entries while preserving concurrent additions. Native success confirms completion, not whether data changed; call release_relations_get separately after success for the next canonical snapshot. "
const releaseRelationOrderGuidance = "Use order_intent to move item_id before next_item_id, otherwise after previous_item_id, otherwise to the end. Array position and sort_order alone do not reorder existing entries."

var releaseRelationTools = []mcpserver.Tool{
	oauthTool(ToolReleaseRelationsGet, "Get Release relations", "Read the authorized canonical editable relation snapshots. Copy the corresponding array unchanged to observed_artists, observed_labels, observed_category_ids, observed_genre_ids, observed_style_ids, observed_formats, or observed_credits before editing. Stable credit IDs identify existing credits; omit id only when adding a new credit.", releaseRelationsGetInputJSONSchema, releaseRelationsOutputJSONSchema, true, false),
	oauthTool(ToolReleaseArtistsSet, "Set Release artists", releaseRelationSetGuidance+releaseRelationOrderGuidance, releaseArtistsSetInputJSONSchema, releaseRelationMutationOutputJSONSchema, false, false),
	oauthTool(ToolReleaseLabelsSet, "Set Release labels", releaseRelationSetGuidance+releaseRelationOrderGuidance, releaseLabelsSetInputJSONSchema, releaseRelationMutationOutputJSONSchema, false, false),
	oauthTool(ToolReleaseCategoriesSet, "Set Release categories", releaseRelationSetGuidance, releaseCategoriesSetInputJSONSchema, releaseRelationMutationOutputJSONSchema, false, false),
	oauthTool(ToolReleaseGenresSet, "Set Release genres", releaseRelationSetGuidance, releaseGenresSetInputJSONSchema, releaseRelationMutationOutputJSONSchema, false, false),
	oauthTool(ToolReleaseStylesSet, "Set Release styles", releaseRelationSetGuidance, releaseStylesSetInputJSONSchema, releaseRelationMutationOutputJSONSchema, false, false),
	oauthTool(ToolReleaseFormatsSet, "Set Release formats", releaseRelationSetGuidance, releaseFormatsSetInputJSONSchema, releaseRelationMutationOutputJSONSchema, false, false),
	oauthTool(ToolReleaseCreditsSet, "Set Release credits", releaseRelationSetGuidance+releaseRelationOrderGuidance+" Omit id or supply null only for a new credit; retain every existing credit's id and unchanged optional attributes in the desired array.", releaseCreditsSetInputJSONSchema, releaseRelationMutationOutputJSONSchema, false, false),
}

// ReleaseRelationApplication retains the native observed-merge and permission boundaries.
type ReleaseRelationApplication interface {
	GetReleaseRelations(context.Context, *connect.Request[managev1.GetReleaseRelationsRequest]) (*connect.Response[managev1.GetReleaseRelationsResponse], error)
	SetReleaseArtists(context.Context, *connect.Request[managev1.SetReleaseArtistsRequest]) (*connect.Response[managev1.SuccessResponse], error)
	SetReleaseLabels(context.Context, *connect.Request[managev1.SetReleaseLabelsRequest]) (*connect.Response[managev1.SuccessResponse], error)
	SetReleaseCategories(context.Context, *connect.Request[managev1.SetReleaseCategoriesRequest]) (*connect.Response[managev1.SuccessResponse], error)
	SetReleaseGenres(context.Context, *connect.Request[managev1.SetReleaseGenresRequest]) (*connect.Response[managev1.SuccessResponse], error)
	SetReleaseStyles(context.Context, *connect.Request[managev1.SetReleaseStylesRequest]) (*connect.Response[managev1.SuccessResponse], error)
	SetReleaseFormats(context.Context, *connect.Request[managev1.SetReleaseFormatsRequest]) (*connect.Response[managev1.SuccessResponse], error)
	SetReleaseCredits(context.Context, *connect.Request[managev1.SetReleaseCreditsRequest]) (*connect.Response[managev1.SuccessResponse], error)
}

type ReleaseRelationTools struct{ application ReleaseRelationApplication }

func NewReleaseRelationTools(application ReleaseRelationApplication) (*ReleaseRelationTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Release relation application is required")
	}
	return &ReleaseRelationTools{application: application}, nil
}

func (*ReleaseRelationTools) ToolNames() []string { return toolDefinitionNames(releaseRelationTools) }
func (*ReleaseRelationTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(releaseRelationTools), nil
}

func (tools *ReleaseRelationTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolReleaseRelationsGet:
		var input struct {
			ReleaseID string `json:"release_id"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateUUID("release_id", input.ReleaseID); err != nil {
			return executionError(err)
		}
		response, err := tools.application.GetReleaseRelations(ctx, connect.NewRequest(&managev1.GetReleaseRelationsRequest{ReleaseId: input.ReleaseID}))
		if err != nil {
			return expectedToolError(err)
		}
		return releaseRelationsResult(projectReleaseRelations(response.Msg))
	case ToolReleaseArtistsSet:
		var input struct {
			ReleaseID   string                         `json:"release_id"`
			Artists     []*managev1.ReleaseArtistInput `json:"artists"`
			Observed    []*managev1.ReleaseArtistInput `json:"observed_artists"`
			OrderIntent *managev1.RelationOrderIntent  `json:"order_intent,omitempty"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateReleaseRelationRows(arguments, input.ReleaseID, input.OrderIntent, "artists", "observed_artists"); err != nil {
			return executionError(err)
		}
		for _, rows := range [][]*managev1.ReleaseArtistInput{input.Artists, input.Observed} {
			for _, row := range rows {
				if err := validateUUID("artist_id", row.ArtistId); err != nil {
					return executionError(err)
				}
			}
		}
		response, err := tools.application.SetReleaseArtists(ctx, connect.NewRequest(&managev1.SetReleaseArtistsRequest{ReleaseId: input.ReleaseID, Artists: input.Artists, Observed: &managev1.ReleaseArtistsSnapshot{Artists: input.Observed}, OrderIntent: input.OrderIntent}))
		return releaseRelationMutationResult(input.ReleaseID, response, err)
	case ToolReleaseLabelsSet:
		var input struct {
			ReleaseID   string                        `json:"release_id"`
			Labels      []*managev1.ReleaseLabelInput `json:"labels"`
			Observed    []*managev1.ReleaseLabelInput `json:"observed_labels"`
			OrderIntent *managev1.RelationOrderIntent `json:"order_intent,omitempty"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateReleaseRelationRows(arguments, input.ReleaseID, input.OrderIntent, "labels", "observed_labels"); err != nil {
			return executionError(err)
		}
		for _, rows := range [][]*managev1.ReleaseLabelInput{input.Labels, input.Observed} {
			for _, row := range rows {
				if err := validateUUID("label_id", row.LabelId); err != nil {
					return executionError(err)
				}
			}
		}
		response, err := tools.application.SetReleaseLabels(ctx, connect.NewRequest(&managev1.SetReleaseLabelsRequest{ReleaseId: input.ReleaseID, Labels: input.Labels, Observed: &managev1.ReleaseLabelsSnapshot{Labels: input.Observed}, OrderIntent: input.OrderIntent}))
		return releaseRelationMutationResult(input.ReleaseID, response, err)
	case ToolReleaseCategoriesSet:
		var input struct {
			ReleaseID string   `json:"release_id"`
			IDs       []string `json:"category_ids"`
			Observed  []string `json:"observed_category_ids"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateReleaseRelationIDs(input.ReleaseID, input.IDs, input.Observed); err != nil {
			return executionError(err)
		}
		response, err := tools.application.SetReleaseCategories(ctx, connect.NewRequest(&managev1.SetReleaseCategoriesRequest{ReleaseId: input.ReleaseID, CategoryIds: input.IDs, Observed: &managev1.StringIdSnapshot{Ids: input.Observed}}))
		return releaseRelationMutationResult(input.ReleaseID, response, err)
	case ToolReleaseGenresSet:
		var input struct {
			ReleaseID string   `json:"release_id"`
			IDs       []string `json:"genre_ids"`
			Observed  []string `json:"observed_genre_ids"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateReleaseRelationIDs(input.ReleaseID, input.IDs, input.Observed); err != nil {
			return executionError(err)
		}
		response, err := tools.application.SetReleaseGenres(ctx, connect.NewRequest(&managev1.SetReleaseGenresRequest{ReleaseId: input.ReleaseID, GenreIds: input.IDs, Observed: &managev1.StringIdSnapshot{Ids: input.Observed}}))
		return releaseRelationMutationResult(input.ReleaseID, response, err)
	case ToolReleaseStylesSet:
		var input struct {
			ReleaseID string   `json:"release_id"`
			IDs       []string `json:"style_ids"`
			Observed  []string `json:"observed_style_ids"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateReleaseRelationIDs(input.ReleaseID, input.IDs, input.Observed); err != nil {
			return executionError(err)
		}
		response, err := tools.application.SetReleaseStyles(ctx, connect.NewRequest(&managev1.SetReleaseStylesRequest{ReleaseId: input.ReleaseID, StyleIds: input.IDs, Observed: &managev1.StringIdSnapshot{Ids: input.Observed}}))
		return releaseRelationMutationResult(input.ReleaseID, response, err)
	case ToolReleaseFormatsSet:
		var input struct {
			ReleaseID string                         `json:"release_id"`
			Formats   []*managev1.ReleaseFormatInput `json:"formats"`
			Observed  []*managev1.ReleaseFormatInput `json:"observed_formats"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateReleaseRelationRows(arguments, input.ReleaseID, nil, "formats", "observed_formats"); err != nil {
			return executionError(err)
		}
		for _, rows := range [][]*managev1.ReleaseFormatInput{input.Formats, input.Observed} {
			for _, row := range rows {
				if err := validateUUID("format_id", row.FormatId); err != nil {
					return executionError(err)
				}
			}
		}
		response, err := tools.application.SetReleaseFormats(ctx, connect.NewRequest(&managev1.SetReleaseFormatsRequest{ReleaseId: input.ReleaseID, Formats: input.Formats, Observed: &managev1.ReleaseFormatsSnapshot{Formats: input.Observed}}))
		return releaseRelationMutationResult(input.ReleaseID, response, err)
	case ToolReleaseCreditsSet:
		var input struct {
			ReleaseID   string                         `json:"release_id"`
			Credits     []*managev1.ReleaseCreditInput `json:"credits"`
			Observed    []*managev1.ReleaseCreditInput `json:"observed_credits"`
			OrderIntent *managev1.RelationOrderIntent  `json:"order_intent,omitempty"`
		}
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		if err := validateReleaseRelationRows(arguments, input.ReleaseID, input.OrderIntent, "credits", "observed_credits"); err != nil {
			return executionError(err)
		}
		for _, row := range input.Observed {
			if row.Id == nil {
				return executionError(errors.New("observed_credits items require their existing id"))
			}
		}
		for _, rows := range [][]*managev1.ReleaseCreditInput{input.Credits, input.Observed} {
			for _, row := range rows {
				for name, id := range map[string]*string{"id": row.Id, "artist_id": row.ArtistId, "member_id": row.MemberId} {
					if id != nil {
						if err := validateUUID(name, *id); err != nil {
							return executionError(err)
						}
					}
				}
			}
		}
		response, err := tools.application.SetReleaseCredits(ctx, connect.NewRequest(&managev1.SetReleaseCreditsRequest{ReleaseId: input.ReleaseID, Credits: input.Credits, Observed: &managev1.ReleaseCreditsSnapshot{Credits: input.Observed}, OrderIntent: input.OrderIntent}))
		return releaseRelationMutationResult(input.ReleaseID, response, err)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

func validateReleaseRelationRows(arguments mcpserver.ToolArguments, releaseID string, intent *managev1.RelationOrderIntent, names ...string) error {
	if err := validateUUID("release_id", releaseID); err != nil {
		return err
	}
	if err := rejectNullArguments(arguments, "order_intent"); err != nil {
		return err
	}
	if intent != nil {
		if err := validateUUID("order_intent.item_id", intent.ItemId); err != nil {
			return err
		}
		for name, id := range map[string]*string{"previous_item_id": intent.PreviousItemId, "next_item_id": intent.NextItemId} {
			if id != nil {
				if err := validateUUID("order_intent."+name, *id); err != nil {
					return err
				}
			}
		}
	}
	for _, name := range names {
		var rows []mcpserver.ToolArguments
		if err := json.Unmarshal(arguments[name], &rows); err != nil {
			return fmt.Errorf("%s must be an array: %w", name, err)
		}
		if rows == nil {
			return fmt.Errorf("%s must be a supplied array, including [] for an empty snapshot", name)
		}
		for index, row := range rows {
			if row == nil {
				return fmt.Errorf("%s[%d] cannot be null", name, index)
			}
			if err := rejectNullArguments(row, "sort_order"); err != nil {
				return fmt.Errorf("%s[%d]: %w", name, index, err)
			}
		}
	}
	return nil
}

func validateReleaseRelationIDs(releaseID string, desired, observed []string) error {
	if err := validateUUID("release_id", releaseID); err != nil {
		return err
	}
	if desired == nil || observed == nil {
		return errors.New("desired and observed ID arrays are required; supply [] for empty arrays")
	}
	for _, ids := range [][]string{desired, observed} {
		for _, id := range ids {
			if err := validateUUID("relation ID", id); err != nil {
				return err
			}
		}
	}
	return nil
}

type releaseRelationsOutput struct {
	ReleaseID   string                         `json:"release_id"`
	Artists     []*managev1.ReleaseArtistInput `json:"artists"`
	Labels      []*managev1.ReleaseLabelInput  `json:"labels"`
	CategoryIDs []string                       `json:"category_ids"`
	GenreIDs    []string                       `json:"genre_ids"`
	StyleIDs    []string                       `json:"style_ids"`
	Formats     []*managev1.ReleaseFormatInput `json:"formats"`
	Credits     []*managev1.ReleaseCreditInput `json:"credits"`
}

func projectReleaseRelations(source *managev1.GetReleaseRelationsResponse) releaseRelationsOutput {
	output := releaseRelationsOutput{ReleaseID: source.ReleaseId,
		Artists: make([]*managev1.ReleaseArtistInput, 0, len(source.Artists)), Labels: make([]*managev1.ReleaseLabelInput, 0, len(source.Labels)),
		CategoryIDs: make([]string, 0, len(source.Categories)), GenreIDs: make([]string, 0, len(source.Genres)), StyleIDs: make([]string, 0, len(source.Styles)),
		Formats: make([]*managev1.ReleaseFormatInput, 0, len(source.Formats)), Credits: make([]*managev1.ReleaseCreditInput, 0, len(source.Credits)),
	}
	for _, row := range source.Artists {
		output.Artists = append(output.Artists, &managev1.ReleaseArtistInput{ArtistId: row.ArtistId, SortOrder: row.SortOrder})
	}
	for _, row := range source.Labels {
		output.Labels = append(output.Labels, &managev1.ReleaseLabelInput{LabelId: row.LabelId, CatalogNumber: row.CatalogNumber, SortOrder: row.SortOrder})
	}
	for _, row := range source.Categories {
		output.CategoryIDs = append(output.CategoryIDs, row.Id)
	}
	for _, row := range source.Genres {
		output.GenreIDs = append(output.GenreIDs, row.Id)
	}
	for _, row := range source.Styles {
		output.StyleIDs = append(output.StyleIDs, row.Id)
	}
	for _, row := range source.Formats {
		output.Formats = append(output.Formats, &managev1.ReleaseFormatInput{FormatId: row.Id, FormatDescription: row.FormatDescription})
	}
	for _, row := range source.Credits {
		id := row.Id
		output.Credits = append(output.Credits, &managev1.ReleaseCreditInput{Id: &id, ArtistId: row.ArtistId, MemberId: row.MemberId, CreditedName: row.CreditedName, CreditRole: row.CreditRole, SortOrder: row.SortOrder})
	}
	return output
}

func releaseRelationMutationResult(releaseID string, response *connect.Response[managev1.SuccessResponse], err error) (mcpserver.ToolResult, error) {
	if err != nil {
		return expectedToolError(err)
	}
	return releaseRelationsResult(struct {
		ReleaseID string `json:"release_id"`
		Success   bool   `json:"success"`
		NextStep  string `json:"next_step"`
	}{releaseID, response.Msg.Success, "Call release_relations_get with this release_id for the next canonical observed snapshot."})
}

func releaseRelationsResult(value any) (mcpserver.ToolResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP Release relation result: %w", err)
	}
	return structuredResult(encoded, false)
}
