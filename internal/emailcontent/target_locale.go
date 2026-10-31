package emailcontent

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// LocaleMetadata is one locale row of an owner.
type LocaleMetadata struct {
	Locale    string         `gorm:"column:locale"`
	Subject   sql.NullString `gorm:"column:subject"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
}

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

func (state ExactLocaleState) locked(owner Owner) targetlocale.State {
	return targetlocale.State{
		Entity: owner.Name, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

// MaterializeLocale returns the state's document in locale with source
// fallback for absent target values.
func (state ExactLocaleState) MaterializeLocale(owner Owner, locale string) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, owner.NormalizeContentBlockError(err)
	}
	return document, nil
}

// LoadLocaleMetadata reads the owner's row of locale, reporting whether it
// exists.
func (o Owner) LoadLocaleMetadata(ctx context.Context, db *gorm.DB, id, locale string, forUpdate bool) (LocaleMetadata, bool, error) {
	query := db.WithContext(ctx).Table(o.translationTable()).
		Select("locale", "subject", "updated_at").
		Where("entity_id = ? AND locale = ?", id, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row LocaleMetadata
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return LocaleMetadata{}, false, nil
		}
		return LocaleMetadata{}, false, errs.Internal(err)
	}
	return row, true, nil
}

// LoadExactLocaleState reads the owner's document and the rows of its source
// locale and of locale, locking them when forUpdate is set.
func (o Owner) LoadExactLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	id string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (ExactLocaleState, error) {
	locale, err := NormalizeLocale(locale)
	if err != nil {
		return ExactLocaleState{}, err
	}
	domain, err := o.Rows.SourceContext(ctx, tx, id, forUpdate)
	if err != nil {
		return ExactLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return ExactLocaleState{}, o.NormalizeContentBlockError(err)
	}
	if snapshot.Document.Profile != Profile {
		return ExactLocaleState{}, errs.FailedPrecondition(o.Name + " content document profile changed")
	}
	source, exists, err := o.LoadLocaleMetadata(ctx, tx, id, domain.SourceLocale, forUpdate)
	if err != nil {
		return ExactLocaleState{}, err
	}
	if !exists || !source.Subject.Valid || strings.TrimSpace(source.Subject.String) == "" {
		return ExactLocaleState{}, errs.FailedPrecondition(o.Name + " source locale subject is not initialized")
	}
	state := ExactLocaleState{Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: source}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &state.SourceMetadata
		return state, nil
	}
	target, exists, err := o.LoadLocaleMetadata(ctx, tx, id, locale, forUpdate)
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
	Fence                contentblock.DomainFence
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
func (o Owner) ApplyTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input TargetLocaleMutation,
) (TargetLocaleMutationResult, error) {
	state, err := o.lockTargetLocale(ctx, tx, store, input)
	if err != nil {
		return TargetLocaleMutationResult{}, err
	}
	batch, err := input.Policy.Admit(state.locked(o), input.Locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return TargetLocaleMutationResult{}, err
	}
	write := targetSubjectWrite{owner: o, input: input, state: state, now: targetlocale.WriteTime(input.Now)}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, input.Locale, input.Fence, write.apply)
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
	if err := tx.WithContext(ctx).Table(o.Entity).Where("id = ?", input.ID).
		UpdateColumn("updated_at", write.now).Error; err != nil {
		return TargetLocaleMutationResult{}, errs.Internal(err)
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, input.DocumentID, state.SourceLocale)
	if err != nil {
		return TargetLocaleMutationResult{}, o.NormalizeContentBlockError(err)
	}
	return output, o.projectTarget(ctx, tx, input.ID, snapshot, input.Locale)
}

// lockTargetLocale fences the write and loads the locked target state the
// caller expects.
func (o Owner) lockTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input TargetLocaleMutation,
) (ExactLocaleState, error) {
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return ExactLocaleState{}, err
	}
	state, err := o.LoadExactLocaleState(ctx, tx, store, input.ID, input.DocumentID, input.Locale, true)
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
type targetSubjectWrite struct {
	owner Owner
	input TargetLocaleMutation
	state ExactLocaleState
	now   time.Time
	final LocaleMetadata
}

func (w *targetSubjectWrite) apply(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
	changed := contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.input.Locale}}
	if w.state.TargetMetadata == nil {
		subject := w.state.SourceMetadata.Subject
		if w.input.SetSubject {
			subject = nullableSubject(w.input.Subject)
		}
		created := tx.WithContext(ctx).Exec(
			`INSERT INTO `+w.owner.translationTable()+` (
			 entity_id, locale, subject, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?)`,
			w.input.ID, w.input.Locale, subjectValue(subject), w.now, w.now,
		)
		if created.Error != nil {
			return contentblock.MetadataEffect{}, errs.Internal(created.Error)
		}
		if created.RowsAffected != 1 {
			return contentblock.MetadataEffect{}, errs.InternalMsg(w.owner.Name + " target locale could not be created")
		}
		w.final = LocaleMetadata{Locale: w.input.Locale, Subject: subject, UpdatedAt: w.now}
		return changed, nil
	}
	w.final = *w.state.TargetMetadata
	metadataChanged := w.input.SetSubject && !subjectEquals(w.final.Subject, w.input.Subject)
	if w.input.SetSubject {
		w.final.Subject = nullableSubject(w.input.Subject)
	}
	if !contentChanged && !metadataChanged {
		return contentblock.MetadataEffect{}, nil
	}
	w.final.UpdatedAt = translation.NextTargetUpdatedAt(w.now, w.final.UpdatedAt)
	updated := tx.WithContext(ctx).Table(w.owner.translationTable()).
		Where("entity_id = ? AND locale = ?", w.input.ID, w.input.Locale).
		Updates(map[string]any{"subject": subjectValue(w.final.Subject), "updated_at": w.final.UpdatedAt})
	if updated.Error != nil {
		return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return contentblock.MetadataEffect{}, errs.InternalMsg(w.owner.Name + " target locale disappeared while saving")
	}
	return changed, nil
}

// DeleteTargetLocale deletes an existing target locale's Blocks and row
// against the expected document and target revisions.
func (o Owner) DeleteTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	id string,
	documentID uuid.UUID,
	locale string,
	expectedDocumentRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := o.LoadExactLocaleState(ctx, tx, store, id, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale || state.TargetMetadata == nil {
		return contentblock.Result{}, errs.FailedPrecondition(o.Name + " target locale must exist before deletion")
	}
	if state.Snapshot.Document.Revision != expectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked(o).CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), contributors...),
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, locale, fence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec(
				"DELETE FROM "+o.translationTable()+" WHERE entity_id = ? AND locale = ?", id, locale,
			)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
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
		if err := tx.WithContext(ctx).Table(o.Entity).Where("id = ?", id).
			UpdateColumn("updated_at", now.UTC()).Error; err != nil {
			return contentblock.Result{}, errs.Internal(err)
		}
	}
	return result, nil
}

// projectTarget stores the target locale's materialized HTML and text.
func (o Owner) projectTarget(ctx context.Context, tx *gorm.DB, id string, snapshot contentblock.Snapshot, locale string) error {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
	if err != nil {
		return o.NormalizeContentBlockError(err)
	}
	updated := tx.WithContext(ctx).Table(o.translationTable()).
		Where("entity_id = ? AND locale = ?", id, locale).
		Updates(map[string]any{"content_html": projection.HTML, "content_text": projection.Text})
	if updated.Error != nil {
		return errs.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return errs.FailedPrecondition(o.Name + " target locale disappeared while projecting")
	}
	return nil
}

func subjectValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableSubject(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func subjectEquals(current sql.NullString, next *string) bool {
	if !current.Valid || next == nil {
		return !current.Valid && next == nil
	}
	return current.String == *next
}
