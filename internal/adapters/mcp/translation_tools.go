package mcp

import (
	"context"
	"errors"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

const (
	ToolTranslationList        = "translation_list"
	ToolTranslationGet         = "translation_get"
	ToolTranslationXLIFFExport = "translation_xliff_export"
	ToolTranslationXLIFFImport = "translation_xliff_import"
	ToolTranslationUnitsGet    = "translation_units_get"
	ToolTranslationUnitsPut    = "translation_units_put"
)

var translationTools = toolRegistry[TranslationTools]{
	defineTool(mcpserver.Tool{
		Name: ToolTranslationList, Title: "List document translations",
		Description: "List locale presence metadata for one document. Read translated fields with document_read.",
		InputSchema: translationTargetInputSchema(), OutputSchema: translationListOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*TranslationTools).listTranslations),
	defineTool(mcpserver.Tool{
		Name: ToolTranslationGet, Title: "Get document translation metadata",
		Description: "Get locale presence metadata without returning editor-native content. Read content with document_read.",
		InputSchema: translationGetInputSchema(), OutputSchema: translationGetOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*TranslationTools).getTranslation),
	defineTool(mcpserver.Tool{
		Name: ToolTranslationXLIFFExport, Title: "Export XLIFF 2.2",
		Description: "Create a short-lived XLIFF 2.2 artifact reference. XML is never returned in tool content.",
		InputSchema: translationXLIFFExportInputSchema(), OutputSchema: translationXLIFFExportOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*TranslationTools).exportXLIFF),
	defineTool(mcpserver.Tool{
		Name: ToolTranslationXLIFFImport, Title: "Import XLIFF 2.2",
		Description: "Import a completed existing File upload by file_id with explicit patch or replace semantics.",
		InputSchema: translationXLIFFImportInputSchema(), OutputSchema: translationXLIFFImportOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*TranslationTools).importXLIFF),
	defineTool(mcpserver.Tool{
		Name: ToolTranslationUnitsGet, Title: "Get translation units",
		Description: "Read the source and current target of a document's translation units for one target locale, " +
			"without an XLIFF file. Units use XLIFF 2.2 inline markup; translate them and write back with translation_units_put.",
		InputSchema: translationUnitsGetInputSchema(), OutputSchema: translationUnitsGetOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*TranslationTools).getTranslationUnits),
	defineTool(mcpserver.Tool{
		Name: ToolTranslationUnitsPut, Title: "Put translation units",
		Description: "Write target translations for named units of one target locale. Other units keep their values. " +
			"Each target must keep its source's inline codes with the same ids and nesting.",
		InputSchema: translationUnitsPutInputSchema(), OutputSchema: translationXLIFFImportOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, true, false),
		Meta:            oauthSecurityMeta(),
	}, (*TranslationTools).putTranslationUnits),
}

// TranslationApplication is the Translation use cases the tools call, the
// same ones the Connect transport serves, so PAT actor context,
// owning-domain authorization and XLIFF authority are not
// reimplemented here. This public MCP adapter does not own or call Web AI
// Chat, browser sessions, prompts, models, provider credentials or an
// in-editor provider loop.
type TranslationApplication interface {
	ListEntityTranslations(context.Context, translationapp.ListEntityTranslationsQuery) (translationapp.EntityTranslations, error)
	GetEntityTranslation(context.Context, translationapp.GetEntityTranslationQuery) (translationapp.Entry, error)
	ExportEntityTranslationXLIFF(context.Context, translationapp.ExportTranslationXLIFFCommand) (translationapp.XLIFFExport, error)
	ImportEntityTranslationXLIFF(context.Context, translationapp.ImportTranslationXLIFFCommand) (translationapp.XLIFFImport, error)
	GetEntityTranslationUnits(context.Context, translationapp.GetTranslationUnitsQuery) (translationapp.TranslationUnits, error)
	PutEntityTranslationUnits(context.Context, translationapp.PutTranslationUnitsCommand) (translationapp.XLIFFImport, error)
}

type TranslationTools struct {
	application TranslationApplication
}

func NewTranslationTools(application TranslationApplication) (*TranslationTools, error) {
	if interfaceValueIsNil(application) {
		return nil, errors.New("MCP Translation application is required")
	}
	return &TranslationTools{application: application}, nil
}

func (*TranslationTools) ToolNames() []string { return translationTools.names() }

func (*TranslationTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return translationTools.list(), nil
}

func (tools *TranslationTools) CallTool(
	ctx context.Context,
	_ mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	return translationTools.call(tools, ctx, name, arguments)
}

var (
	_ mcpserver.ToolRegistry   = (*TranslationTools)(nil)
	_ mcpserver.ToolDispatcher = (*TranslationTools)(nil)
	_ ToolProvider             = (*TranslationTools)(nil)
)
