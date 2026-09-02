package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
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
	relatedTool(ToolDocumentFeaturedImageSet, "Set featured image", "Set an existing Geul File as the featured image of a Post, Work, or Page.", featuredImageSetInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolDocumentFeaturedImageDelete, "Delete featured image", "Remove the featured image from a Post, Work, or Page.", documentTypeAndIDInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolPostParticipantsList, "List Post participants", "List the authors and collaborators assigned to a Post, including effective authority.", contentIDInputJSONSchema, postParticipantsOutputJSONSchema, true, false),
	relatedTool(ToolPostAuthorAdd, "Add Post author", "Add a Geul Member as a Post author using the existing Post authority rules.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, false),
	relatedTool(ToolPostAuthorRemove, "Remove Post author", "Remove a Geul Member from the Post author role.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolPostCollaboratorAdd, "Add Post collaborator", "Add a Geul Member as a Post collaborator.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, false),
	relatedTool(ToolPostCollaboratorRemove, "Remove Post collaborator", "Remove a Geul Member from the Post collaborator role.", postParticipantInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolWorkCreditsGet, "Get Work credits", "Read all credit groups and credits attached to a Work.", contentIDInputJSONSchema, workCreditsOutputJSONSchema, true, false),
	relatedTool(ToolWorkCreditGroupCreate, "Create Work credit group", "Create a named credit group on a Work.", workCreditGroupCreateInputJSONSchema, contentActionOutputJSONSchema, false, false),
	relatedTool(ToolWorkCreditGroupUpdate, "Update Work credit group", "Rename a Work credit group.", workCreditGroupUpdateInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolWorkCreditGroupDelete, "Delete Work credit group", "Delete a Work credit group using the existing Work credit rules.", workCreditGroupDeleteInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolWorkCreditAdd, "Add Work credit", "Add an artist, Member, or literal-name credit to a Work, optionally inside a credit group.", workCreditAddInputJSONSchema, contentActionOutputJSONSchema, false, false),
	relatedTool(ToolWorkCreditUpdate, "Update Work credit", "Move a Work credit between groups or change its role.", workCreditUpdateInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolWorkCreditDelete, "Delete Work credit", "Delete one Work credit.", workCreditDeleteInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolDocumentVersionsList, "List document versions", "List version checkpoints for a Post, Work, or Page.", documentVersionsListInputJSONSchema, documentVersionsOutputJSONSchema, true, false),
	relatedTool(ToolDocumentVersionRestore, "Restore document version", "Restore one version checkpoint into the current Post, Work, or Page.", documentVersionRestoreInputJSONSchema, contentActionOutputJSONSchema, false, true),
	relatedTool(ToolDocumentSlugCheck, "Check document slug", "Check whether a slug is available for a Post, Work, or Page, optionally excluding the current document.", documentSlugCheckInputJSONSchema, documentSlugCheckOutputJSONSchema, true, false),
}

func relatedTool(name, title, description, inputSchema, outputSchema string, readOnly, destructive bool) mcpserver.Tool {
	return mcpserver.Tool{
		Name: name, Title: title, Description: description,
		InputSchema: json.RawMessage(inputSchema), OutputSchema: json.RawMessage(outputSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(readOnly, destructive, false), Meta: oauthSecurityMeta(),
	}
}

type PostRelatedApplication interface {
	SetPostFeaturedImage(context.Context, *connect.Request[managev1.SetPostFeaturedImageRequest]) (*connect.Response[managev1.SetPostFeaturedImageResponse], error)
	DeletePostFeaturedImage(context.Context, *connect.Request[managev1.DeletePostFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error)
	ListPostParticipants(context.Context, *connect.Request[managev1.ListPostParticipantsRequest]) (*connect.Response[managev1.ListPostParticipantsResponse], error)
	AddPostAuthor(context.Context, *connect.Request[managev1.AddPostAuthorRequest]) (*connect.Response[managev1.PostParticipant], error)
	RemovePostAuthor(context.Context, *connect.Request[managev1.RemovePostAuthorRequest]) (*connect.Response[managev1.DeleteResponse], error)
	AddPostCollaborator(context.Context, *connect.Request[managev1.AddPostCollaboratorRequest]) (*connect.Response[managev1.PostParticipant], error)
	RemovePostCollaborator(context.Context, *connect.Request[managev1.RemovePostCollaboratorRequest]) (*connect.Response[managev1.DeleteResponse], error)
	ListPostVersions(context.Context, *connect.Request[managev1.ListPostVersionsRequest]) (*connect.Response[managev1.ListPostVersionsResponse], error)
	RestorePostVersion(context.Context, *connect.Request[managev1.RestorePostVersionRequest]) (*connect.Response[managev1.Post], error)
	CheckSlugAvailable(context.Context, *connect.Request[managev1.CheckSlugAvailableRequest]) (*connect.Response[managev1.CheckSlugAvailableResponse], error)
}

type WorkRelatedApplication interface {
	SetWorkFeaturedImage(context.Context, *connect.Request[managev1.SetWorkFeaturedImageRequest]) (*connect.Response[managev1.SetWorkFeaturedImageResponse], error)
	DeleteWorkFeaturedImage(context.Context, *connect.Request[managev1.DeleteWorkFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error)
	GetWorkCredits(context.Context, *connect.Request[managev1.GetWorkCreditsRequest]) (*connect.Response[managev1.GetWorkCreditsResponse], error)
	CreateWorkCreditGroup(context.Context, *connect.Request[managev1.CreateWorkCreditGroupRequest]) (*connect.Response[managev1.WorkCreditGroup], error)
	UpdateWorkCreditGroup(context.Context, *connect.Request[managev1.UpdateWorkCreditGroupRequest]) (*connect.Response[managev1.WorkCreditGroup], error)
	DeleteWorkCreditGroup(context.Context, *connect.Request[managev1.DeleteWorkCreditGroupRequest]) (*connect.Response[managev1.DeleteResponse], error)
	AddWorkCredit(context.Context, *connect.Request[managev1.AddWorkCreditRequest]) (*connect.Response[managev1.WorkCredit], error)
	UpdateWorkCredit(context.Context, *connect.Request[managev1.UpdateWorkCreditRequest]) (*connect.Response[managev1.WorkCredit], error)
	DeleteWorkCredit(context.Context, *connect.Request[managev1.DeleteWorkCreditRequest]) (*connect.Response[managev1.DeleteResponse], error)
	ListWorkVersions(context.Context, *connect.Request[managev1.ListWorkVersionsRequest]) (*connect.Response[managev1.ListWorkVersionsResponse], error)
	RestoreWorkVersion(context.Context, *connect.Request[managev1.RestoreWorkVersionRequest]) (*connect.Response[managev1.Work], error)
	CheckWorkSlugAvailable(context.Context, *connect.Request[managev1.CheckWorkSlugAvailableRequest]) (*connect.Response[managev1.CheckWorkSlugAvailableResponse], error)
}

type PageRelatedApplication interface {
	SetPageFeaturedImage(context.Context, *connect.Request[managev1.SetPageFeaturedImageRequest]) (*connect.Response[managev1.SetPageFeaturedImageResponse], error)
	DeletePageFeaturedImage(context.Context, *connect.Request[managev1.DeletePageFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error)
	ListPageVersions(context.Context, *connect.Request[managev1.ListPageVersionsRequest]) (*connect.Response[managev1.ListPageVersionsResponse], error)
	RestorePageVersion(context.Context, *connect.Request[managev1.RestorePageVersionRequest]) (*connect.Response[managev1.Page], error)
	CheckPageSlugAvailable(context.Context, *connect.Request[managev1.CheckPageSlugAvailableRequest]) (*connect.Response[managev1.CheckPageSlugAvailableResponse], error)
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
	var input featuredImageArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if name == ToolDocumentFeaturedImageSet {
		var runID *string
		switch input.DocumentType {
		case "post":
			response, err := tools.posts.SetPostFeaturedImage(ctx, connect.NewRequest(&managev1.SetPostFeaturedImageRequest{PostId: input.DocumentID, FileId: input.FileID}))
			if err != nil {
				return expectedToolError(err)
			}
			runID = response.Msg.OgGenerationRunId
		case "work":
			response, err := tools.works.SetWorkFeaturedImage(ctx, connect.NewRequest(&managev1.SetWorkFeaturedImageRequest{WorkId: input.DocumentID, FileId: input.FileID}))
			if err != nil {
				return expectedToolError(err)
			}
			runID = response.Msg.OgGenerationRunId
		case "page":
			response, err := tools.pages.SetPageFeaturedImage(ctx, connect.NewRequest(&managev1.SetPageFeaturedImageRequest{PageId: input.DocumentID, FileId: input.FileID}))
			if err != nil {
				return expectedToolError(err)
			}
			runID = response.Msg.OgGenerationRunId
		default:
			return executionError(fmt.Errorf("document_type must be post, work, or page"))
		}
		return contentResult(map[string]any{"resource_type": "featured_image", "resource_id": input.FileID, "document_type": input.DocumentType, "document_id": input.DocumentID, "file_id": input.FileID, "changed": true, "og_generation_run_id": optionalStringValue(runID)})
	}
	var response *connect.Response[managev1.OgAssetDeleteResponse]
	var err error
	switch input.DocumentType {
	case "post":
		response, err = tools.posts.DeletePostFeaturedImage(ctx, connect.NewRequest(&managev1.DeletePostFeaturedImageRequest{PostId: input.DocumentID}))
	case "work":
		response, err = tools.works.DeleteWorkFeaturedImage(ctx, connect.NewRequest(&managev1.DeleteWorkFeaturedImageRequest{WorkId: input.DocumentID}))
	case "page":
		response, err = tools.pages.DeletePageFeaturedImage(ctx, connect.NewRequest(&managev1.DeletePageFeaturedImageRequest{PageId: input.DocumentID}))
	default:
		return executionError(fmt.Errorf("document_type must be post, work, or page"))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"resource_type": "featured_image", "resource_id": input.DocumentID, "document_type": input.DocumentType, "document_id": input.DocumentID, "changed": response.Msg.Success, "deleted": response.Msg.Success, "og_generation_run_id": optionalStringValue(response.Msg.OgGenerationRunId)})
}

func (tools *ContentRelatedTools) listPostParticipants(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	response, err := tools.posts.ListPostParticipants(ctx, connect.NewRequest(&managev1.ListPostParticipantsRequest{PostId: input.DocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	participants := make([]map[string]any, 0, len(response.Msg.Participants))
	for _, participant := range response.Msg.Participants {
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
	changed := true
	switch name {
	case ToolPostAuthorAdd:
		_, err := tools.posts.AddPostAuthor(ctx, connect.NewRequest(&managev1.AddPostAuthorRequest{PostId: input.DocumentID, MemberId: input.MemberID}))
		if err != nil {
			return expectedToolError(err)
		}
	case ToolPostAuthorRemove:
		response, err := tools.posts.RemovePostAuthor(ctx, connect.NewRequest(&managev1.RemovePostAuthorRequest{PostId: input.DocumentID, MemberId: input.MemberID}))
		if err != nil {
			return expectedToolError(err)
		}
		changed = response.Msg.Success
	case ToolPostCollaboratorAdd:
		_, err := tools.posts.AddPostCollaborator(ctx, connect.NewRequest(&managev1.AddPostCollaboratorRequest{PostId: input.DocumentID, MemberId: input.MemberID}))
		if err != nil {
			return expectedToolError(err)
		}
	case ToolPostCollaboratorRemove:
		response, err := tools.posts.RemovePostCollaborator(ctx, connect.NewRequest(&managev1.RemovePostCollaboratorRequest{PostId: input.DocumentID, MemberId: input.MemberID}))
		if err != nil {
			return expectedToolError(err)
		}
		changed = response.Msg.Success
	}
	return contentResult(map[string]any{"resource_type": "post_participant", "resource_id": input.MemberID, "document_type": "post", "document_id": input.DocumentID, "member_id": input.MemberID, "role": role, "changed": changed, "deleted": name == ToolPostAuthorRemove || name == ToolPostCollaboratorRemove})
}

func (tools *ContentRelatedTools) getWorkCredits(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input contentIDArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	response, err := tools.works.GetWorkCredits(ctx, connect.NewRequest(&managev1.GetWorkCreditsRequest{WorkId: input.DocumentID}))
	if err != nil {
		return expectedToolError(err)
	}
	groups := make([]map[string]any, 0, len(response.Msg.Groups))
	for _, group := range response.Msg.Groups {
		if group != nil {
			groups = append(groups, map[string]any{"id": group.Id, "name": group.Name})
		}
	}
	credits := make([]map[string]any, 0, len(response.Msg.Credits))
	for _, credit := range response.Msg.Credits {
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

func (tools *ContentRelatedTools) mutateWorkCreditGroup(ctx context.Context, toolName string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input workCreditGroupArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	output := map[string]any{"resource_type": "work_credit_group", "changed": true}
	switch toolName {
	case ToolWorkCreditGroupCreate:
		response, err := tools.works.CreateWorkCreditGroup(ctx, connect.NewRequest(&managev1.CreateWorkCreditGroupRequest{WorkId: input.DocumentID, Name: input.Name}))
		if err != nil {
			return expectedToolError(err)
		}
		output["resource_id"], output["document_type"], output["document_id"], output["name"] = response.Msg.Id, "work", input.DocumentID, response.Msg.Name
	case ToolWorkCreditGroupUpdate:
		response, err := tools.works.UpdateWorkCreditGroup(ctx, connect.NewRequest(&managev1.UpdateWorkCreditGroupRequest{GroupId: input.GroupID, Name: &input.Name}))
		if err != nil {
			return expectedToolError(err)
		}
		output["resource_id"], output["name"] = response.Msg.Id, response.Msg.Name
	case ToolWorkCreditGroupDelete:
		response, err := tools.works.DeleteWorkCreditGroup(ctx, connect.NewRequest(&managev1.DeleteWorkCreditGroupRequest{GroupId: input.GroupID}))
		if err != nil {
			return expectedToolError(err)
		}
		output["resource_id"], output["changed"], output["deleted"] = input.GroupID, response.Msg.Success, response.Msg.Success
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

func (tools *ContentRelatedTools) mutateWorkCredit(ctx context.Context, toolName string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input workCreditArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	output := map[string]any{"resource_type": "work_credit", "changed": true}
	switch toolName {
	case ToolWorkCreditAdd:
		response, err := tools.works.AddWorkCredit(ctx, connect.NewRequest(&managev1.AddWorkCreditRequest{WorkId: input.DocumentID, GroupId: input.GroupID, ArtistId: input.ArtistID, MemberId: input.MemberID, Name: input.Name, CreditRole: input.CreditRole}))
		if err != nil {
			return expectedToolError(err)
		}
		output["resource_id"], output["document_type"], output["document_id"] = response.Msg.Id, "work", input.DocumentID
		copyWorkCreditMutationFields(output, response.Msg)
	case ToolWorkCreditUpdate:
		response, err := tools.works.UpdateWorkCredit(ctx, connect.NewRequest(&managev1.UpdateWorkCreditRequest{CreditId: input.CreditID, GroupId: input.GroupID, CreditRole: input.CreditRole}))
		if err != nil {
			return expectedToolError(err)
		}
		output["resource_id"] = response.Msg.Id
		copyWorkCreditMutationFields(output, response.Msg)
	case ToolWorkCreditDelete:
		response, err := tools.works.DeleteWorkCredit(ctx, connect.NewRequest(&managev1.DeleteWorkCreditRequest{CreditId: input.CreditID}))
		if err != nil {
			return expectedToolError(err)
		}
		output["resource_id"], output["changed"], output["deleted"] = input.CreditID, response.Msg.Success, response.Msg.Success
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
	var input documentVersionsArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if input.Limit == 0 {
		input.Limit = 20
	}
	pagination := &commonv1.PaginationRequest{Limit: input.Limit, Offset: input.Offset}
	versions := make([]map[string]any, 0)
	var page *commonv1.PaginationResponse
	switch input.DocumentType {
	case "post":
		response, err := tools.posts.ListPostVersions(ctx, connect.NewRequest(&managev1.ListPostVersionsRequest{PostId: input.DocumentID, Pagination: pagination}))
		if err != nil {
			return expectedToolError(err)
		}
		page = response.Msg.Pagination
		for _, version := range response.Msg.Versions {
			if version != nil {
				versions = append(versions, versionOutput(version.Id, version.Version, version.Title, version.Summary, version.CanonicalHash, version.SourceLocale, version.CreatedAt, version.Contributors))
			}
		}
	case "work":
		response, err := tools.works.ListWorkVersions(ctx, connect.NewRequest(&managev1.ListWorkVersionsRequest{WorkId: input.DocumentID, Pagination: pagination}))
		if err != nil {
			return expectedToolError(err)
		}
		page = response.Msg.Pagination
		for _, version := range response.Msg.Versions {
			if version != nil {
				versions = append(versions, versionOutput(version.Id, version.Version, version.Title, version.Summary, version.CanonicalHash, version.SourceLocale, version.CreatedAt, version.Contributors))
			}
		}
	case "page":
		response, err := tools.pages.ListPageVersions(ctx, connect.NewRequest(&managev1.ListPageVersionsRequest{PageId: input.DocumentID, Pagination: pagination}))
		if err != nil {
			return expectedToolError(err)
		}
		page = response.Msg.Pagination
		for _, version := range response.Msg.Versions {
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
			output["next_offset"] = page.Offset + page.Limit
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
		response, err := tools.posts.RestorePostVersion(ctx, connect.NewRequest(&managev1.RestorePostVersionRequest{PostId: input.DocumentID, VersionId: input.VersionID}))
		if err != nil {
			return expectedToolError(err)
		}
		output["title"], output["document_revision"], output["status"] = response.Msg.Title, response.Msg.Revision, contentStatus(response.Msg.Status.String(), "POST_STATUS_")
	case "work":
		response, err := tools.works.RestoreWorkVersion(ctx, connect.NewRequest(&managev1.RestoreWorkVersionRequest{WorkId: input.DocumentID, VersionId: input.VersionID}))
		if err != nil {
			return expectedToolError(err)
		}
		output["title"], output["document_revision"], output["status"] = response.Msg.Title, response.Msg.Revision, contentStatus(response.Msg.Status.String(), "WORK_STATUS_")
	case "page":
		response, err := tools.pages.RestorePageVersion(ctx, connect.NewRequest(&managev1.RestorePageVersionRequest{PageId: input.DocumentID, VersionId: input.VersionID}))
		if err != nil {
			return expectedToolError(err)
		}
		output["title"], output["document_revision"], output["status"] = response.Msg.Title, response.Msg.Revision, contentStatus(response.Msg.Status.String(), "PAGE_STATUS_")
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
		response, err := tools.posts.CheckSlugAvailable(ctx, connect.NewRequest(&managev1.CheckSlugAvailableRequest{Slug: input.Slug, ExcludePostId: input.ExcludeDocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		available = response.Msg.Available
	case "work":
		response, err := tools.works.CheckWorkSlugAvailable(ctx, connect.NewRequest(&managev1.CheckWorkSlugAvailableRequest{Slug: input.Slug, ExcludeWorkId: input.ExcludeDocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		available = response.Msg.Available
	case "page":
		response, err := tools.pages.CheckPageSlugAvailable(ctx, connect.NewRequest(&managev1.CheckPageSlugAvailableRequest{Slug: input.Slug, ExcludeId: input.ExcludeDocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		available = response.Msg.Available
	default:
		return executionError(fmt.Errorf("document_type must be post, work, or page"))
	}
	return contentResult(map[string]any{"document_type": input.DocumentType, "slug": input.Slug, "available": available})
}

var _ ToolProvider = (*ContentRelatedTools)(nil)
