package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/work"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolDocumentFeaturedImageSet    = "document_featured_image_set"
	ToolDocumentFeaturedImageDelete = "document_featured_image_delete"
	ToolPostParticipantsList        = "post_participants_list"
	ToolPostAuthorAdd               = "post_author_add"
	ToolPostAuthorRemove            = "post_author_remove"
	ToolPostCollaboratorAdd         = "post_collaborator_add"
	ToolPostCollaboratorRemove      = "post_collaborator_remove"
	ToolWorkCreditsGet              = "work_credits_get"
	ToolWorkCreditGroupCreate       = "work_credit_group_create"
	ToolWorkCreditGroupUpdate       = "work_credit_group_update"
	ToolWorkCreditGroupDelete       = "work_credit_group_delete"
	ToolWorkCreditAdd               = "work_credit_add"
	ToolWorkCreditUpdate            = "work_credit_update"
	ToolWorkCreditDelete            = "work_credit_delete"
	ToolDocumentVersionsList        = "document_versions_list"
	ToolDocumentVersionRestore      = "document_version_restore"
	ToolDocumentSlugCheck           = "document_slug_check"
)

var contentRelatedTools = []mcpserver.Tool{
	oauthTool(ToolDocumentFeaturedImageSet, "Set featured image", "Set an existing File as the featured image of a Post, Work, or Page.", featuredImageSetInputJSONSchema, contentActionOutputJSONSchema, false, true),
	oauthTool(ToolDocumentFeaturedImageDelete, "Delete featured image", "Remove the featured image from a Post, Work, or Page.", documentTypeAndIDInputJSONSchema, contentActionOutputJSONSchema, false, true),
	oauthTool(ToolPostParticipantsList, "List Post participants", "List the authors and collaborators assigned to a Post, including effective authority.", contentIDInputJSONSchema, postParticipantsOutputJSONSchema, true, false),
	oauthTool(ToolPostAuthorAdd, "Add Post author", "Add a Member as a Post author using the existing Post authority rules.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, false),
	oauthTool(ToolPostAuthorRemove, "Remove Post author", "Remove a Member from the Post author role.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, true),
	oauthTool(ToolPostCollaboratorAdd, "Add Post collaborator", "Add a Member as a Post collaborator.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, false),
	oauthTool(ToolPostCollaboratorRemove, "Remove Post collaborator", "Remove a Member from the Post collaborator role.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, true),
	oauthTool(ToolWorkCreditsGet, "Get Work credits", "Read all credit groups and credits attached to a Work.", contentIDInputJSONSchema, workCreditsOutputJSONSchema, true, false),
	oauthTool(ToolWorkCreditGroupCreate, "Create Work credit group", "Create a named credit group on a Work.", workCreditGroupCreateInputJSONSchema, contentActionOutputJSONSchema, false, false),
	oauthTool(ToolWorkCreditGroupUpdate, "Update Work credit group", "Rename a Work credit group and return its current fields.", workCreditGroupUpdateInputJSONSchema, workCreditGroupUpdateOutputJSONSchema, false, true),
	oauthTool(ToolWorkCreditGroupDelete, "Delete Work credit group", "Delete a Work credit group using the existing Work credit rules.", workCreditGroupDeleteInputJSONSchema, contentActionOutputJSONSchema, false, true),
	oauthTool(ToolWorkCreditAdd, "Add Work credit", "Add an artist, Member, or literal-name credit to a Work, optionally inside a credit group. Supply exactly one of artist_id, member_id, or name.", workCreditAddInputJSONSchema, contentActionOutputJSONSchema, false, false),
	oauthTool(ToolWorkCreditUpdate, "Update Work credit", "Move a Work credit between groups or change its role and return its current fields. Supply group_id, credit_role, or both.", workCreditUpdateInputJSONSchema, workCreditUpdateOutputJSONSchema, false, true),
	oauthTool(ToolWorkCreditDelete, "Delete Work credit", "Delete one Work credit.", workCreditDeleteInputJSONSchema, contentActionOutputJSONSchema, false, true),
	oauthTool(ToolDocumentVersionsList, "List document versions", "List version checkpoints for a Post, Work, or Page.", documentVersionsListInputJSONSchema, documentVersionsOutputJSONSchema, true, false),
	oauthTool(ToolDocumentVersionRestore, "Restore document version", "Restore one version checkpoint into the current Post, Work, or Page.", documentVersionRestoreInputJSONSchema, contentActionOutputJSONSchema, false, true),
	oauthTool(ToolDocumentSlugCheck, "Check document slug", "Check whether a slug is available for a Post, Work, or Page, optionally excluding the current document.", documentSlugCheckInputJSONSchema, documentSlugCheckOutputJSONSchema, true, false),
}

type PostRelatedApplication interface {
	SetPostFeaturedImage(context.Context, postdomain.SetPostFeaturedImageCommand) (postdomain.FeaturedImage, error)
	DeletePostFeaturedImage(context.Context, postdomain.DeletePostFeaturedImageCommand) (*string, error)
	ListPostParticipants(context.Context, postdomain.PostParticipantsQuery) ([]postdomain.Participant, error)
	AddPostAuthor(context.Context, postdomain.PostParticipantCommand) (postdomain.Participant, error)
	RemovePostAuthor(context.Context, postdomain.PostParticipantCommand) error
	AddPostCollaborator(context.Context, postdomain.PostParticipantCommand) (postdomain.Participant, error)
	RemovePostCollaborator(context.Context, postdomain.PostParticipantCommand) error
	ListPostVersions(context.Context, postdomain.ListPostVersionsQuery) (postdomain.VersionPage, error)
	RestorePostVersion(context.Context, postdomain.RestorePostVersionCommand) (postdomain.View, error)
	CheckSlugAvailable(context.Context, postdomain.CheckSlugAvailableQuery) (bool, error)
}

type WorkRelatedApplication interface {
	SetWorkFeaturedImage(context.Context, work.SetWorkFeaturedImageCommand) (work.FeaturedImageChange, error)
	DeleteWorkFeaturedImage(context.Context, work.DeleteWorkFeaturedImageCommand) (*string, error)
	GetWorkCredits(context.Context, work.GetWorkCreditsQuery) (work.Credits, error)
	CreateWorkCreditGroup(context.Context, work.CreateWorkCreditGroupCommand) (work.CreditGroup, error)
	UpdateWorkCreditGroup(context.Context, work.UpdateWorkCreditGroupCommand) (work.CreditGroup, error)
	DeleteWorkCreditGroup(context.Context, work.DeleteWorkCreditGroupCommand) error
	AddWorkCredit(context.Context, work.AddWorkCreditCommand) (work.CreditView, error)
	UpdateWorkCredit(context.Context, work.UpdateWorkCreditCommand) (work.CreditView, error)
	DeleteWorkCredit(context.Context, work.DeleteWorkCreditCommand) error
	ListWorkVersions(context.Context, work.ListWorkVersionsQuery) (work.VersionPage, error)
	RestoreWorkVersion(context.Context, work.RestoreWorkVersionCommand) (work.View, error)
	CheckWorkSlugAvailable(context.Context, work.CheckWorkSlugAvailableQuery) (bool, error)
}

type PageRelatedApplication interface {
	SetPageFeaturedImage(context.Context, pagedomain.SetPageFeaturedImageCommand) (pagedomain.FeaturedImageResult, error)
	DeletePageFeaturedImage(context.Context, pagedomain.DeletePageFeaturedImageCommand) (*string, error)
	ListPageVersions(context.Context, pagedomain.ListPageVersionsQuery) (pagedomain.VersionList, error)
	RestorePageVersion(context.Context, pagedomain.RestorePageVersionCommand) (pagedomain.Detail, error)
	CheckPageSlugAvailable(context.Context, pagedomain.CheckPageSlugAvailableQuery) (bool, error)
}

type ContentRelatedTools struct {
	posts PostRelatedApplication
	works WorkRelatedApplication
	pages PageRelatedApplication
}

func NewContentRelatedTools(posts PostRelatedApplication, works WorkRelatedApplication, pages PageRelatedApplication) (*ContentRelatedTools, error) {
	if interfaceValueIsNil(posts) || interfaceValueIsNil(works) || interfaceValueIsNil(pages) {
		return nil, errors.New("MCP Post, Work, and Page related applications are required")
	}
	return &ContentRelatedTools{posts: posts, works: works, pages: pages}, nil
}

func (*ContentRelatedTools) ToolNames() []string { return toolDefinitionNames(contentRelatedTools) }
func (*ContentRelatedTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(contentRelatedTools), nil
}

func (tools *ContentRelatedTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolDocumentFeaturedImageSet, ToolDocumentFeaturedImageDelete:
		return tools.featuredImage(ctx, name, arguments)
	case ToolPostParticipantsList:
		return tools.listPostParticipants(ctx, arguments)
	case ToolPostAuthorAdd, ToolPostAuthorRemove, ToolPostCollaboratorAdd, ToolPostCollaboratorRemove:
		return tools.mutatePostParticipant(ctx, name, arguments)
	case ToolWorkCreditsGet:
		return tools.getWorkCredits(ctx, arguments)
	case ToolWorkCreditGroupCreate, ToolWorkCreditGroupUpdate, ToolWorkCreditGroupDelete:
		return tools.mutateWorkCreditGroup(ctx, name, arguments)
	case ToolWorkCreditAdd, ToolWorkCreditUpdate, ToolWorkCreditDelete:
		return tools.mutateWorkCredit(ctx, name, arguments)
	case ToolDocumentVersionsList:
		return tools.listDocumentVersions(ctx, arguments)
	case ToolDocumentVersionRestore:
		return tools.restoreDocumentVersion(ctx, arguments)
	case ToolDocumentSlugCheck:
		return tools.checkDocumentSlug(ctx, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

type featuredImageArguments struct {
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	FileID       string `json:"file_id,omitempty"`
}

func (tools *ContentRelatedTools) featuredImage(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if name == ToolDocumentFeaturedImageDelete {
		if err := rejectUnadvertisedArguments(arguments, "document_type", "document_id"); err != nil {
			return executionError(err)
		}
	}
	var input featuredImageArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if name == ToolDocumentFeaturedImageSet {
		var runID *string
		switch input.DocumentType {
		case "post":
			featured, err := tools.posts.SetPostFeaturedImage(ctx, postdomain.SetPostFeaturedImageCommand{PostID: input.DocumentID, FileID: input.FileID})
			if err != nil {
				return expectedToolError(err)
			}
			runID = featured.OgRunID
		case "work":
			response, err := tools.works.SetWorkFeaturedImage(ctx, work.SetWorkFeaturedImageCommand{WorkID: input.DocumentID, FileID: input.FileID})
			if err != nil {
				return expectedToolError(err)
			}
			runID = response.OgGenerationRunID
		case "page":
			response, err := tools.pages.SetPageFeaturedImage(ctx, pagedomain.SetPageFeaturedImageCommand{PageID: input.DocumentID, FileID: input.FileID})
			if err != nil {
				return expectedToolError(err)
			}
			runID = response.OgGenerationRunID
		default:
			return executionError(fmt.Errorf("document_type must be post, work, or page"))
		}
		return contentResult(map[string]any{"resource_type": "featured_image", "resource_id": input.FileID, "document_type": input.DocumentType, "document_id": input.DocumentID, "file_id": input.FileID, "changed": true, "og_generation_run_id": optionalStringValue(runID)})
	}
	var response *connect.Response[managev1.OgAssetDeleteResponse]
	var err error
	switch input.DocumentType {
	case "post":
		runID, err := tools.posts.DeletePostFeaturedImage(ctx, postdomain.DeletePostFeaturedImageCommand{PostID: input.DocumentID})
		if err != nil {
			return expectedToolError(err)
		}
		return featuredImageDeleted(input, postdomain.FeaturedImageDeletedMessage(runID))
	case "work":
		runID, err := tools.works.DeleteWorkFeaturedImage(ctx, work.DeleteWorkFeaturedImageCommand{WorkID: input.DocumentID})
		if err != nil {
			return expectedToolError(err)
		}
		return featuredImageDeleted(input, &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: runID})
	case "page":
		var runID *string
		if runID, err = tools.pages.DeletePageFeaturedImage(ctx, pagedomain.DeletePageFeaturedImageCommand{PageID: input.DocumentID}); err == nil {
			var deleted *managev1.OgAssetDeleteResponse
			deleted, err = pagedomain.FeaturedImageDeleteMessage(runID)
			response = connect.NewResponse(deleted)
		}
	default:
		return executionError(fmt.Errorf("document_type must be post, work, or page"))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return featuredImageDeleted(input, response.Msg)
}

func featuredImageDeleted(input featuredImageArguments, deleted *managev1.OgAssetDeleteResponse) (mcpserver.ToolResult, error) {
	return contentResult(map[string]any{"resource_type": "featured_image", "resource_id": input.DocumentID, "document_type": input.DocumentType, "document_id": input.DocumentID, "changed": deleted.Success, "deleted": deleted.Success, "og_generation_run_id": optionalStringValue(deleted.OgGenerationRunId)})
}

func (tools *ContentRelatedTools) listPostParticipants(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	listed, err := tools.posts.ListPostParticipants(ctx, postdomain.PostParticipantsQuery{PostID: input.DocumentID})
	if err != nil {
		return expectedToolError(err)
	}
	response := postdomain.ParticipantsMessage(listed)
	participants := make([]map[string]any, 0, len(response.Participants))
	for _, participant := range response.Participants {
		if participant == nil || participant.Member == nil {
			continue
		}
		participants = append(participants, map[string]any{
			"member_id": participant.Member.Id, "nickname": participant.Member.Nickname,
			"role":                    strings.ToLower(strings.TrimPrefix(participant.Role.String(), "POST_PARTICIPANT_ROLE_")),
			"has_effective_authority": participant.HasEffectiveAuthority, "deleted": participant.Member.Deleted,
			"created_at": timestampString(participant.CreatedAt),
		})
	}
	return contentResult(map[string]any{"document_id": input.DocumentID, "participants": participants})
}

type postParticipantArguments struct {
	DocumentID string `json:"document_id"`
	MemberID   string `json:"member_id"`
}

func (tools *ContentRelatedTools) mutatePostParticipant(ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input postParticipantArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	role := "author"
	if name == ToolPostCollaboratorAdd || name == ToolPostCollaboratorRemove {
		role = "collaborator"
	}
	command := postdomain.PostParticipantCommand{PostID: input.DocumentID, MemberID: input.MemberID}
	var err error
	switch name {
	case ToolPostAuthorAdd:
		_, err = tools.posts.AddPostAuthor(ctx, command)
	case ToolPostAuthorRemove:
		err = tools.posts.RemovePostAuthor(ctx, command)
	case ToolPostCollaboratorAdd:
		_, err = tools.posts.AddPostCollaborator(ctx, command)
	case ToolPostCollaboratorRemove:
		err = tools.posts.RemovePostCollaborator(ctx, command)
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"resource_type": "post_participant", "resource_id": input.MemberID, "document_type": "post", "document_id": input.DocumentID, "member_id": input.MemberID, "role": role, "changed": true, "deleted": name == ToolPostAuthorRemove || name == ToolPostCollaboratorRemove})
}

func (tools *ContentRelatedTools) getWorkCredits(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	workCredits, err := tools.works.GetWorkCredits(ctx, work.GetWorkCreditsQuery{WorkID: input.DocumentID})
	if err != nil {
		return expectedToolError(err)
	}
	response := work.CreditsMessage(workCredits)
	groups := make([]map[string]any, 0, len(response.Groups))
	for _, group := range response.Groups {
		if group != nil {
			groups = append(groups, map[string]any{"id": group.Id, "name": group.Name})
		}
	}
	credits := make([]map[string]any, 0, len(response.Credits))
	for _, credit := range response.Credits {
		if credit != nil {
			credits = append(credits, workCreditOutput(credit))
		}
	}
	return contentResult(map[string]any{"document_id": input.DocumentID, "groups": groups, "credits": credits})
}

type workCreditGroupArguments struct {
	DocumentID string `json:"document_id,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// workCreditGroupAdvertisedArguments mirrors each work credit group tool
// schema; the tools share workCreditGroupArguments.
var workCreditGroupAdvertisedArguments = map[string][]string{
	ToolWorkCreditGroupCreate: {"document_id", "name"},
	ToolWorkCreditGroupUpdate: {"group_id", "name"},
	ToolWorkCreditGroupDelete: {"group_id"},
}

func (tools *ContentRelatedTools) mutateWorkCreditGroup(ctx context.Context, toolName string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if err := rejectUnadvertisedArguments(arguments, workCreditGroupAdvertisedArguments[toolName]...); err != nil {
		return executionError(err)
	}
	var input workCreditGroupArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	output := map[string]any{"resource_type": "work_credit_group"}
	switch toolName {
	case ToolWorkCreditGroupCreate:
		group, err := tools.works.CreateWorkCreditGroup(ctx, work.CreateWorkCreditGroupCommand{WorkID: input.DocumentID, Name: input.Name})
		if err != nil {
			return expectedToolError(err)
		}
		response := work.CreditGroupMessage(group)
		output["resource_id"], output["document_type"], output["document_id"], output["name"] = response.Id, "work", input.DocumentID, response.Name
		output["changed"] = true
	case ToolWorkCreditGroupUpdate:
		group, err := tools.works.UpdateWorkCreditGroup(ctx, work.UpdateWorkCreditGroupCommand{GroupID: input.GroupID, Name: &input.Name})
		if err != nil {
			return expectedToolError(err)
		}
		response := work.CreditGroupMessage(group)
		output["resource_id"], output["name"] = response.Id, response.Name
	case ToolWorkCreditGroupDelete:
		if err := tools.works.DeleteWorkCreditGroup(ctx, work.DeleteWorkCreditGroupCommand{GroupID: input.GroupID}); err != nil {
			return expectedToolError(err)
		}
		output["resource_id"], output["changed"], output["deleted"] = input.GroupID, true, true
	}
	return contentResult(output)
}

type workCreditArguments struct {
	DocumentID string  `json:"document_id,omitempty"`
	CreditID   string  `json:"credit_id,omitempty"`
	GroupID    *string `json:"group_id,omitempty"`
	ArtistID   *string `json:"artist_id,omitempty"`
	MemberID   *string `json:"member_id,omitempty"`
	Name       *string `json:"name,omitempty"`
	CreditRole *string `json:"credit_role,omitempty"`
}

// workCreditAdvertisedArguments mirrors each work credit tool schema; the
// tools share workCreditArguments.
var workCreditAdvertisedArguments = map[string][]string{
	ToolWorkCreditAdd:    {"document_id", "group_id", "artist_id", "member_id", "name", "credit_role"},
	ToolWorkCreditUpdate: {"credit_id", "group_id", "credit_role"},
	ToolWorkCreditDelete: {"credit_id"},
}

func (tools *ContentRelatedTools) mutateWorkCredit(ctx context.Context, toolName string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if err := rejectUnadvertisedArguments(arguments, workCreditAdvertisedArguments[toolName]...); err != nil {
		return executionError(err)
	}
	var input workCreditArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	output := map[string]any{"resource_type": "work_credit"}
	switch toolName {
	case ToolWorkCreditAdd:
		subjects := 0
		for _, present := range []bool{input.ArtistID != nil, input.MemberID != nil, input.Name != nil} {
			if present {
				subjects++
			}
		}
		if subjects != 1 {
			return executionError(errors.New("exactly one of artist_id, member_id, or name is required"))
		}
		credit, err := tools.works.AddWorkCredit(ctx, work.AddWorkCreditCommand{WorkID: input.DocumentID, GroupID: input.GroupID, ArtistID: input.ArtistID, MemberID: input.MemberID, Name: input.Name, CreditRole: input.CreditRole})
		if err != nil {
			return expectedToolError(err)
		}
		response := work.CreditMessage(credit)
		output["resource_id"], output["document_type"], output["document_id"] = response.Id, "work", input.DocumentID
		output["changed"] = true
		copyWorkCreditMutationFields(output, response)
	case ToolWorkCreditUpdate:
		if input.GroupID == nil && input.CreditRole == nil {
			return executionError(errors.New("at least one of group_id or credit_role is required"))
		}
		credit, err := tools.works.UpdateWorkCredit(ctx, work.UpdateWorkCreditCommand{CreditID: input.CreditID, GroupID: input.GroupID, CreditRole: input.CreditRole})
		if err != nil {
			return expectedToolError(err)
		}
		response := work.CreditMessage(credit)
		output["resource_id"] = response.Id
		copyWorkCreditMutationFields(output, response)
	case ToolWorkCreditDelete:
		if err := tools.works.DeleteWorkCredit(ctx, work.DeleteWorkCreditCommand{CreditID: input.CreditID}); err != nil {
			return expectedToolError(err)
		}
		output["resource_id"], output["changed"], output["deleted"] = input.CreditID, true, true
	}
	return contentResult(output)
}

func copyWorkCreditMutationFields(output map[string]any, credit *managev1.WorkCredit) {
	if credit.GroupId != nil {
		output["group_id"] = *credit.GroupId
	}
	if credit.Name != nil {
		output["name"] = *credit.Name
	}
	if credit.CreditRole != nil {
		output["credit_role"] = *credit.CreditRole
	}
	if credit.Member != nil {
		output["member_id"] = credit.Member.Id
	}
	if credit.Artist != nil {
		output["artist_id"] = credit.Artist.Id
	}
}

func workCreditOutput(credit *managev1.WorkCredit) map[string]any {
	output := map[string]any{"id": credit.Id}
	if credit.GroupId != nil {
		output["group_id"] = *credit.GroupId
	}
	if credit.Name != nil {
		output["name"] = *credit.Name
	}
	if credit.CreditRole != nil {
		output["credit_role"] = *credit.CreditRole
	}
	if credit.Artist != nil {
		output["artist_id"], output["artist_name"] = credit.Artist.Id, credit.Artist.Name
	}
	if credit.Member != nil {
		output["member_id"], output["member_nickname"] = credit.Member.Id, credit.Member.Nickname
	}
	return output
}

type documentVersionsArguments struct {
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	Limit        int32  `json:"limit,omitempty"`
	Offset       int32  `json:"offset,omitempty"`
}

func (tools *ContentRelatedTools) listDocumentVersions(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(arguments, "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input documentVersionsArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if _, supplied := arguments["limit"]; !supplied {
		input.Limit = 20
	}
	if input.Limit < 1 || input.Limit > 100 {
		return executionError(errors.New("limit must be between 1 and 100, or omitted for the default"))
	}
	if input.Offset < 0 {
		return executionError(errors.New("offset must be nonnegative"))
	}
	pagination := &commonv1.PaginationRequest{Limit: input.Limit, Offset: input.Offset}
	versions := make([]map[string]any, 0)
	var page *commonv1.PaginationResponse
	switch input.DocumentType {
	case "post":
		listed, err := tools.posts.ListPostVersions(ctx, postdomain.ListPostVersionsQuery{PostID: input.DocumentID, Page: postdomain.Page{Limit: input.Limit, Offset: input.Offset}})
		if err != nil {
			return expectedToolError(err)
		}
		response := postdomain.VersionsMessage(listed)
		page = response.Pagination
		for _, version := range response.Versions {
			if version != nil {
				versions = append(versions, versionOutput(version.Id, version.Version, version.Title, version.Summary, version.CanonicalHash, version.SourceLocale, version.CreatedAt, version.Contributors))
			}
		}
	case "work":
		workVersions, err := tools.works.ListWorkVersions(ctx, work.ListWorkVersionsQuery{WorkID: input.DocumentID, Page: work.Page{Limit: input.Limit, Offset: input.Offset}})
		if err != nil {
			return expectedToolError(err)
		}
		response := work.VersionsMessage(workVersions)
		page = response.Pagination
		for _, version := range response.Versions {
			if version != nil {
				versions = append(versions, versionOutput(version.Id, version.Version, version.Title, version.Summary, version.CanonicalHash, version.SourceLocale, version.CreatedAt, version.Contributors))
			}
		}
	case "page":
		list, err := tools.pages.ListPageVersions(ctx, pagedomain.ListPageVersionsQuery{
			PageID: input.DocumentID, Page: pagedomain.Paging{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()},
		})
		response, err := renderPage(list, err, pagedomain.VersionListMessage)
		if err != nil {
			return expectedToolError(err)
		}
		page = response.Pagination
		for _, version := range response.Versions {
			if version != nil {
				versions = append(versions, versionOutput(version.Id, version.Version, optionalString(version.Title), version.Summary, version.CanonicalHash, version.SourceLocale, version.CreatedAt, version.Contributors))
			}
		}
	default:
		return executionError(fmt.Errorf("document_type must be post, work, or page"))
	}
	output := map[string]any{"document_type": input.DocumentType, "document_id": input.DocumentID, "versions": versions, "total": int32(0), "has_more": false}
	if page != nil {
		output["total"], output["has_more"] = page.Total, page.HasMore
		if page.HasMore {
			output["next_offset"] = int64(page.Offset) + int64(page.Limit)
		}
	}
	return contentResult(output)
}

func versionOutput(id string, version int32, title string, summary *string, hash, sourceLocale string, createdAt *timestamppb.Timestamp, contributors []*managev1.VersionContributor) map[string]any {
	items := make([]map[string]any, 0, len(contributors))
	for _, contributor := range contributors {
		if contributor != nil {
			items = append(items, map[string]any{"member_id": contributor.MemberId, "nickname": contributor.Nickname})
		}
	}
	output := map[string]any{"version_id": id, "version": version, "title": title, "canonical_hash": hash, "source_locale": sourceLocale, "created_at": timestampString(createdAt), "contributors": items}
	if summary != nil {
		output["summary"] = *summary
	}
	return output
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type documentVersionRestoreArguments struct {
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	VersionID    string `json:"version_id"`
}

func (tools *ContentRelatedTools) restoreDocumentVersion(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input documentVersionRestoreArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	output := map[string]any{"resource_type": "document", "resource_id": input.DocumentID, "document_type": input.DocumentType, "document_id": input.DocumentID, "changed": true}
	switch input.DocumentType {
	case "post":
		restored, err := tools.posts.RestorePostVersion(ctx, postdomain.RestorePostVersionCommand{PostID: input.DocumentID, VersionID: input.VersionID})
		if err != nil {
			return expectedToolError(err)
		}
		response := postdomain.PostMessage(restored)
		output["title"], output["document_revision"], output["status"] = response.Title, response.Revision, contentStatus(response.Status.String(), "POST_STATUS_")
	case "work":
		restored, err := tools.works.RestoreWorkVersion(ctx, work.RestoreWorkVersionCommand{WorkID: input.DocumentID, VersionID: input.VersionID})
		if err != nil {
			return expectedToolError(err)
		}
		output["title"], output["document_revision"], output["status"] = restored.Title, restored.Revision, contentStatus(work.WorkStatusName(restored.Status), "WORK_STATUS_")
	case "page":
		detail, err := tools.pages.RestorePageVersion(ctx, pagedomain.RestorePageVersionCommand{PageID: input.DocumentID, VersionID: input.VersionID})
		response, err := renderPage(detail, err, pagedomain.PageMessage)
		if err != nil {
			return expectedToolError(err)
		}
		output["title"], output["document_revision"], output["status"] = response.Title, response.Revision, contentStatus(response.Status.String(), "PAGE_STATUS_")
	default:
		return executionError(fmt.Errorf("document_type must be post, work, or page"))
	}
	return contentResult(output)
}

type documentSlugArguments struct {
	DocumentType      string  `json:"document_type"`
	Slug              string  `json:"slug"`
	ExcludeDocumentID *string `json:"exclude_document_id,omitempty"`
}

func (tools *ContentRelatedTools) checkDocumentSlug(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input documentSlugArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	var available bool
	switch input.DocumentType {
	case "post":
		postAvailable, err := tools.posts.CheckSlugAvailable(ctx, postdomain.CheckSlugAvailableQuery{Slug: input.Slug, ExcludePostID: input.ExcludeDocumentID})
		if err != nil {
			return expectedToolError(err)
		}
		available = postAvailable
	case "work":
		workAvailable, err := tools.works.CheckWorkSlugAvailable(ctx, work.CheckWorkSlugAvailableQuery{Slug: input.Slug, ExcludeWorkID: input.ExcludeDocumentID})
		if err != nil {
			return expectedToolError(err)
		}
		available = workAvailable
	case "page":
		pageAvailable, err := tools.pages.CheckPageSlugAvailable(ctx, pagedomain.CheckPageSlugAvailableQuery{Slug: input.Slug, ExcludeID: input.ExcludeDocumentID})
		if err != nil {
			return expectedToolError(err)
		}
		available = pageAvailable
	default:
		return executionError(fmt.Errorf("document_type must be post, work, or page"))
	}
	return contentResult(map[string]any{"document_type": input.DocumentType, "slug": input.Slug, "available": available})
}

var _ ToolProvider = (*ContentRelatedTools)(nil)
