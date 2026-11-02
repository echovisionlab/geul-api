package emailcontent

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// ExactLocaleState is an owner's document with its source row and the
// requested locale's row, without source fallback. TargetMetadata is the
// source row for the source locale and nil for a missing target.
type ExactLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata LocaleMetadata
	TargetMetadata *LocaleMetadata
	TargetRevision string
}

func (state ExactLocaleState) locked(name string) targetlocale.State {
	return targetlocale.State{
		Entity: name, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

// materializeLocale returns the state's document in locale with source
// fallback for absent target values.
func (o Owner[S]) materializeLocale(state ExactLocaleState, locale string) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, o.NormalizeContentBlockError(err)
	}
	return document, nil
}

// LoadLocaleMetadata reads the owner's row of locale, reporting whether it
// exists.
func (o Owner[S]) LoadLocaleMetadata(ctx context.Context, store S, id, locale string, forUpdate bool) (LocaleMetadata, bool, error) {
	lock := Unlocked
	if forUpdate {
		lock = UpdateLock
	}
	row, exists, err := store.LocaleRow(ctx, id, locale, lock)
	if err != nil {
		return LocaleMetadata{}, false, errs.Internal(err)
	}
	return row, exists, nil
}

// LoadExactLocaleState reads the owner's document and the rows of its source
// locale and of locale, locking them when forUpdate is set.
func (o Owner[S]) LoadExactLocaleState(
	ctx context.Context,
	store S,
	id string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (ExactLocaleState, error) {
	locale, err := NormalizeLocale(locale)
	if err != nil {
		return ExactLocaleState{}, err
	}
	domain, err := o.Rows.SourceContext(ctx, store, id, forUpdate)
	if err != nil {
		return ExactLocaleState{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return ExactLocaleState{}, o.NormalizeContentBlockError(err)
	}
	if snapshot.Document.Profile != Profile {
		return ExactLocaleState{}, errs.FailedPrecondition(o.Name + " content document profile changed")
	}
	source, exists, err := o.LoadLocaleMetadata(ctx, store, id, domain.SourceLocale, forUpdate)
	if err != nil {
		return ExactLocaleState{}, err
	}
	if !exists || source.Subject == nil || strings.TrimSpace(*source.Subject) == "" {
		return ExactLocaleState{}, errs.FailedPrecondition(o.Name + " source locale subject is not initialized")
	}
	state := ExactLocaleState{Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: source}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &state.SourceMetadata
		return state, nil
	}
	target, exists, err := o.LoadLocaleMetadata(ctx, store, id, locale, forUpdate)
	if err != nil {
		return ExactLocaleState{}, err
	}
	if !exists {
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return ExactLocaleState{}, errs.FailedPrecondition(o.Name + " target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return ExactLocaleState{}, err
	}
	return state, nil
}

func deriveTargetRevision(documentRevision string, metadata LocaleMetadata) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

// TargetLocaleMutation writes one target locale's Blocks and subject.
type TargetLocaleMutation struct {
	ID                       string
	DocumentID               uuid.UUID
	Locale                   string
	Batch                    contentblock.Batch
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	// ExpectedLocaleExists, when set, is the target presence the caller
	// observed.
	ExpectedLocaleExists *bool
	Policy               targetlocale.Policy
	SetSubject           bool
	Subject              *string
	Now                  time.Time
	Fence                Fence
}

// TargetLocaleMutationResult is the store result of a target write with
// the target revision it left.
type TargetLocaleMutationResult struct {
	Result         contentblock.Result
	TargetRevision string
	LocaleCreated  bool
}

// ApplyTargetLocaleMutation writes the target locale's Blocks and subject
// against the expected document revision, creating the locale row when the
// policy allows, and projects the changed locale's materialized content.
func (o Owner[S]) ApplyTargetLocaleMutation(
	ctx context.Context,
	store S,
	input TargetLocaleMutation,
) (TargetLocaleMutationResult, error) {
	state, err := o.lockTargetLocale(ctx, store, input)
	if err != nil {
		return TargetLocaleMutationResult{}, err
	}
	batch, err := input.Policy.Admit(state.locked(o.Name), input.Locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return TargetLocaleMutationResult{}, err
	}
	write := &targetSubjectWrite[S]{owner: o, store: store, input: input, state: state, now: targetlocale.WriteTime(input.Now)}
	result, err := store.ApplyTargetLocaleContentBatch(ctx, batch, input.Locale, input.Fence, write.apply)
	if err != nil {
		return TargetLocaleMutationResult{}, o.NormalizeContentBlockError(err)
	}
	output := TargetLocaleMutationResult{Result: result, LocaleCreated: state.TargetMetadata == nil}
	if output.TargetRevision, err = deriveTargetRevision(result.DocumentRevision.String(), write.final); err != nil {
		return TargetLocaleMutationResult{}, err
	}
	if !result.Changed {
		return output, nil
	}
	if err := store.TouchOwner(ctx, input.ID, write.now); err != nil {
		return TargetLocaleMutationResult{}, errs.Internal(err)
	}
	snapshot, err := store.LockedContentSnapshot(ctx, input.DocumentID, state.SourceLocale)
	if err != nil {
		return TargetLocaleMutationResult{}, o.NormalizeContentBlockError(err)
	}
	return output, o.projectTarget(ctx, store, input.ID, snapshot, input.Locale)
}

// lockTargetLocale fences the write and loads the locked target state the
// caller expects.
func (o Owner[S]) lockTargetLocale(
	ctx context.Context,
	store S,
	input TargetLocaleMutation,
) (ExactLocaleState, error) {
	domain, err := input.Fence(ctx, input.DocumentID)
	if err != nil {
		return ExactLocaleState{}, err
	}
	state, err := o.LoadExactLocaleState(ctx, store, input.ID, input.DocumentID, input.Locale, true)
	if err != nil {
		return ExactLocaleState{}, err
	}
	if input.Locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale {
		return ExactLocaleState{}, errs.FailedPrecondition(o.Name + " target locale source changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return ExactLocaleState{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if input.ExpectedLocaleExists != nil && *input.ExpectedLocaleExists != (state.TargetMetadata != nil) {
		return ExactLocaleState{}, errs.FailedPrecondition(o.Name + " target locale presence changed; reload before saving")
	}
	return state, nil
}

// targetSubjectWrite creates or updates the target locale row with the
// content batch; final is the row the write left.
type targetSubjectWrite[S Store] struct {
	owner Owner[S]
	store S
	input TargetLocaleMutation
	state ExactLocaleState
	now   time.Time
	final LocaleMetadata
}

func (w *targetSubjectWrite[S]) apply(ctx context.Context, contentChanged bool) (contentblock.MetadataEffect, error) {
	changed := contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.input.Locale}}
	if w.state.TargetMetadata == nil {
		subject := w.state.SourceMetadata.Subject
		if w.input.SetSubject {
			subject = w.input.Subject
		}
		created, err := w.store.InsertLocale(ctx, w.input.ID, w.input.Locale, subject, w.now)
		if err != nil {
			return contentblock.MetadataEffect{}, errs.Internal(err)
		}
		if !created {
			return contentblock.MetadataEffect{}, errs.InternalMsg(w.owner.Name + " target locale could not be created")
		}
		w.final = LocaleMetadata{Locale: w.input.Locale, Subject: cloneString(subject), UpdatedAt: w.now}
		return changed, nil
	}
	w.final = *w.state.TargetMetadata
	metadataChanged := w.input.SetSubject && !stringsEqual(w.final.Subject, w.input.Subject)
	if w.input.SetSubject {
		w.final.Subject = cloneString(w.input.Subject)
	}
	if !contentChanged && !metadataChanged {
		return contentblock.MetadataEffect{}, nil
	}
	w.final.UpdatedAt = translation.NextTargetUpdatedAt(w.now, w.final.UpdatedAt)
	updated, err := w.store.UpdateLocale(ctx, w.input.ID, w.input.Locale, w.final.Subject, w.final.UpdatedAt)
	if err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	if !updated {
		return contentblock.MetadataEffect{}, errs.InternalMsg(w.owner.Name + " target locale disappeared while saving")
	}
	return changed, nil
}

// DeleteTargetLocale deletes an existing target locale's Blocks and row
// against the expected document and target revisions.
func (o Owner[S]) DeleteTargetLocale(
	ctx context.Context,
	store S,
	id string,
	documentID uuid.UUID,
	locale string,
	expectedDocumentRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	now time.Time,
	fence Fence,
) (contentblock.Result, error) {
	domain, err := fence(ctx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := o.LoadExactLocaleState(ctx, store, id, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale || state.TargetMetadata == nil {
		return contentblock.Result{}, errs.FailedPrecondition(o.Name + " target locale must exist before deletion")
	}
	if state.Snapshot.Document.Revision != expectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked(o.Name).CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), contributors...),
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	result, err := store.ApplyTargetLocaleContentBatch(ctx, batch, locale, fence,
		func(ctx context.Context, _ bool) (contentblock.MetadataEffect, error) {
			deleted, err := store.DeleteLocale(ctx, id, locale)
			if err != nil {
				return contentblock.MetadataEffect{}, errs.Internal(err)
			}
			if !deleted {
				return contentblock.MetadataEffect{}, errs.InternalMsg(o.Name + " target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, o.NormalizeContentBlockError(err)
	}
	if result.Changed {
		if now.IsZero() {
			now = dbtime.Now()
		}
		if err := store.TouchOwner(ctx, id, now.UTC()); err != nil {
			return contentblock.Result{}, errs.Internal(err)
		}
	}
	return result, nil
}

// projectTarget stores the target locale's materialized HTML and text.
func (o Owner[S]) projectTarget(ctx context.Context, store S, id string, snapshot contentblock.Snapshot, locale string) error {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	updated, err := store.ProjectLocale(ctx, id, locale, LocaleProjection{HTML: projection.HTML, Text: projection.Text})
	if err != nil {
		return errs.Internal(err)
	}
	if !updated {
		return errs.FailedPrecondition(o.Name + " target locale disappeared while projecting")
	}
	return nil
}
