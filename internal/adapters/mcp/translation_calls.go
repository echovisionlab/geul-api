package mcp

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

func (tools *TranslationTools) listTranslations(ctx context.Context, _ string, call toolCall[translationTargetArguments]) (mcpserver.ToolResult, error) {
	var input translationTargetArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	target, err := translationTarget(input)
	if err != nil {
		return executionError(err)
	}
	translations, err := tools.application.ListEntityTranslations(ctx, translationapp.ListEntityTranslationsQuery{
		Target: translationapp.TargetFromMessage(target),
	})
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeTranslationEntries(
		target, translations.SourceLocale, translationapp.EntryMessages(translations.Entries),
	))
}

func (tools *TranslationTools) getTranslation(ctx context.Context, _ string, call toolCall[translationGetArguments]) (mcpserver.ToolResult, error) {
	var input translationGetArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	target, err := translationTarget(input.translationTargetArguments)
	if err != nil {
		return executionError(err)
	}
	if err := validateCompactLocale(input.Locale); err != nil {
		return executionError(err)
	}
	entry, err := tools.application.GetEntityTranslation(ctx, translationapp.GetEntityTranslationQuery{
		Target: translationapp.TargetFromMessage(target), Locale: string(input.Locale),
	})
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeTranslationEntry(target, translationapp.EntryMessage(entry)))
}

func (tools *TranslationTools) exportXLIFF(ctx context.Context, _ string, call toolCall[translationXLIFFExportArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments
	command, err := translationXLIFFExportCommand(arguments)
	if err != nil {
		return executionError(err)
	}
	exported, err := tools.application.ExportEntityTranslationXLIFF(ctx, command)
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeXLIFFExport(exported))
}

func (tools *TranslationTools) importXLIFF(ctx context.Context, _ string, call toolCall[translationXLIFFImportArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments
	command, err := translationXLIFFImportCommand(arguments)
	if err != nil {
		return executionError(err)
	}
	imported, err := tools.application.ImportEntityTranslationXLIFF(ctx, command)
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeXLIFFImport(imported))
}

func translationCallError(err error) (mcpserver.ToolResult, error) {
	if err == nil {
		return mcpserver.ToolResult{}, nil
	}
	classified, ok := errs.Classified(err)
	if !ok {
		return mcpserver.ToolResult{}, err
	}
	switch classified.Code() {
	case connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeAlreadyExists,
		connect.CodePermissionDenied, connect.CodeUnauthenticated,
		connect.CodeFailedPrecondition, connect.CodeAborted, connect.CodeOutOfRange:
		// The client sees the classified message, as the Connect boundary
		// renders it; the use-case cause stays server-side.
		return mcpserver.ToolResult{}, &mcpserver.ToolExecutionError{Message: classified.Error(), Cause: err}
	default:
		return mcpserver.ToolResult{}, err
	}
}
