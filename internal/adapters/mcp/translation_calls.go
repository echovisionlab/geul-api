package mcp

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

func (tools *TranslationTools) listJobs(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(arguments, "p", "d", "tl", "sl", "s", "n", "o", "k", "z"); err != nil {
		return executionError(err)
	}
	var input translationJobsListArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	query, err := translationJobsListQuery(input)
	if err != nil {
		return executionError(err)
	}
	page, err := tools.application.ListTranslationJobs(ctx, query)
	if err != nil {
		return translationCallError(err)
	}
	if page.Pagination == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete Job list")
	}
	return translationStructuredResult(encodeTranslationJobList(page.Jobs, page.Pagination))
}

func (tools *TranslationTools) listTranslations(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input translationTargetArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	target, err := translationTarget(input)
	if err != nil {
		return executionError(err)
	}
	translations, err := tools.application.ListEntityTranslations(ctx, translationapp.ListEntityTranslationsQuery{Target: target})
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeTranslationEntries(target, translations.SourceLocale, translations.Entries))
}

func (tools *TranslationTools) getTranslation(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input translationGetArguments
	if err := decodeArguments(arguments, &input); err != nil {
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
		Target: target, Locale: string(input.Locale),
	})
	if err != nil {
		return translationCallError(err)
	}
	if entry == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete locale entry")
	}
	return translationStructuredResult(encodeTranslationEntry(target, entry))
}

func (tools *TranslationTools) cancelJob(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input translationJobArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("job ID", input.JobID); err != nil {
		return executionError(err)
	}
	if err := tools.application.CancelTranslationJob(ctx, translationapp.CancelTranslationJobCommand{JobID: input.JobID}); err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeTranslationJobCancellation(input.JobID))
}

func (tools *TranslationTools) regenerate(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input translationRegenerateArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	target, err := translationTarget(input.translationTargetArguments)
	if err != nil {
		return executionError(err)
	}
	locales, err := translationLocales(input.Locales)
	if err != nil {
		return executionError(err)
	}
	jobs, err := tools.application.RegenerateEntityTranslations(ctx, translationapp.RegenerateEntityTranslationsCommand{
		Target: target, Locales: locales,
	})
	if err != nil {
		return translationCallError(err)
	}
	return translationStructuredResult(encodeTranslationJobs(jobs))
}

func (tools *TranslationTools) exportXLIFF(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	command, err := translationXLIFFExportCommand(arguments)
	if err != nil {
		return executionError(err)
	}
	exported, err := tools.application.ExportEntityTranslationXLIFF(ctx, command)
	if err != nil {
		return translationCallError(err)
	}
	if exported == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete XLIFF export")
	}
	return translationStructuredResult(encodeXLIFFExport(exported))
}

func (tools *TranslationTools) importXLIFF(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	command, err := translationXLIFFImportCommand(arguments)
	if err != nil {
		return executionError(err)
	}
	imported, err := tools.application.ImportEntityTranslationXLIFF(ctx, command)
	if err != nil {
		return translationCallError(err)
	}
	if imported == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete XLIFF import")
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
