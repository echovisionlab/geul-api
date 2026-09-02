package mcp

import (
	"context"
	"errors"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

const (
	ToolTranslationJobsList    = "translation_jobs_list"
	ToolTranslationList        = "translation_list"
	ToolTranslationGet         = "translation_get"
	ToolTranslationJobCancel   = "translation_job_cancel"
	ToolTranslationRegenerate  = "translation_regenerate"
	ToolTranslationXLIFFExport = "translation_xliff_export"
	ToolTranslationXLIFFImport = "translation_xliff_import"
)

var translationTools = []mcpserver.Tool{
	{
		Name: ToolTranslationJobsList, Title: "List translation jobs",
		Description: "List bounded translation Job metadata. Non-admin callers must provide one exact document target.",
		InputSchema: translationJobsListInputSchema(), OutputSchema: translationJobsOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolTranslationList, Title: "List document translations",
		Description: "List locale presence metadata for one document. Read translated fields with document_read.",
		InputSchema: translationTargetInputSchema(), OutputSchema: translationListOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolTranslationGet, Title: "Get document translation metadata",
		Description: "Get locale presence metadata without returning editor-native content. Read content with document_read.",
		InputSchema: translationGetInputSchema(), OutputSchema: translationGetOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolTranslationJobCancel, Title: "Cancel translation job",
		Description: "Cancel one queued or running translation Job under current source-document edit authority.",
		InputSchema: translationJobInputSchema(), OutputSchema: translationJobCancellationOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolTranslationRegenerate, Title: "Request document translations",
		Description: "Request or regenerate Translation Jobs for an explicit non-empty locale selection.",
		InputSchema: translationRegenerateInputSchema(), OutputSchema: translationJobsOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, true),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolTranslationXLIFFExport, Title: "Export XLIFF 2.2",
		Description: "Create a short-lived XLIFF 2.2 artifact reference. XML is never returned in tool content.",
		InputSchema: translationXLIFFExportInputSchema(), OutputSchema: translationXLIFFExportOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, false, false),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolTranslationXLIFFImport, Title: "Import XLIFF 2.2",
		Description: "Import a completed existing File upload by file_id with explicit patch or replace semantics.",
		InputSchema: translationXLIFFImportInputSchema(), OutputSchema: translationXLIFFImportOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	},
}

// TranslationApplication is the exact generated TranslationService handler
// contract used by the normal Connect transport. MCP calls the same
// application object so PAT actor context, owning-domain authorization, Job
// lifecycle and XLIFF authority are not reimplemented here. This public MCP
// adapter does not own or call Web AI Chat, browser sessions, prompts, models,
// provider credentials or an in-editor provider loop.
type TranslationApplication = managev1connect.TranslationServiceHandler

type TranslationTools struct {
	application TranslationApplication
}

func NewTranslationTools(application TranslationApplication) (*TranslationTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Translation application is required")
	}
	return &TranslationTools{application: application}, nil
}

func (*TranslationTools) ToolNames() []string {
	return toolDefinitionNames(translationTools)
}

func (*TranslationTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(translationTools), nil
}

func (tools *TranslationTools) CallTool(
	ctx context.Context,
	_ mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	switch name {
	case ToolTranslationJobsList:
		return tools.listJobs(ctx, arguments)
	case ToolTranslationList:
		return tools.listTranslations(ctx, arguments)
	case ToolTranslationGet:
		return tools.getTranslation(ctx, arguments)
	case ToolTranslationJobCancel:
		return tools.cancelJob(ctx, arguments)
	case ToolTranslationRegenerate:
		return tools.regenerate(ctx, arguments)
	case ToolTranslationXLIFFExport:
		return tools.exportXLIFF(ctx, arguments)
	case ToolTranslationXLIFFImport:
		return tools.importXLIFF(ctx, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

var (
	_ mcpserver.ToolRegistry   = (*TranslationTools)(nil)
	_ mcpserver.ToolDispatcher = (*TranslationTools)(nil)
	_ ToolProvider             = (*TranslationTools)(nil)
)
