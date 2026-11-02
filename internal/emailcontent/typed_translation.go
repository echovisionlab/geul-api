package emailcontent

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// LoadTranslationSourceDocument returns the owner's typed source document
// and subject for the shared translation orchestrator, read through store.
// sourceSubject reads the owner's source subject.
func (o Owner[S]) LoadTranslationSourceDocument(
	ctx context.Context,
	store S,
	id string,
	sourceSubject func(ctx context.Context, store S, id, sourceLocale string) (string, error),
) (*translation.SourceDocument, error) {
	domain, err := o.Rows.SourceContext(ctx, store, id, false)
	if err != nil {
		return nil, err
	}
	subject, err := sourceSubject(ctx, store, id, domain.SourceLocale)
	if err != nil {
		return nil, err
	}
	documentID, err := o.Rows.DocumentID(ctx, store, id)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return nil, o.NormalizeContentBlockError(err)
	}
	if snapshot.Document.Profile != Profile {
		return nil, errs.FailedPrecondition(o.Name + " translation source requires the Email content profile")
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, domain.SourceLocale)
	if err != nil {
		return nil, o.NormalizeContentBlockError(err)
	}
	return &translation.SourceDocument{
		Title: subject, ContentDocumentRevision: snapshot.Document.Revision.String(),
		ContentBlockDocument: document,
	}, nil
}

// TranslationCandidate is one accepted TranslationJob's generated target.
type TranslationCandidate struct {
	Job       *TranslationJob
	Candidate *translation.Candidate
	Input     translation.EntryWrite
	// Fence is the owner's job-apply fence: an accepted job finishes while
	// the owner still exists, whatever its lifecycle now.
	Fence Fence
}

// ApplyTranslationCandidate replaces one generated target against the
// current document revision; a provider result whose target is the source
// locale rewrites the source and its subject. Request-time revisions are
// source artifacts, not target CAS tokens, so intervening target edits do not
// cancel an accepted replacement. store and auditWriter are bound to the
// translation delivery's transaction.
func (o Owner[S]) ApplyTranslationCandidate(
	ctx context.Context,
	store S,
	apply TranslationCandidate,
	auditWriter domainaudit.Appender,
) error {
	job, candidate := apply.Job, apply.Candidate
	if job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed " + o.Name + " translation candidate and content Block store are required")
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != job.TargetLocale {
		return errs.FailedPrecondition("translated " + o.Name + " Block overlay locale does not match the target locale")
	}
	documentID, err := o.Rows.DocumentID(ctx, store, job.EntityID)
	if err != nil {
		return err
	}
	domain, err := apply.Fence(ctx, documentID)
	if err != nil {
		return err
	}
	currentSnapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	if candidate.Title == nil || strings.TrimSpace(*candidate.Title) == "" {
		return errs.FailedPrecondition(o.TranslatedSubjectRequired)
	}
	batch, err := translation.BuildProviderTargetRichTextBatch(
		currentSnapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL, job.TargetLocale, candidate,
	)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	write := typedTranslationWrite[S]{owner: o, store: store, apply: apply, documentID: documentID, sourceLocale: domain.SourceLocale, batch: batch, audit: auditWriter}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		return write.source(ctx)
	}
	return write.target(ctx, currentSnapshot.Document.Revision)
}

// typedTranslationWrite is one validated translation batch.
type typedTranslationWrite[S Store] struct {
	owner        Owner[S]
	store        S
	apply        TranslationCandidate
	documentID   uuid.UUID
	sourceLocale string
	batch        contentblock.Batch
	audit        domainaudit.Appender
}

// source writes a provider result whose target is the source locale: the
// source Blocks and subject change as one source revision.
func (w typedTranslationWrite[S]) source(ctx context.Context) error {
	o, job, now := w.owner, w.apply.Job, w.apply.Input.Now
	memberID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if parseErr != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errs.WithCause(errs.InternalMsg(o.Name+" translation audit requires canonical requester Member"), parseErr)
	}
	batch := w.batch
	batch.ContributorMemberIDs = []uuid.UUID{memberID}
	result, err := w.store.ApplyContentBatchWithMetadata(ctx, batch, w.apply.Fence,
		func(ctx context.Context) (contentblock.MetadataEffect, error) {
			return o.ApplySubject(ctx, w.store, AIDocumentMutation{
				Reference: job.EntityID, Locale: w.sourceLocale, ExpectedSource: w.sourceLocale,
				ExpectedPresence: true, SetSubject: true, Subject: strings.TrimSpace(*w.apply.Candidate.Title),
			}, now)
		},
	)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	if !result.Changed {
		return nil
	}
	if !result.TranslationSourceChanged {
		return errs.InternalMsg("provider source " + o.Name + " translation did not advance source state")
	}
	if err := o.refreshSource(ctx, w.store, job.EntityID, w.documentID, w.sourceLocale, true, now); err != nil {
		return err
	}
	return o.appendLocaleContentAudit(
		ctx, w.audit, memberID.String(), job.EntityID, w.sourceLocale,
		LocaleContentOperation(true, false, false, true),
	)
}

// target creates or overwrites the target locale and its subject, then
// returns the target's materialized content on the candidate.
func (w typedTranslationWrite[S]) target(ctx context.Context, currentRevision uuid.UUID) error {
	o, job, candidate := w.owner, w.apply.Job, w.apply.Candidate
	output, err := o.ApplyTargetLocaleMutation(ctx, w.store, TargetLocaleMutation{
		ID: job.EntityID, DocumentID: w.documentID, Locale: job.TargetLocale,
		Batch: w.batch, ExpectedDocumentRevision: currentRevision,
		Policy:     targetlocale.Policy{AllowCreate: true, AllowLocaleDeletes: true, RevisionMode: targetlocale.RevisionCurrent},
		SetSubject: true, Subject: candidate.Title, Now: w.apply.Input.Now, Fence: w.apply.Fence,
	})
	if err != nil {
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &targetConflict) {
			return err
		}
		return o.NormalizeContentBlockError(err)
	}
	if output.Result.Changed {
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errs.InternalMsg(o.Name + " translation audit requires requester Member")
		}
		if err := o.appendLocaleContentAudit(
			ctx, w.audit, strings.TrimSpace(job.RequestedByMemberID), job.EntityID, job.TargetLocale,
			LocaleContentOperation(false, output.LocaleCreated, false, !output.LocaleCreated),
		); err != nil {
			return err
		}
	}
	resultSnapshot, err := w.store.LockedContentSnapshot(ctx, w.documentID, w.sourceLocale)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(resultSnapshot, job.TargetLocale)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	candidate.ContentHTML = &materialized.HTML
	candidate.ContentText = &materialized.Text
	return nil
}
