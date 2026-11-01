package emailcontent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// AIDocumentState is an owner's coherent subject and Email profile document
// in one locale, as the compact AI document adapter reads it.
type AIDocumentState struct {
	Reference        string
	DocumentID       uuid.UUID
	DocumentRevision string
	TargetRevision   *string
	SourceLocale     string
	Locale           string
	LocaleExists     bool
	ViewerMemberID   string
	Subject          *string
	Document         *contentv1.LocalizedRichTextDocument
}

// AIDocumentMutation is an adapter-compiled email content mutation. Subject
// omission and an explicit empty target subject remain distinct.
type AIDocumentMutation struct {
	Reference                string
	Locale                   string
	ExpectedDocumentRevision string
	ExpectedTargetRevision   *string
	ExpectedSource           string
	ExpectedPresence         bool
	ContributorMember        uuid.UUID
	Batch                    *contentblock.Batch
	SetSubject               bool
	Subject                  string
	CreateTranslation        bool
	DeleteTranslation        bool
}

type AIDocumentMutationResult struct {
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
}

type AIDocumentExecutionMode uint8

const (
	AIDocumentExecutionValidate AIDocumentExecutionMode = iota
	AIDocumentExecutionApply
)

// AIDocumentMutationCompiler is invoked only with the current authorized
// state loaded under the owner's root lock.
type AIDocumentMutationCompiler func(AIDocumentState) (AIDocumentMutation, error)

type compilerError struct{ cause error }

func (e *compilerError) Error() string { return e.cause.Error() }
func (e *compilerError) Unwrap() error { return e.cause }

type AIDocumentRevisionConflictKind string

const (
	AIDocumentDocumentRevisionConflict AIDocumentRevisionConflictKind = "document_revision"
	AIDocumentTargetRevisionConflict   AIDocumentRevisionConflictKind = "target_revision"
)

type AIDocumentRevisionConflictError struct {
	Owner                   string
	Kind                    AIDocumentRevisionConflictKind
	CurrentDocumentRevision string
	CurrentTargetRevision   *string
}

func (e *AIDocumentRevisionConflictError) Error() string {
	return fmt.Sprintf("%s AI document revision conflict: current revision is %q", e.Owner, e.CurrentDocumentRevision)
}

// AIDocumentAuthority reads the owner's root for a load, or locks it for a
// mutation, rejects what the owner's lifecycle forbids, authorizes the
// request's Member to edit it and, for a mutation, locks that Member as the
// contributor. It returns the owner's content document and the Member.
type AIDocumentAuthority func(ctx context.Context, tx *gorm.DB, id string, forUpdate bool) (uuid.UUID, uuid.UUID, error)

// AIDocuments is the AI document seam of one owner. Its writes run through
// the owner's mutation pipeline.
type AIDocuments struct {
	Owner     Owner
	DB        *gorm.DB
	Store     *contentblock.Store
	Mutations *mutation.Runner
	Authorize AIDocumentAuthority
}

// Load returns the owner's state in locale for an authorized editor.
func (d AIDocuments) Load(ctx context.Context, id, locale string) (AIDocumentState, error) {
	id, err := CanonicalID(d.Owner.IDField, id)
	if err != nil {
		return AIDocumentState{}, err
	}
	locale, err = NormalizeLocale(locale)
	if err != nil {
		return AIDocumentState{}, err
	}
	var state AIDocumentState
	err = mutation.ReadLocked(ctx, d.DB, func(tx *gorm.DB) error {
		documentID, memberID, err := d.Authorize(ctx, tx, id, false)
		if err != nil {
			return err
		}
		state, err = d.loadState(ctx, tx, id, locale, documentID, memberID, false)
		return err
	})
	return state, failurecause.WithAttrs(err, slog.String("entity_type", d.Owner.Entity), slog.String("entity_id", id), slog.String("locale", locale))
}

var errRollbackAIDocumentValidation = errors.New("rollback email content AI document validation")

// ExecuteAIDocumentMutation is the owner's exact DCDP mutation boundary. It
// locks and authorizes the owner's root and lifecycle, locks the current
// source, locale and contributor facts, and only then invokes the adapter
// compiler; the write, its projections, Audit and signal share the
// transaction. Validate rolls the transaction back before Audit and signal.
func (d AIDocuments) ExecuteAIDocumentMutation(
	ctx context.Context,
	id string,
	locale string,
	mode AIDocumentExecutionMode,
	compiler AIDocumentMutationCompiler,
) (AIDocumentMutationResult, error) {
	if compiler == nil {
		return AIDocumentMutationResult{}, errs.DependencyUnavailable(d.Owner.Name + " AI document compiler")
	}
	if mode != AIDocumentExecutionValidate && mode != AIDocumentExecutionApply {
		return AIDocumentMutationResult{}, errs.InvalidArgument("mode", "is not supported")
	}
	id, err := CanonicalID(d.Owner.IDField, id)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	locale, err = NormalizeLocale(locale)
	if err != nil {
		return AIDocumentMutationResult{}, err
	}
	var documentID, memberID uuid.UUID
	var applied aiDocumentApply
	_, err = mutation.Run(ctx, d.Mutations, mutation.Spec[aiDocumentApply]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
			var err error
			documentID, memberID, err = d.Authorize(ctx, tx, id, true)
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (aiDocumentApply, error) {
			var err error
			applied, err = d.execute(ctx, tx, id, locale, documentID, memberID, compiler)
			if err == nil && mode == AIDocumentExecutionValidate {
				err = errRollbackAIDocumentValidation
			}
			return applied, err
		},
		Audit: func(applied aiDocumentApply) mutation.Audit { return applied.record },
		Outbox: func(ctx context.Context, tx *gorm.DB, applied aiDocumentApply) error {
			m, output := applied.mutation, applied.output
			return contentsignal.PublishInteractive(ctx, tx, d.Owner.ContentEntityType, m.Reference, m.Locale, output.DocumentRevision, output.TargetRevision, output.Changed, m.DeleteTranslation, m.ContributorMember.String())
		},
	})
	output, currentDocumentRevision := applied.output, applied.currentDocumentRevision
	if errors.Is(err, errRollbackAIDocumentValidation) {
		return output, nil
	}
	if err != nil {
		var compileErr *compilerError
		if errors.As(err, &compileErr) {
			err = compileErr.cause
		} else {
			err = d.Owner.mapAIDocumentError(err, currentDocumentRevision)
		}
		return AIDocumentMutationResult{}, failurecause.WithAttrs(err, slog.String("entity_type", d.Owner.Entity), slog.String("entity_id", id), slog.String("locale", locale))
	}
	return output, nil
}

// aiDocumentApply is one applied mutation, the record it names and the
// document revision the compiler saw.
type aiDocumentApply struct {
	mutation                AIDocumentMutation
	output                  AIDocumentMutationResult
	record                  mutation.Audit
	currentDocumentRevision string
}

// execute compiles and applies one mutation in tx after the owner locked and
// authorized documentID for memberID.
func (d AIDocuments) execute(
	ctx context.Context,
	tx *gorm.DB,
	id string,
	locale string,
	documentID uuid.UUID,
	memberID uuid.UUID,
	compiler AIDocumentMutationCompiler,
) (aiDocumentApply, error) {
	domain, err := d.Owner.Rows.SourceContext(ctx, tx, id, true)
	if err != nil {
		return aiDocumentApply{}, err
	}
	state, err := d.loadState(ctx, tx, id, locale, documentID, memberID, true)
	if err != nil {
		return aiDocumentApply{}, err
	}
	applied := aiDocumentApply{currentDocumentRevision: state.DocumentRevision}
	compiled, err := compiler(state)
	if err != nil {
		return applied, &compilerError{cause: err}
	}
	compiled, expected, err := d.Owner.normalizeAIDocumentMutation(compiled)
	if err != nil {
		return applied, err
	}
	if err := d.Owner.validateCompiledAIDocumentMutation(state, compiled, expected); err != nil {
		return applied, err
	}
	write := aiDocumentWrite{
		mutation: compiled, expected: expected, documentID: documentID,
		fence: d.Owner.authorizedAIDocumentFence(documentID, domain), now: dbtime.Now(),
	}
	applied.mutation = compiled
	applied.output, applied.record, err = d.apply(ctx, tx, write)
	return applied, err
}

// aiDocumentWrite is one validated mutation of the locked document.
type aiDocumentWrite struct {
	mutation   AIDocumentMutation
	expected   uuid.UUID
	documentID uuid.UUID
	fence      contentblock.DomainFence
	now        time.Time
}

func (w aiDocumentWrite) contributors() []uuid.UUID {
	return []uuid.UUID{w.mutation.ContributorMember}
}

// apply writes the mutation: a created or deleted translation, a source
// edit with its subject, or a target edit. A changed source refreshes the
// owner's projections; every change names the locale content record.
func (d AIDocuments) apply(ctx context.Context, tx *gorm.DB, w aiDocumentWrite) (AIDocumentMutationResult, mutation.Audit, error) {
	m := w.mutation
	var result contentblock.Result
	var targetRevision *string
	var err error
	switch {
	case m.DeleteTranslation:
		result, err = d.Owner.DeleteTargetLocale(
			ctx, tx, d.Store, m.Reference, w.documentID, m.Locale,
			w.expected, m.ExpectedTargetRevision, w.contributors(), w.now, w.fence,
		)
	case !m.CreateTranslation && m.Locale == m.ExpectedSource:
		result, err = d.applySource(ctx, tx, w)
	default:
		var target TargetLocaleMutationResult
		target, err = d.Owner.ApplyTargetLocaleMutation(ctx, tx, d.Store, d.targetMutation(w))
		result, targetRevision = target.Result, &target.TargetRevision
	}
	if err != nil {
		return AIDocumentMutationResult{}, mutation.Audit{}, err
	}
	record := mutation.Unchanged()
	if result.Changed {
		record = d.Owner.LocaleContentChange(
			m.ContributorMember.String(), m.Reference, m.Locale,
			LocaleContentOperation(m.Locale == m.ExpectedSource, m.CreateTranslation, m.DeleteTranslation, m.ExpectedPresence),
		)
	}
	return AIDocumentMutationResult{
		DocumentRevision: result.DocumentRevision.String(), TargetRevision: targetRevision, Changed: result.Changed,
	}, record, nil
}

// targetMutation is the target write of a created translation or a target
// edit; a created translation seeds the source Blocks.
func (d AIDocuments) targetMutation(w aiDocumentWrite) TargetLocaleMutation {
	m := w.mutation
	input := TargetLocaleMutation{
		ID: m.Reference, DocumentID: w.documentID, Locale: m.Locale,
		Batch: contentblock.Batch{
			DocumentID: w.documentID, ExpectedRevision: w.expected, ContributorMemberIDs: w.contributors(),
		},
		ExpectedDocumentRevision: w.expected, ExpectedTargetRevision: m.ExpectedTargetRevision,
		Policy: targetlocale.Policy{AllowCreate: true, SeedSource: true}, Now: w.now, Fence: w.fence,
	}
	if m.CreateTranslation {
		return input
	}
	input.Batch = w.sourceBatch()
	if m.SetSubject {
		input.SetSubject, input.Subject = true, &m.Subject
	}
	return input
}

// sourceBatch is the compiled batch bound to the locked document, revision
// and contributor.
func (w aiDocumentWrite) sourceBatch() contentblock.Batch {
	batch := *w.mutation.Batch
	batch.DocumentID = w.documentID
	batch.ExpectedRevision = w.expected
	batch.ContributorMemberIDs = w.contributors()
	return batch
}

// applySource writes the source Blocks and subject as one source revision
// and refreshes the owner's projections when they changed.
func (d AIDocuments) applySource(ctx context.Context, tx *gorm.DB, w aiDocumentWrite) (contentblock.Result, error) {
	m := w.mutation
	if m.ExpectedTargetRevision != nil {
		return contentblock.Result{}, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	result, err := d.Store.ApplyBatchWithMetadata(ctx, tx, w.sourceBatch(), w.fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return d.Owner.ApplySubject(ctx, tx, m, w.now)
		},
	)
	if err != nil || !result.Changed {
		return result, err
	}
	return result, d.Owner.refreshSource(ctx, tx, d.Store, m.Reference, w.documentID, m.ExpectedSource, result.TranslationSourceChanged, w.now)
}

// refreshSource advances the owner row and rebuilds its projections from
// the source snapshot a source write left.
func (o Owner) refreshSource(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	id string,
	documentID uuid.UUID,
	sourceLocale string,
	sourceChanged bool,
	now time.Time,
) error {
	if err := o.touch(ctx, tx, id, now); err != nil {
		return err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	return o.Rows.ProjectSource(ctx, tx, id, snapshot, sourceChanged, now)
}

func (d AIDocuments) loadState(
	ctx context.Context,
	tx *gorm.DB,
	id string,
	locale string,
	documentID uuid.UUID,
	memberID uuid.UUID,
	lockLocale bool,
) (AIDocumentState, error) {
	state, err := d.Owner.LoadExactLocaleState(ctx, tx, d.Store, id, documentID, locale, lockLocale)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(state.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, d.Owner.NormalizeContentBlockError(err)
	}
	var subject *string
	if state.TargetMetadata != nil && state.TargetMetadata.Subject.Valid {
		value := state.TargetMetadata.Subject.String
		subject = &value
	}
	var targetRevision *string
	if locale != state.SourceLocale && state.TargetMetadata != nil {
		targetRevision = &state.TargetRevision
	}
	return AIDocumentState{
		Reference: id, DocumentID: documentID,
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		TargetRevision:   targetRevision, SourceLocale: state.SourceLocale,
		Locale: locale, LocaleExists: state.TargetMetadata != nil, ViewerMemberID: memberID.String(),
		Subject: cloneString(subject), Document: document,
	}, nil
}

func (o Owner) authorizedAIDocumentFence(expectedDocumentID uuid.UUID, domain contentblock.DomainContext) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if documentID != expectedDocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition(o.Name + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

func (o Owner) validateCompiledAIDocumentMutation(state AIDocumentState, mutation AIDocumentMutation, expected uuid.UUID) error {
	if mutation.Reference != state.Reference || mutation.Locale != state.Locale ||
		mutation.ContributorMember.String() != state.ViewerMemberID {
		return errs.InvalidArgument("mutation", "compiled "+o.Name+" identity, locale, and contributor must match the locked state")
	}
	if mutation.ExpectedDocumentRevision != state.DocumentRevision || mutation.ExpectedSource != state.SourceLocale ||
		mutation.ExpectedPresence != state.LocaleExists {
		return o.conflict(AIDocumentDocumentRevisionConflict, state.DocumentRevision, state.TargetRevision)
	}
	if !stringsEqual(mutation.ExpectedTargetRevision, state.TargetRevision) {
		return o.conflict(AIDocumentTargetRevisionConflict, state.DocumentRevision, state.TargetRevision)
	}
	if mutation.Batch != nil && (mutation.Batch.DocumentID != state.DocumentID ||
		mutation.Batch.ExpectedRevision != expected || len(mutation.Batch.ContributorMemberIDs) != 1 ||
		mutation.Batch.ContributorMemberIDs[0] != mutation.ContributorMember) {
		return errs.InvalidArgument("mutation", "compiled "+o.Name+" document, revision, and attribution must match the locked state")
	}
	return nil
}

func (o Owner) conflict(kind AIDocumentRevisionConflictKind, documentRevision string, targetRevision *string) *AIDocumentRevisionConflictError {
	return &AIDocumentRevisionConflictError{
		Owner: o.Name, Kind: kind, CurrentDocumentRevision: documentRevision, CurrentTargetRevision: targetRevision,
	}
}

// normalizeAIDocumentMutation canonicalizes the compiled mutation and
// returns its expected document revision.
func (o Owner) normalizeAIDocumentMutation(input AIDocumentMutation) (AIDocumentMutation, uuid.UUID, error) {
	id, err := CanonicalID(o.IDField, input.Reference)
	if err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	input.Reference = id
	if input.Locale, err = NormalizeLocale(input.Locale); err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	if input.ExpectedSource, err = NormalizeLocale(input.ExpectedSource); err != nil {
		return AIDocumentMutation{}, uuid.Nil, err
	}
	expected, err := uuid.Parse(strings.TrimSpace(input.ExpectedDocumentRevision))
	if err != nil || expected == uuid.Nil || expected.String() != strings.TrimSpace(input.ExpectedDocumentRevision) {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	if input.ContributorMember == uuid.Nil {
		return AIDocumentMutation{}, uuid.Nil, errs.InvalidArgument("contributor_member_id", "is required")
	}
	return input, expected, o.validateAIDocumentMode(input)
}

// validateAIDocumentMode admits exactly one of a batch, a created
// translation of a missing target, and a deleted existing target.
func (o Owner) validateAIDocumentMode(input AIDocumentMutation) error {
	modes := 0
	for _, set := range []bool{input.Batch != nil, input.CreateTranslation, input.DeleteTranslation} {
		if set {
			modes++
		}
	}
	if modes != 1 {
		return errs.InvalidArgument("operations", "exactly one "+o.Name+" mutation mode is required")
	}
	if input.CreateTranslation && (input.Locale == input.ExpectedSource || input.ExpectedPresence) {
		return errs.InvalidArgument("locale", "only a missing non-source "+o.Name+" locale can be created")
	}
	if input.DeleteTranslation && (input.Locale == input.ExpectedSource || !input.ExpectedPresence) {
		return errs.InvalidArgument("locale", "only an existing non-source "+o.Name+" locale can be deleted")
	}
	return nil
}

func (o Owner) mapAIDocumentError(err error, currentDocumentRevision string) error {
	var conflict *AIDocumentRevisionConflictError
	if errors.As(err, &conflict) {
		return conflict
	}
	var stale *contentblock.StaleRevisionError
	if errors.As(err, &stale) {
		return o.conflict(AIDocumentDocumentRevisionConflict, stale.CurrentRevision.String(), nil)
	}
	var target *translation.TargetRevisionConflict
	if errors.As(err, &target) {
		var current *string
		if target.CurrentExists {
			current = &target.CurrentRevision
		}
		return o.conflict(AIDocumentTargetRevisionConflict, currentDocumentRevision, current)
	}
	return o.NormalizeContentBlockError(err)
}
