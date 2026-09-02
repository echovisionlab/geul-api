package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

type translationProviderDocumentPinnedError struct {
	cause error
}

var (
	errTranslationProviderDocumentRejected = errors.New("translation provider rejected the document")
	errTranslationProviderResponseInvalid  = translation.ErrProviderResponseInvalid
)

func (translationProviderDocumentPinnedError) Error() string {
	return "translation provider document generation failed"
}
func (e translationProviderDocumentPinnedError) Unwrap() error { return e.cause }

func pinTranslationProviderDocumentError(err error) error {
	if err == nil {
		return nil
	}
	var pinned translationProviderDocumentPinnedError
	if errors.As(err, &pinned) {
		return err
	}
	return translationProviderDocumentPinnedError{cause: err}
}

func (m *TranslationJobManager) generateValidatedTranslation(
	ctx context.Context,
	job *model.TranslationJob,
	request translation.ProviderRequest,
	generators []translation.Generator,
	allowExpiredDocumentReplacement ...bool,
) (*translation.ProviderResponse, translation.Generator, error) {
	if len(generators) == 0 {
		return nil, nil, errTranslationProviderUnavailable
	}

	generator := generators[0]
	if m != nil && m.db != nil && job != nil && strings.TrimSpace(job.ID) != "" {
		if err := m.updateRunningJobProvider(ctx, job.ID, generator); err != nil {
			return nil, generator, err
		}
	}
	allowReplacement := len(allowExpiredDocumentReplacement) > 0 && allowExpiredDocumentReplacement[0]
	response, err := m.generateValidatedTranslationWithGenerator(ctx, job, request, generator, allowReplacement)
	return response, generator, err
}

func (m *TranslationJobManager) generateValidatedTranslationWithGenerator(
	ctx context.Context,
	job *model.TranslationJob,
	request translation.ProviderRequest,
	generator translation.Generator,
	allowExpiredDocumentReplacement ...bool,
) (*translation.ProviderResponse, error) {
	allowReplacement := len(allowExpiredDocumentReplacement) > 0 && allowExpiredDocumentReplacement[0]
	if documentGenerator, ok := generator.(translation.ResumableDocumentGenerator); ok {
		return m.generateValidatedResumableDocument(
			ctx,
			job,
			request,
			documentGenerator,
			allowReplacement,
		)
	}

	return m.generateValidatedTranslationRequest(ctx, request, generator)
}

func (m *TranslationJobManager) generateValidatedResumableDocument(
	ctx context.Context,
	job *model.TranslationJob,
	request translation.ProviderRequest,
	generator translation.ResumableDocumentGenerator,
	allowExpiredDocumentReplacement bool,
) (_ *translation.ProviderResponse, retErr error) {
	session, err := generator.StartDocumentSession(ctx, request)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("translation provider returned no document session")
	}
	providerPinned := false
	defer func() {
		if closeErr := session.Close(ctx); closeErr != nil && retErr == nil {
			if providerPinned {
				retErr = pinTranslationProviderDocumentError(closeErr)
			} else {
				retErr = closeErr
			}
		}
	}()

	handle, hasHandle, err := translationProviderDocumentHandle(job, generator)
	if err != nil {
		return nil, pinTranslationProviderDocumentError(err)
	}
	providerPinned = hasHandle
	upload := func() error {
		nextHandle, uploadErr := traceTranslationProviderOperation(
			ctx, "upload", generator.ProviderName(),
			func(callCtx context.Context) (translation.ProviderDocumentHandle, error) {
				return session.UploadDocument(callCtx, request)
			},
		)
		if uploadErr != nil {
			return uploadErr
		}
		providerPinned = true
		submittedAt := m.now().UTC()
		if err := m.persistTranslationProviderDocumentHandle(
			ctx,
			job.ID,
			generator.ProviderName(),
			generator.ModelName(),
			nextHandle,
			submittedAt,
		); err != nil {
			return err
		}
		handle = nextHandle
		hasHandle = true
		job.ProviderDocumentID = translationProviderDocumentStringPointer(nextHandle.DocumentID())
		job.ProviderDocumentKey = translationProviderDocumentStringPointer(nextHandle.DocumentKey())
		job.ProviderDocumentSubmittedAt = &submittedAt
		return nil
	}

	if !hasHandle {
		if err := upload(); err != nil {
			if providerPinned {
				return nil, pinTranslationProviderDocumentError(err)
			}
			return nil, err
		}
	}
	for {
		check, err := traceTranslationProviderOperation(
			ctx, "poll", generator.ProviderName(),
			func(callCtx context.Context) (translation.ProviderDocumentCheck, error) {
				return session.CheckDocument(callCtx, handle)
			},
		)
		if err != nil {
			if allowExpiredDocumentReplacement && errors.Is(err, translation.ErrProviderDocumentNotFound) {
				if err := m.replaceExpiredTranslationProviderDocument(ctx, job, upload); err != nil {
					return nil, pinTranslationProviderDocumentError(err)
				}
				allowExpiredDocumentReplacement = false
				continue
			}
			return nil, pinTranslationProviderDocumentError(err)
		}

		switch check.State {
		case translation.ProviderDocumentPending:
			if err := waitForTranslationProviderDocumentPoll(ctx, check.PollAfter); err != nil {
				return nil, pinTranslationProviderDocumentError(err)
			}
		case translation.ProviderDocumentComplete:
			response, err := traceTranslationProviderOperation(
				ctx, "download", generator.ProviderName(),
				func(callCtx context.Context) (*translation.ProviderResponse, error) {
					return session.DownloadDocument(callCtx, request, handle)
				},
			)
			if err != nil {
				return nil, pinTranslationProviderDocumentError(err)
			}
			if response == nil {
				return nil, pinTranslationProviderDocumentError(errTranslationProviderResponseInvalid)
			}
			validation := translation.ValidateResponse(request.Document, *response)
			if !validation.Passed {
				return nil, pinTranslationProviderDocumentError(errTranslationProviderResponseInvalid)
			}
			normalized := normalizeTranslationProviderResponse(request, *response)
			return &normalized, nil
		case translation.ProviderDocumentError:
			return nil, pinTranslationProviderDocumentError(errTranslationProviderDocumentRejected)
		case translation.ProviderDocumentNotFound:
			if allowExpiredDocumentReplacement {
				if err := m.replaceExpiredTranslationProviderDocument(ctx, job, upload); err != nil {
					return nil, pinTranslationProviderDocumentError(err)
				}
				allowExpiredDocumentReplacement = false
				continue
			}
			return nil, pinTranslationProviderDocumentError(translation.ErrProviderDocumentNotFound)
		default:
			return nil, pinTranslationProviderDocumentError(fmt.Errorf("translation provider returned an unsupported document state"))
		}
	}
}

func (m *TranslationJobManager) replaceExpiredTranslationProviderDocument(
	ctx context.Context,
	job *model.TranslationJob,
	upload func() error,
) error {
	if err := m.clearExpiredTranslationProviderDocumentHandle(ctx, job); err != nil {
		return err
	}
	return upload()
}

func queuedTranslationJobHasProviderDocument(job *model.TranslationJob) bool {
	return job != nil && job.Status == translationJobStatusQueued &&
		validateTranslationJobRequester(job) == nil &&
		job.Provider != nil && job.Model != nil &&
		job.ProviderDocumentID != nil && job.ProviderDocumentKey != nil &&
		job.ProviderDocumentSubmittedAt != nil && !job.ProviderDocumentSubmittedAt.IsZero()
}

func translationProviderDocumentHandle(
	job *model.TranslationJob,
	generator translation.Generator,
) (translation.ProviderDocumentHandle, bool, error) {
	if job == nil {
		return translation.ProviderDocumentHandle{}, false, fmt.Errorf("resumable translation requires a persisted job")
	}
	if job.ProviderDocumentID == nil && job.ProviderDocumentKey == nil && job.ProviderDocumentSubmittedAt == nil {
		return translation.ProviderDocumentHandle{}, false, nil
	}
	if job.ProviderDocumentID == nil || job.ProviderDocumentKey == nil || job.ProviderDocumentSubmittedAt == nil ||
		job.ProviderDocumentSubmittedAt.IsZero() || job.Provider == nil || job.Model == nil {
		return translation.ProviderDocumentHandle{}, false, errTranslationProviderDocumentHandleMismatch
	}
	if *job.Provider != generator.ProviderName() || *job.Model != generator.ModelName() {
		return translation.ProviderDocumentHandle{}, false, errTranslationProviderDocumentHandleMismatch
	}
	handle, err := translation.NewProviderDocumentHandle(*job.ProviderDocumentID, *job.ProviderDocumentKey)
	if err != nil {
		return translation.ProviderDocumentHandle{}, false, errTranslationProviderDocumentHandleMismatch
	}
	return handle, true, nil
}

func waitForTranslationProviderDocumentPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func translationProviderDocumentStringPointer(value string) *string { return &value }

func (m *TranslationJobManager) generateValidatedTranslationRequest(
	ctx context.Context,
	request translation.ProviderRequest,
	generator translation.Generator,
) (*translation.ProviderResponse, error) {
	return traceTranslationProviderOperation(
		ctx,
		"generate",
		generator.ProviderName(),
		func(callCtx context.Context) (_ *translation.ProviderResponse, retErr error) {
			session, err := generator.StartSession(callCtx, request)
			if err != nil {
				return nil, err
			}
			defer func() {
				if closeErr := session.Close(callCtx); closeErr != nil && retErr == nil {
					retErr = closeErr
				}
			}()

			response, err := session.Translate(callCtx, request)
			if err != nil {
				return nil, err
			}
			if response == nil {
				return nil, errTranslationProviderResponseInvalid
			}

			hardValidation := translation.ValidateResponse(request.Document, *response)
			if !hardValidation.Passed {
				return nil, errTranslationProviderResponseInvalid
			}
			normalized := normalizeTranslationProviderResponse(request, *response)
			return &normalized, nil
		},
	)
}
