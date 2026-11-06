package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/work"
)

const (
	ToolDocumentPublish     = "document_publish"
	ToolDocumentDelete      = "document_delete"
	ToolDocumentSettingsGet = "document_settings_get"
	ToolDocumentSlugCheck   = "document_slug_check"
)

var documentOperationTools = toolRegistry[DocumentOperationTools]{
	defineTool(oauthTool(ToolDocumentPublish, "Change document publication",
		"Publish, unpublish, archive, republish, schedule or cancel the schedule of one or many documents, of any mix of types, in one call. Each document stands alone and the result reports each. Post/Page/Release unpublish returns to draft; archived Posts can return to draft. Legal policies have their own policy_schedule and policy_activate_now, which send notice email.",
		documentPublishInputJSONSchema, documentOperationOutputJSONSchema, false, false), (*DocumentOperationTools).publish),
	defineTool(oauthTool(ToolDocumentDelete, "Delete documents",
		"Permanently delete one or many documents, of any mix of types, when their current lifecycle state allows it. Each document stands alone and the result reports each.",
		documentDeleteInputJSONSchema, documentOperationOutputJSONSchema, false, true), (*DocumentOperationTools).delete),
	defineTool(oauthTool(ToolDocumentSettingsGet, "Get document settings",
		"Read a Post, Page, Work, Release or Program Event's settings, publication state and allowed actions without its body. Read this before the matching *_settings_update.",
		documentSettingsGetInputJSONSchema, documentSettingsGetOutputJSONSchema, true, false), (*DocumentOperationTools).settings),
	defineTool(oauthTool(ToolDocumentSlugCheck, "Check document slug",
		"Check whether a slug is free for a Post, Page, Work or Release. Pass exclude_document_id when checking a document's own slug.",
		documentSlugCheckInputJSONSchema, documentSlugCheckOutputJSONSchema, true, false), (*DocumentOperationTools).checkSlug),
}

// DocumentOperationApplications holds the use cases of the operations document
// types share. Each one keeps its own authorization and audit.
type DocumentOperationApplications struct {
	Posts    PostOperationApplication
	Works    WorkOperationApplication
	Pages    PageOperationApplication
	Releases ReleaseOperationApplication
	Events   ProgramEventOperationApplication
}

// DocumentOperationTools gives the operations every document type shares one tool
// each.
type DocumentOperationTools struct {
	applications DocumentOperationApplications
}

func NewDocumentOperationTools(applications DocumentOperationApplications) (*DocumentOperationTools, error) {
	switch {
	case interfaceValueIsNil(applications.Posts):
		return nil, errors.New("MCP document operation tools need the Post application")
	case interfaceValueIsNil(applications.Works):
		return nil, errors.New("MCP document operation tools need the Work application")
	case interfaceValueIsNil(applications.Pages):
		return nil, errors.New("MCP document operation tools need the Page application")
	case interfaceValueIsNil(applications.Releases):
		return nil, errors.New("MCP document operation tools need the Release application")
	case interfaceValueIsNil(applications.Events):
		return nil, errors.New("MCP document operation tools need the Program Event application")
	}
	return &DocumentOperationTools{applications: applications}, nil
}

func (*DocumentOperationTools) ToolNames() []string { return documentOperationTools.names() }

func (*DocumentOperationTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return documentOperationTools.list(), nil
}

func (tools *DocumentOperationTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return documentOperationTools.call(tools, ctx, name, arguments)
}

type documentBatchTarget struct {
	DocumentType core.Domain            `json:"document_type"`
	DocumentID   core.DocumentReference `json:"document_id"`
}

type documentPublishArguments struct {
	Action            string                `json:"action"`
	Documents         []documentBatchTarget `json:"documents"`
	ScheduledAt       *string               `json:"scheduled_at,omitempty"`
	ScheduledTimeZone *string               `json:"scheduled_time_zone,omitempty"`
}

// postSchedule reads the time of the schedule action, and refuses a time given
// with any other action.
func (input documentPublishArguments) postSchedule() (postSchedule, error) {
	if input.Action != documentActionSchedule {
		if input.ScheduledAt != nil || input.ScheduledTimeZone != nil {
			return postSchedule{}, errors.New("scheduled_at and scheduled_time_zone go with action schedule, and only with it")
		}
		return postSchedule{}, nil
	}
	if input.ScheduledAt == nil || input.ScheduledTimeZone == nil {
		return postSchedule{}, errors.New("schedule needs scheduled_at and scheduled_time_zone")
	}
	at, err := time.Parse(time.RFC3339, *input.ScheduledAt)
	if err != nil {
		return postSchedule{}, fmt.Errorf("scheduled_at must be RFC 3339: %w", err)
	}
	return postSchedule{at: at, timeZone: *input.ScheduledTimeZone}, nil
}

type documentDeleteArguments struct {
	Documents []documentBatchTarget `json:"documents"`
}

type documentOperationResult struct {
	documentOutcome
	Changed *bool `json:"changed,omitempty"`
}

func (tools *DocumentOperationTools) publish(ctx context.Context, _ string, call toolCall[documentPublishArguments]) (mcpserver.ToolResult, error) {
	var input documentPublishArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	schedule, err := input.postSchedule()
	if err != nil {
		return executionError(err)
	}
	actions, known := tools.publicationActions(schedule)[input.Action]
	if !known {
		return executionError(fmt.Errorf("unsupported action %q", input.Action))
	}
	return runOnEach(ctx, input.Action, actions, input.Documents)
}

func (tools *DocumentOperationTools) delete(ctx context.Context, _ string, call toolCall[documentDeleteArguments]) (mcpserver.ToolResult, error) {
	var input documentDeleteArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	return runOnEach(ctx, "delete", tools.deleteActions(), input.Documents)
}

// runOnEach refuses the whole call before acting when a document cannot take the
// action; after that each document stands alone.
func runOnEach(ctx context.Context, actionName string, actions map[core.Domain]documentAction, documents []documentBatchTarget) (mcpserver.ToolResult, error) {
	if len(documents) == 0 || len(documents) > documentBatchMaxDocuments {
		return executionError(fmt.Errorf("documents must name 1 to %d documents", documentBatchMaxDocuments))
	}
	for index, document := range documents {
		if _, supported := actions[document.DocumentType]; !supported {
			return executionError(fmt.Errorf("document %d: a %s has no action %s", index, document.DocumentType, actionName))
		}
		if err := validateUUID("document_id", string(document.DocumentID)); err != nil {
			return executionError(fmt.Errorf("document %d: %w", index, err))
		}
	}
	results := make([]documentOperationResult, 0, len(documents))
	for _, document := range documents {
		results = append(results, runDocumentAction(ctx, actions[document.DocumentType], document))
	}
	return encodeDocumentBatch(results)
}

func runDocumentAction(ctx context.Context, action documentAction, document documentBatchTarget) documentOperationResult {
	result := documentOperationResult{documentOutcome: documentOutcome{DocumentType: document.DocumentType, DocumentID: document.DocumentID, Status: documentStatusApplied}}
	changed, err := action(ctx, string(document.DocumentID))
	if err != nil {
		_, toolErr := expectedToolError(err)
		result.fail(ctx, toolErr)
		return result
	}
	result.Changed = &changed
	return result
}

type documentSettingsArguments struct {
	DocumentType core.Domain `json:"document_type"`
	DocumentID   string      `json:"document_id"`
}

func (tools *DocumentOperationTools) settings(ctx context.Context, _ string, call toolCall[documentSettingsArguments]) (mcpserver.ToolResult, error) {
	var input documentSettingsArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	switch input.DocumentType {
	case core.DomainPost:
		return tools.postSettings(ctx, input.DocumentID)
	case core.DomainWork:
		return tools.workSettings(ctx, input.DocumentID)
	case core.DomainPage:
		return tools.pageSettings(ctx, input.DocumentID)
	case core.DomainRelease:
		return tools.releaseSettings(ctx, input.DocumentID)
	case core.DomainProgramEvent:
		return tools.programEventSettings(ctx, input.DocumentID)
	}
	return executionError(errors.New("document_type must be post, page, work, release or program_event"))
}

type documentSlugCheckArguments struct {
	DocumentType      core.Domain `json:"document_type"`
	Slug              string      `json:"slug"`
	ExcludeDocumentID *string     `json:"exclude_document_id,omitempty"`
}

func (tools *DocumentOperationTools) checkSlug(ctx context.Context, _ string, call toolCall[documentSlugCheckArguments]) (mcpserver.ToolResult, error) {
	var input documentSlugCheckArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if _, supplied := call.Arguments["slug"]; !supplied {
		return executionError(errors.New("slug is required"))
	}
	if err := rejectNullArguments(call.Arguments, "slug", "exclude_document_id"); err != nil {
		return executionError(err)
	}
	if input.ExcludeDocumentID != nil {
		if err := validateUUID("exclude_document_id", *input.ExcludeDocumentID); err != nil {
			return executionError(err)
		}
	}
	available, err := tools.slugAvailable(ctx, input)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"document_type": input.DocumentType, "slug": input.Slug, "available": available})
}

func (tools *DocumentOperationTools) slugAvailable(ctx context.Context, input documentSlugCheckArguments) (bool, error) {
	switch input.DocumentType {
	case core.DomainPost:
		return tools.applications.Posts.CheckSlugAvailable(ctx, postdomain.CheckSlugAvailableQuery{Slug: input.Slug, ExcludePostID: input.ExcludeDocumentID})
	case core.DomainWork:
		return tools.applications.Works.CheckWorkSlugAvailable(ctx, work.CheckWorkSlugAvailableQuery{Slug: input.Slug, ExcludeWorkID: input.ExcludeDocumentID})
	case core.DomainPage:
		return tools.applications.Pages.CheckPageSlugAvailable(ctx, pagedomain.CheckPageSlugAvailableQuery{Slug: input.Slug, ExcludeID: input.ExcludeDocumentID})
	case core.DomainRelease:
		return tools.applications.Releases.CheckReleaseSlugAvailable(ctx, releasedomain.CheckReleaseSlugQuery{Slug: input.Slug, ExcludeReleaseID: input.ExcludeDocumentID})
	}
	return false, &core.InputError{Message: "document_type must be post, page, work or release"}
}

var (
	_ mcpserver.ToolRegistry   = (*DocumentOperationTools)(nil)
	_ mcpserver.ToolDispatcher = (*DocumentOperationTools)(nil)
	_ ToolProvider             = (*DocumentOperationTools)(nil)
)
