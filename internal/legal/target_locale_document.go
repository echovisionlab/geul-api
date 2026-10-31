package legal

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// legalEntityName names the owner in shared content document messages.
const legalEntityName = "legal"

type legalTargetLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type legalTargetLocaleState struct {
	Root              legalContentDocumentRoot
	Snapshot          contentblock.Snapshot
	SourceLocale      string
	SourceMetadata    legalTargetLocaleMetadataRow
	TargetMetadata    *legalTargetLocaleMetadataRow
	TargetRevision    string
	LocalizedDocument *contentv1.LocalizedRichTextDocument
}

func (state legalTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: legalEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

type legalTargetLocaleMutationInput struct {
	EntityType               string
	EntityID                 string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	Batch                    contentblock.Batch
	Policy                   targetlocale.Policy
	SetTitle                 bool
	Title                    *string
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type legalTargetLocaleMutationResult struct {
	DocumentRevision string
	TargetRevision   string
	Changed          bool
	LocaleCreated    bool
	ContentChanged   bool
	MetadataChanged  bool
}

type legalTargetLocaleDeleteInput struct {
	EntityType               string
	EntityID                 string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ContributorMemberIDs     []uuid.UUID
	Fence                    contentblock.DomainFence
}

func canonicalLegalLocale(value string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(value)
	if normalized == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func loadLegalTargetLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	lockTarget bool,
) (legalTargetLocaleState, error) {
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, lockTarget)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, *root.ContentDocumentID, root.SourceLocale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	if snapshot.SourceLocale != root.SourceLocale {
		return legalTargetLocaleState{}, domainerr.Precondition(entityType + " source locale does not match its content document")
	}
	if snapshot.Document.Profile != legalContentDocumentProfile {
		return legalTargetLocaleState{}, domainerr.Precondition(entityType + " target locale requires the policy content profile")
	}
	// Collaboration consumers need the complete current block graph with
	// source fallback for leaves that are absent in this target locale. The
	// snapshot remains available separately for sparse presence and exact
	// target writes; never persist this materialized view as target state.
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	result := legalTargetLocaleState{
		Root:         root,
		Snapshot:     snapshot,
		SourceLocale: root.SourceLocale,
		SourceMetadata: legalTargetLocaleMetadataRow{
			Locale: root.SourceLocale,
			Title:  stringPointer(root.Title),
		},
		LocalizedDocument: document,
	}
	if locale == root.SourceLocale {
		return result, nil
	}
	query := tx.WithContext(ctx).Table(entityType+"_translation").
		Select("locale", "title", "updated_at").
		Where("entity_id = ? AND locale = ?", entityID, locale)
	if lockTarget {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var target legalTargetLocaleMetadataRow
	if err := query.Take(&target).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return legalTargetLocaleState{}, err
		}
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return legalTargetLocaleState{}, domainerr.Precondition(entityType + " target locale Blocks exist without owning metadata")
		}
		return result, nil
	}
	result.TargetMetadata = &target
	result.TargetRevision, err = deriveLegalTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	return result, nil
}

func deriveLegalTargetRevision(
	documentRevision string,
	metadata legalTargetLocaleMetadataRow,
) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
	}
	return revision, nil
}

func applyLegalTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input legalTargetLocaleMutationInput,
) (legalTargetLocaleMutationResult, error) {
	state, locale, err := lockLegalTargetLocale(ctx, tx, store, input.target())
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	if locale == state.SourceLocale {
		return legalTargetLocaleMutationResult{}, domainerr.InvalidField("locale", "legal target locale must differ from the source locale")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return legalTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	batch, err := input.Policy.Admit(state.locked(), locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	fence, err := lockedTargetFence(ctx, tx, state, input.Fence, "legal source locale changed; reload before saving")
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	write := legalTargetMetadataWrite{
		input: input, state: state, locale: locale, now: targetlocale.WriteTime(input.Now),
	}
	storeResult, err := store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, locale, fence, write.apply)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	result := legalTargetLocaleMutationResult{
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		LocaleCreated:    state.TargetMetadata == nil,
		Changed:          storeResult.Changed,
		ContentChanged:   storeResult.ContentChanged,
		MetadataChanged:  storeResult.MetadataChanged,
	}
	result.TargetRevision, err = deriveLegalTargetRevision(result.DocumentRevision, write.final)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	return result, nil
}

// legalTargetLocale names one target locale write.
type legalTargetLocale struct {
	entityType               string
	entityID                 string
	locale                   string
	expectedDocumentRevision uuid.UUID
	fence                    contentblock.DomainFence
}

func (input legalTargetLocaleMutationInput) target() legalTargetLocale {
	return legalTargetLocale{
		entityType: input.EntityType, entityID: input.EntityID, locale: input.Locale,
		expectedDocumentRevision: input.ExpectedDocumentRevision, fence: input.Fence,
	}
}

func (input legalTargetLocaleDeleteInput) target() legalTargetLocale {
	return legalTargetLocale{
		entityType: input.EntityType, entityID: input.EntityID, locale: input.Locale,
		expectedDocumentRevision: input.ExpectedDocumentRevision, fence: input.Fence,
	}
}

// lockLegalTargetLocale validates a target write and loads its locked state.
func lockLegalTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	target legalTargetLocale,
) (legalTargetLocaleState, string, error) {
	if target.expectedDocumentRevision == uuid.Nil || target.fence == nil {
		return legalTargetLocaleState{}, "", domainerr.InvalidField("target", "legal target identity, revision, and fence are required")
	}
	if err := validateAIDocumentIdentity(target.entityType, target.entityID, target.locale); err != nil {
		return legalTargetLocaleState{}, "", err
	}
	locale, err := canonicalLegalLocale(target.locale)
	if err != nil {
		return legalTargetLocaleState{}, "", err
	}
	state, err := loadLegalTargetLocaleState(ctx, tx, store, target.entityType, target.entityID, locale, true)
	return state, locale, err
}

// lockedTargetFence runs the write's domain fence once and returns the
// fence of the locked document; a source locale that moved answers
// sourceChanged.
func lockedTargetFence(
	ctx context.Context,
	tx *gorm.DB,
	state legalTargetLocaleState,
	fence contentblock.DomainFence,
	sourceChanged string,
) (contentblock.DomainFence, error) {
	documentID := *state.Root.ContentDocumentID
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return nil, err
	}
	if domain.SourceLocale != state.SourceLocale {
		return nil, domainerr.Precondition(sourceChanged)
	}
	return contentblock.LockedFence(legalEntityName, documentID, domain), nil
}

// legalTargetMetadataWrite creates or updates the target locale's row with
// the content batch; final is the row the write left.
type legalTargetMetadataWrite struct {
	input  legalTargetLocaleMutationInput
	state  legalTargetLocaleState
	locale string
	now    time.Time
	final  legalTargetLocaleMetadataRow
}

func (w *legalTargetMetadataWrite) apply(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
	current := w.state.TargetMetadata
	currentTitle := w.state.SourceMetadata.Title
	if current != nil {
		currentTitle = current.Title
	}
	title := currentTitle
	metadataChanged := false
	if w.input.SetTitle {
		title = w.input.Title
		metadataChanged = !sameNullableString(currentTitle, title)
	}
	changed := contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.locale}}
	if current == nil {
		w.final = legalTargetLocaleMetadataRow{Locale: w.locale, Title: title, UpdatedAt: w.now}
		insert := tx.WithContext(ctx).Exec(
			"INSERT INTO "+w.input.EntityType+"_translation (entity_id, locale, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
			w.input.EntityID, w.locale, title, w.now, w.now,
		)
		if insert.Error != nil {
			return contentblock.MetadataEffect{}, insert.Error
		}
		if insert.RowsAffected != 1 {
			return contentblock.MetadataEffect{}, errors.New("legal target locale could not be created")
		}
		return changed, nil
	}
	if !contentChanged && !metadataChanged {
		w.final = *current
		return contentblock.MetadataEffect{}, nil
	}
	updatedAt := translation.NextTargetUpdatedAt(w.now, current.UpdatedAt)
	updated := tx.WithContext(ctx).Table(w.input.EntityType+"_translation").
		Where("entity_id = ? AND locale = ?", w.input.EntityID, w.locale).
		Updates(structured.Fields{"title": title, "updated_at": updatedAt})
	if updated.Error != nil {
		return contentblock.MetadataEffect{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return contentblock.MetadataEffect{}, errors.New("legal target locale disappeared while saving")
	}
	w.final = legalTargetLocaleMetadataRow{Locale: w.locale, Title: title, UpdatedAt: updatedAt}
	return changed, nil
}

func deleteLegalTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input legalTargetLocaleDeleteInput,
) (contentblock.Result, error) {
	state, locale, err := lockLegalTargetLocale(ctx, tx, store, input.target())
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, domainerr.InvalidField("locale", "source locale is not a translation resource")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked().CheckRevision(input.ExpectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil {
		return contentblock.Result{DocumentRevision: state.Snapshot.Document.Revision}, nil
	}
	fence, err := lockedTargetFence(ctx, tx, state, input.Fence, "legal source locale changed; reload before deleting target")
	if err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: *state.Root.ContentDocumentID, ExpectedRevision: input.ExpectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), input.ContributorMemberIDs...),
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	return store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, locale, fence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Table(input.EntityType+"_translation").
				Where("entity_id = ? AND locale = ?", input.EntityID, locale).Delete(nil)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, deleted.Error
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errors.New("legal target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
}
