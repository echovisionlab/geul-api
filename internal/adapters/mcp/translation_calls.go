package mcp

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (tools *TranslationTools) listJobs(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input translationJobsListArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	request, err := translationJobsListRequest(input)
	if err != nil {
		return executionError(err)
	}
	response, err := tools.application.ListTranslationJobs(ctx, connect.NewRequest(request))
	if err != nil {
		return translationCallError(err)
	}
	if response == nil || response.Msg == nil || response.Msg.Pagination == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete Job list")
	}
	return translationStructuredResult(encodeTranslationJobList(response.Msg.Jobs, response.Msg.Pagination))
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
	response, err := tools.application.ListEntityTranslations(ctx, connect.NewRequest(&managev1.ListEntityTranslationsRequest{Target: target}))
	if err != nil {
		return translationCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete locale list")
	}
	return translationStructuredResult(encodeTranslationEntries(target, response.Msg.SourceLocale, response.Msg.Entries))
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
	response, err := tools.application.GetEntityTranslation(ctx, connect.NewRequest(&managev1.GetEntityTranslationRequest{
		Target: target, Locale: string(input.Locale),
	}))
	if err != nil {
		return translationCallError(err)
	}
	if response == nil || response.Msg == nil || response.Msg.Entry == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete locale entry")
	}
	return translationStructuredResult(encodeTranslationEntry(target, response.Msg.Entry))
}

func (tools *TranslationTools) cancelJob(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input translationJobArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("job ID", input.JobID); err != nil {
		return executionError(err)
	}
	response, err := tools.application.CancelTranslationJob(ctx, connect.NewRequest(&managev1.CancelTranslationJobRequest{JobId: input.JobID}))
	if err != nil {
		return translationCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete cancellation result")
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
	response, err := tools.application.RegenerateEntityTranslations(ctx, connect.NewRequest(&managev1.RegenerateEntityTranslationsRequest{
		Target: target, Locales: locales,
	}))
	if err != nil {
		return translationCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete regeneration result")
	}
	return translationStructuredResult(encodeTranslationJobs(response.Msg.Jobs))
}

func (tools *TranslationTools) exportXLIFF(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	request, err := translationXLIFFExportRequest(arguments)
	if err != nil {
		return executionError(err)
	}
	response, err := tools.application.ExportEntityTranslationXLIFF(ctx, connect.NewRequest(request))
	if err != nil {
		return translationCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete XLIFF export")
	}
	return translationStructuredResult(encodeXLIFFExport(response.Msg))
}

func (tools *TranslationTools) importXLIFF(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	request, err := translationXLIFFImportRequest(arguments)
	if err != nil {
		return executionError(err)
	}
	response, err := tools.application.ImportEntityTranslationXLIFF(ctx, connect.NewRequest(request))
	if err != nil {
		return translationCallError(err)
	}
	if response == nil || response.Msg == nil {
		return mcpserver.ToolResult{}, errors.New("translation application returned an incomplete XLIFF import")
	}
	return translationStructuredResult(encodeXLIFFImport(response.Msg))
}

func translationCallError(err error) (mcpserver.ToolResult, error) {
	if err == nil {
		return mcpserver.ToolResult{}, nil
	}
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeAlreadyExists,
		connect.CodePermissionDenied, connect.CodeUnauthenticated,
		connect.CodeFailedPrecondition, connect.CodeAborted, connect.CodeOutOfRange:
		return executionError(err)
	default:
		return mcpserver.ToolResult{}, err
	}
}
