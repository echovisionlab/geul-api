package form

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
)

// TranslationEntrySelectSQL returns the Form-owned management projection.
func TranslationEntrySelectSQL() string {
	return `SELECT locale, title, NULL::text AS summary,
		NULL::text AS content_html, content_text, content_json,
		updated_at, og_asset_id FROM form_translation`
}

// ApplyProviderTranslationCandidateWithDB applies the request-time provider
// unit set to the current root-owned Form graph. A source-locale pointer switch
// does not revive the old source row: surviving stable handles are projected
// onto the current source topology. If the requested target has become the
// current source, the same accepted replacement is promoted to a proper source
// mutation and advances the shared Content Document revision.
func ApplyProviderTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if tx == nil || store == nil || job == nil || candidate == nil || auditWriter == nil {
		return errors.New("form provider translation dependencies are required")
	}
	if job.EntityType != "form" || !IsValidUUID(job.EntityID) ||
		strings.TrimSpace(job.TargetLocale) == "" ||
		strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errs.InvalidArgument("translation_job", "Form provider translation identity is invalid")
	}
	if input.Now.IsZero() {
		return errs.InvalidArgument("now", "Form provider translation time is required")
	}
	patch, ok := candidate.ProviderPatch()
	if !ok {
		return errs.InvalidArgument("candidate", "Form provider unit patch is required")
	}

	root, err := loadFormAIDocumentRoot(ctx, tx, job.EntityID, "UPDATE")
	if err != nil {
		return err
	}
	source, sourceExists, err := loadFormAIDocumentLocale(
		ctx, tx, job.EntityID, root.SourceLocale, true,
	)
	if err != nil {
		return err
	}
	if !sourceExists {
		return errs.FailedPrecondition("Form source locale document is missing")
	}
	if err := validateCanonicalFormSchema(source.Schema); err != nil {
		return errs.FailedPrecondition("Form source locale document is invalid")
	}

	sourceTitle := ""
	if source.Title != nil {
		sourceTitle = *source.Title
	}
	replacement, err := ApplyTranslationCandidate(
		&translation.SourceDocument{Title: sourceTitle, ContentJSON: source.Schema},
		patch.Results,
	)
	if err != nil {
		return errs.InvalidArgument("candidate", err.Error())
	}
	// Title is an optional stable scalar handle. Explicit empty still means the
	// handle exists and accepts the request-time result; only SQL NULL removes
	// it from the current source graph.
	if source.Title == nil {
		replacement.Title = nil
	}
	canonicalSchema, _, err := NormalizeLocalizedFormSchemaOverlay(
		source.Schema, replacement.ContentJSON,
	)
	if err != nil {
		return errs.InvalidArgument("candidate", err.Error())
	}
	if err := validateFormAIDocumentTargetSchema(source.Schema, canonicalSchema); err != nil {
		return errs.InvalidArgument("candidate", err.Error())
	}

	current, currentExists := source, true
	if job.TargetLocale != root.SourceLocale {
		current, currentExists, err = loadFormAIDocumentLocale(
			ctx, tx, job.EntityID, job.TargetLocale, true,
		)
		if err != nil {
			return err
		}
	}
	mutation := AIDocumentMutation{
		FormID:                   job.EntityID,
		Locale:                   job.TargetLocale,
		ExpectedDocumentRevision: root.DocumentRevision,
		ExpectedSource:           root.SourceLocale,
		ExpectedPresence:         currentExists,
		SetTitle:                 true,
		Title:                    replacement.Title,
		SetSchema:                true,
		Schema:                   canonicalSchema,
		ContributorMemberID:      strings.TrimSpace(job.RequestedByMemberID),
	}
	service := &InternalFormService{contentBlocks: store, auditWriter: auditWriter}
	changed, err := service.persistAIDocumentMutation(
		ctx, tx, mutation, root, source, current, currentExists,
	)
	if err != nil || !changed {
		return err
	}
	return service.appendAIDocumentAudit(ctx, tx, mutation)
}

func formJSONValueOrNil(value []byte) structured.Value {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}
