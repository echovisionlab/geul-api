package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

const (
	ToolDocumentFileAdd                  = "document_file_add"
	ToolDocumentFileReplace              = "document_file_replace"
	ToolDocumentFileRemove               = "document_file_remove"
	ToolDocumentFileDownloadPolicyGet    = "document_file_download_policy_get"
	ToolDocumentFileDownloadPolicyUpdate = "document_file_download_policy_update"
	ToolFileUsageList                    = "file_usage_list"

	fileBlockKind          core.BlockKind = "file"
	fileBlockField         core.FieldID   = "attachment"
	fileBlockReferencePath                = "file"
)

var fileBlockTools = []mcpserver.Tool{
	{
		Name: ToolDocumentFileAdd, Title: "Add an existing File to a document",
		Description: "Add a new File Block that reuses an existing Geul File; this does not upload or copy bytes. " +
			"Read the document first and pass its exact current revision. New File Block download policy starts disabled.",
		InputSchema: json.RawMessage(documentFileAddInputJSONSchema), OutputSchema: json.RawMessage(documentFileMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, false, false), Meta: oauthSecurityMeta(),
	},
	{
		Name: ToolDocumentFileReplace, Title: "Replace a document File Block attachment",
		Description: "Replace the existing File attached to one File Block with another existing Geul File; this does not upload or delete File bytes. " +
			"The server verifies the target is a File Block and resets that attachment's download policy to disabled.",
		InputSchema: json.RawMessage(documentFileReplaceInputJSONSchema), OutputSchema: json.RawMessage(documentFileMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, true, false), Meta: oauthSecurityMeta(),
	},
	{
		Name: ToolDocumentFileRemove, Title: "Remove a File Block from a document",
		Description: "Remove one verified File Block and its attachment policy from a document. " +
			"The reusable Geul File and its bytes remain in File Manager.",
		InputSchema: json.RawMessage(documentFileRemoveInputJSONSchema), OutputSchema: json.RawMessage(documentFileMutationOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, true, false), Meta: oauthSecurityMeta(),
	},
	{
		Name: ToolDocumentFileDownloadPolicyGet, Title: "Read a File Block download policy",
		Description: "Read the download audience owned by one exact File Block attachment. " +
			"The server resolves the current File from document_type, document_id, and block_id; the caller does not assert File ownership.",
		InputSchema: json.RawMessage(documentFileDownloadPolicyGetInputJSONSchema), OutputSchema: json.RawMessage(documentFileDownloadPolicyOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(true, false, false), Meta: oauthSecurityMeta(),
	},
	{
		Name: ToolDocumentFileDownloadPolicyUpdate, Title: "Update a File Block download policy",
		Description: "Set disabled, public, authenticated, or restricted download access on one exact File Block attachment. " +
			"expected_file_id is only a compare-and-set guard against replacing a different current File; it is not relation authority. Public access can expose the original File outside Geul.",
		InputSchema: json.RawMessage(documentFileDownloadPolicyUpdateInputJSONSchema), OutputSchema: json.RawMessage(documentFileDownloadPolicyOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, true, true), Meta: oauthSecurityMeta(),
	},
	{
		Name: ToolFileUsageList, Title: "List File usages",
		Description: "List authorized Geul entities and exact Block attachment paths that currently reference one reusable File. " +
			"Use this before considering a physical File deletion or to find every document placement.",
		InputSchema: json.RawMessage(fileUsageListInputJSONSchema), OutputSchema: json.RawMessage(fileUsageListOutputJSONSchema),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(true, false, false), Meta: oauthSecurityMeta(),
	},
}

// FileBlockManagement is the exact-relation authorization and policy surface
// used by MCP. The adapter supplies relation selectors and never trusts a
// caller-provided File ID as relation authority.
type FileBlockManagement interface {
	GetFileDownloadPolicy(context.Context, *connect.Request[managev1.GetFileDownloadPolicyRequest]) (*connect.Response[managev1.GetFileDownloadPolicyResponse], error)
	UpdateFileDownloadPolicy(context.Context, *connect.Request[managev1.UpdateFileDownloadPolicyRequest]) (*connect.Response[managev1.UpdateFileDownloadPolicyResponse], error)
	ListFileUsages(context.Context, *connect.Request[managev1.ListFileUsagesRequest]) (*connect.Response[managev1.ListFileUsagesResponse], error)
}

// FileBlockTools translates focused File Block actions into the same exact
// document and File application services used by first-party transports.
type FileBlockTools struct {
	documents AIDocumentApplication
	files     FileBlockManagement
}

func NewFileBlockTools(documents AIDocumentApplication, files FileBlockManagement) (*FileBlockTools, error) {
	if interfaceValueIsNil(documents) {
		return nil, errors.New("MCP File Block document application is required")
	}
	if interfaceValueIsNil(files) {
		return nil, errors.New("MCP File Block management application is required")
	}
	return &FileBlockTools{documents: documents, files: files}, nil
}

func (*FileBlockTools) ToolNames() []string { return toolDefinitionNames(fileBlockTools) }

func (*FileBlockTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(fileBlockTools), nil
}

func (tools *FileBlockTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolDocumentFileAdd:
		return tools.add(ctx, arguments)
	case ToolDocumentFileReplace:
		return tools.replace(ctx, arguments)
	case ToolDocumentFileRemove:
		return tools.remove(ctx, arguments)
	case ToolDocumentFileDownloadPolicyGet:
		return tools.getDownloadPolicy(ctx, arguments)
	case ToolDocumentFileDownloadPolicyUpdate:
		return tools.updateDownloadPolicy(ctx, arguments)
	case ToolFileUsageList:
		return tools.listUsages(ctx, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

type documentFileAddArguments struct {
	focusedMutationArguments
	Parent core.BlockID `json:"parent_block_id,omitempty"`
	After  core.BlockID `json:"after_block_id,omitempty"`
	FileID string       `json:"file_id"`
}

type documentFileReplaceArguments struct {
	focusedMutationArguments
	Block  core.BlockID `json:"block_id"`
	FileID string       `json:"file_id"`
}

type documentFileRemoveArguments struct {
	focusedMutationArguments
	Block core.BlockID `json:"block_id"`
}

type documentFilePolicyArguments struct {
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
	BlockID      string `json:"block_id"`
}

type documentFilePolicyUpdateArguments struct {
	documentFilePolicyArguments
	ExpectedFileID     string   `json:"expected_file_id"`
	Audience           string   `json:"audience"`
	AudienceSegmentIDs []string `json:"audience_segment_ids,omitempty"`
}

type fileBlockSelectionError struct{ message string }

func (err *fileBlockSelectionError) Error() string { return err.message }

type fileUsageListArguments struct {
	FileID    string  `json:"file_id"`
	PageSize  int32   `json:"page_size,omitempty"`
	PageToken *string `json:"page_token,omitempty"`
}

func (tools *FileBlockTools) add(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input documentFileAddArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateFileBlockMutationIdentity(input.focusedMutationArguments); err != nil {
		return executionError(err)
	}
	if input.Profile == core.DomainPage && input.Parent == "" {
		return executionError(errors.New("parent_block_id is required for Page File Blocks"))
	}
	fileID, err := canonicalFileBlockUUID(input.FileID, "file_id")
	if err != nil {
		return executionError(err)
	}
	block := core.BlockID(uuid.NewString())
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{
		core.InsertBlockOperation(block, fileBlockKind, input.Parent, input.After),
		core.AttachFileOperation(block, fileBlockField, core.FileReference(fileID)),
	})
	if err != nil {
		return executionError(err)
	}
	return applyFocusedDocumentRequest(ctx, tools.documents, request, block)
}

func (tools *FileBlockTools) replace(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input documentFileReplaceArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateFileBlockMutationIdentity(input.focusedMutationArguments); err != nil {
		return executionError(err)
	}
	blockID, err := canonicalFileBlockUUID(string(input.Block), "block_id")
	if err != nil {
		return executionError(err)
	}
	fileID, err := canonicalFileBlockUUID(input.FileID, "file_id")
	if err != nil {
		return executionError(err)
	}
	if err := tools.requireFileBlock(ctx, input.focusedMutationArguments, core.BlockID(blockID)); err != nil {
		return expectedOrExecutionError(err)
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{
		core.AttachFileOperation(core.BlockID(blockID), fileBlockField, core.FileReference(fileID)),
	})
	if err != nil {
		return executionError(err)
	}
	return applyFocusedDocumentRequest(ctx, tools.documents, request, "")
}

func (tools *FileBlockTools) remove(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input documentFileRemoveArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateFileBlockMutationIdentity(input.focusedMutationArguments); err != nil {
		return executionError(err)
	}
	blockID, err := canonicalFileBlockUUID(string(input.Block), "block_id")
	if err != nil {
		return executionError(err)
	}
	if err := tools.requireFileBlock(ctx, input.focusedMutationArguments, core.BlockID(blockID)); err != nil {
		return expectedOrExecutionError(err)
	}
	request, err := focusedApplyRequest(input.focusedMutationArguments, []core.Operation{
		core.DeleteBlockOperation(core.BlockID(blockID)),
	})
	if err != nil {
		return executionError(err)
	}
	return applyFocusedDocumentRequest(ctx, tools.documents, request, "")
}

func (tools *FileBlockTools) requireFileBlock(ctx context.Context, input focusedMutationArguments, block core.BlockID) error {
	projection, err := tools.documents.Read(ctx, core.ReadRequest{
		Document: core.DocumentIdentity{Domain: input.Profile, Reference: input.Document},
		Locale:   input.Locale, Mode: core.ReadBlocks, Blocks: []core.BlockID{block}, Limit: 1,
	})
	if err != nil {
		return err
	}
	for _, node := range projection.Nodes {
		if node.ID == block {
			if node.Kind != fileBlockKind {
				return &fileBlockSelectionError{message: fmt.Sprintf("block_id %q identifies a %s Block, not a File Block", block, node.Kind)}
			}
			return nil
		}
	}
	return &fileBlockSelectionError{message: fmt.Sprintf("block_id %q was not found in the document", block)}
}

func applyFocusedDocumentRequest(ctx context.Context, application AIDocumentApplication, request core.ApplyRequest, createdBlock core.BlockID) (mcpserver.ToolResult, error) {
	tools := &AIDocumentTools{application: application}
	return tools.applyRequest(ctx, request, createdBlock)
}

func expectedOrExecutionError(err error) (mcpserver.ToolResult, error) {
	var selectionErr *fileBlockSelectionError
	if errors.As(err, &selectionErr) {
		return executionError(selectionErr)
	}
	return expectedToolError(err)
}

func validateFileBlockMutationIdentity(input focusedMutationArguments) error {
	if _, err := fileBlockEntityType(string(input.Profile)); err != nil {
		return err
	}
	_, err := canonicalFileBlockUUID(string(input.Document), "document_id")
	return err
}

func (tools *FileBlockTools) getDownloadPolicy(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input documentFilePolicyArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	request, err := fileBlockPolicySelector(input)
	if err != nil {
		return executionError(err)
	}
	response, err := tools.files.GetFileDownloadPolicy(ctx, connect.NewRequest(request))
	if err != nil {
		return fileToolCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("file policy service returned an empty response")
	}
	return fileBlockPolicyResult(response.Msg.Policy)
}

func (tools *FileBlockTools) updateDownloadPolicy(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input documentFilePolicyUpdateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	selector, err := fileBlockPolicySelector(input.documentFilePolicyArguments)
	if err != nil {
		return executionError(err)
	}
	expectedFileID, err := canonicalFileBlockUUID(input.ExpectedFileID, "expected_file_id")
	if err != nil {
		return executionError(err)
	}
	audience, err := fileDownloadAudience(input.Audience)
	if err != nil {
		return executionError(err)
	}
	segmentIDs := make([]string, len(input.AudienceSegmentIDs))
	if len(segmentIDs) > 20 {
		return executionError(errors.New("audience_segment_ids cannot contain more than 20 values"))
	}
	seen := make(map[string]struct{}, len(segmentIDs))
	for index, raw := range input.AudienceSegmentIDs {
		segmentIDs[index], err = canonicalFileBlockUUID(raw, fmt.Sprintf("audience_segment_ids[%d]", index))
		if err != nil {
			return executionError(err)
		}
		if _, duplicate := seen[segmentIDs[index]]; duplicate {
			return executionError(fmt.Errorf("audience_segment_ids contains duplicate UUID %q", segmentIDs[index]))
		}
		seen[segmentIDs[index]] = struct{}{}
	}
	if audience != managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED && len(segmentIDs) != 0 {
		return executionError(errors.New("audience_segment_ids are allowed only for restricted audience"))
	}
	response, err := tools.files.UpdateFileDownloadPolicy(ctx, connect.NewRequest(&managev1.UpdateFileDownloadPolicyRequest{
		EntityType: selector.EntityType, EntityId: selector.EntityId,
		BlockId: selector.BlockId, ReferencePath: selector.ReferencePath,
		ExpectedFileId: expectedFileID, Audience: audience, AudienceSegmentIds: segmentIDs,
	}))
	if err != nil {
		return fileToolCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("file policy service returned an empty response")
	}
	return fileBlockPolicyResult(response.Msg.Policy)
}

func fileBlockPolicySelector(input documentFilePolicyArguments) (*managev1.GetFileDownloadPolicyRequest, error) {
	entityType, err := fileBlockEntityType(input.DocumentType)
	if err != nil {
		return nil, err
	}
	documentID, err := canonicalFileBlockUUID(input.DocumentID, "document_id")
	if err != nil {
		return nil, err
	}
	blockID, err := canonicalFileBlockUUID(input.BlockID, "block_id")
	if err != nil {
		return nil, err
	}
	referencePath := fileBlockReferencePath
	return &managev1.GetFileDownloadPolicyRequest{
		EntityType: entityType, EntityId: documentID, BlockId: &blockID, ReferencePath: &referencePath,
	}, nil
}

func fileBlockEntityType(documentType string) (managev1.TranscodeEntityType, error) {
	switch documentType {
	case "post":
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, nil
	case "page":
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE, nil
	case "work":
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK, nil
	case "program_event":
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT, nil
	default:
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, fmt.Errorf("unsupported document_type %q", documentType)
	}
}

func documentTypeFromFileBlockEntity(entityType managev1.TranscodeEntityType) (string, error) {
	switch entityType {
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST:
		return "post", nil
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE:
		return "page", nil
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK:
		return "work", nil
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PROGRAM_EVENT:
		return "program_event", nil
	default:
		return "", fmt.Errorf("file policy service returned unsupported entity type %q", entityType)
	}
}

func fileDownloadAudience(value string) (managev1.FileDownloadAudience, error) {
	switch value {
	case "disabled":
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_DISABLED, nil
	case "public":
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC, nil
	case "authenticated":
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_AUTHENTICATED, nil
	case "restricted":
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED, nil
	default:
		return managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_UNSPECIFIED, fmt.Errorf("unsupported audience %q", value)
	}
}

func fileBlockPolicyResult(policy *managev1.FileDownloadPolicy) (mcpserver.ToolResult, error) {
	if policy == nil || policy.BlockId == nil || policy.ReferencePath == nil || policy.GetReferencePath() != fileBlockReferencePath {
		return mcpserver.ToolResult{}, errors.New("file policy service returned an invalid File Block selector")
	}
	documentType, err := documentTypeFromFileBlockEntity(policy.EntityType)
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	if _, err := canonicalFileBlockUUID(policy.EntityId, "policy document_id"); err != nil {
		return mcpserver.ToolResult{}, err
	}
	if _, err := canonicalFileBlockUUID(policy.GetBlockId(), "policy block_id"); err != nil {
		return mcpserver.ToolResult{}, err
	}
	if _, err := canonicalFileBlockUUID(policy.FileId, "policy file_id"); err != nil {
		return mcpserver.ToolResult{}, err
	}
	audience := strings.ToLower(strings.TrimPrefix(policy.Audience.String(), "FILE_DOWNLOAD_AUDIENCE_"))
	if _, err := fileDownloadAudience(audience); err != nil {
		return mcpserver.ToolResult{}, errors.New("file policy service returned an invalid audience")
	}
	segments := make([]map[string]any, 0, len(policy.AudienceSegments))
	if len(policy.AudienceSegments) > 20 {
		return mcpserver.ToolResult{}, errors.New("file policy service returned too many audience segments")
	}
	for _, segment := range policy.AudienceSegments {
		if segment == nil {
			return mcpserver.ToolResult{}, errors.New("file policy service returned an empty audience segment")
		}
		if _, err := canonicalFileBlockUUID(segment.Id, "policy audience segment ID"); err != nil {
			return mcpserver.ToolResult{}, err
		}
		segments = append(segments, map[string]any{"id": segment.Id, "name": segment.Name})
	}
	if audience != "restricted" && len(segments) != 0 {
		return mcpserver.ToolResult{}, errors.New("file policy service returned audience segments for a non-restricted policy")
	}
	return contentResult(map[string]any{
		"document_type": documentType, "document_id": policy.EntityId,
		"block_id": policy.GetBlockId(), "reference_path": policy.GetReferencePath(),
		"file_id": policy.FileId, "audience": audience, "audience_segments": segments,
	})
}

func (tools *FileBlockTools) listUsages(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input fileUsageListArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	fileID, err := canonicalFileBlockUUID(input.FileID, "file_id")
	if err != nil {
		return executionError(err)
	}
	if input.PageSize == 0 {
		input.PageSize = 20
	}
	response, err := tools.files.ListFileUsages(ctx, connect.NewRequest(&managev1.ListFileUsagesRequest{
		FileId: fileID, PageSize: input.PageSize, PageToken: input.PageToken,
	}))
	if err != nil {
		return fileToolCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("file usage service returned an empty response")
	}
	usages := make([]map[string]any, 0, len(response.Msg.Usages))
	for _, usage := range response.Msg.Usages {
		if usage == nil {
			return mcpserver.ToolResult{}, errors.New("file usage service returned an empty usage")
		}
		domain, err := fileUsageDomainName(usage.Domain)
		if err != nil {
			return mcpserver.ToolResult{}, err
		}
		if strings.TrimSpace(usage.EntityId) == "" || strings.TrimSpace(usage.ReferencePath) == "" || usage.Count < 1 {
			return mcpserver.ToolResult{}, errors.New("file usage service returned an invalid usage identity")
		}
		item := map[string]any{
			"domain": domain, "entity_id": usage.EntityId,
			"reference_path": usage.ReferencePath, "count": usage.Count,
		}
		if usage.BlockId != nil {
			item["block_id"] = *usage.BlockId
		}
		if usage.BlockType != nil {
			item["block_type"] = *usage.BlockType
		}
		if usage.Title != nil {
			item["title"] = *usage.Title
		}
		if usage.Link != nil {
			item["link"] = *usage.Link
		}
		usages = append(usages, item)
	}
	output := map[string]any{"file_id": fileID, "usages": usages, "total": response.Msg.Total}
	if response.Msg.NextPageToken != nil {
		output["next_page_token"] = *response.Msg.NextPageToken
	}
	return contentResult(output)
}

func fileUsageDomainName(domain managev1.FileUsageDomain) (string, error) {
	switch domain {
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_POST:
		return "post", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PAGE:
		return "page", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_WORK:
		return "work", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_SITE_SETTINGS:
		return "site_settings", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_RELEASE:
		return "release", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_TRACK:
		return "track", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_ARTIST:
		return "artist", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_LABEL:
		return "label", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_CLIENT:
		return "client", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_SERIES:
		return "series", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_FORM:
		return "form", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PROGRAM_EVENT:
		return "program_event", nil
	case managev1.FileUsageDomain_FILE_USAGE_DOMAIN_MAP_PLACE:
		return "map_place", nil
	default:
		return "", fmt.Errorf("file usage service returned unsupported domain %q", domain)
	}
}

func canonicalFileBlockUUID(value, field string) (string, error) {
	id, err := uuidutil.ParseCanonical(value, field)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

var _ ToolProvider = (*FileBlockTools)(nil)
