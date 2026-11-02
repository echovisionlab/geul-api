package legal

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// legalEntityName names the owner in shared content document messages.
const legalEntityName = "legal"

type legalTargetLocaleState struct {
	Root              Root
	Snapshot          contentblock.Snapshot
	SourceLocale      string
	SourceMetadata    Translation
	TargetMetadata    *Translation
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
	Kind                     *policyKind
	EntityID                 string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	Batch                    contentblock.Batch
	Policy                   targetlocale.Policy
	SetTitle                 bool
	Title                    *string
	Now                      time.Time
	Fence                    ContentFence
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
	Kind                     *policyKind
	EntityID                 string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ContributorMemberIDs     []uuid.UUID
	Fence                    ContentFence
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
	store Store,
	kind *policyKind,
	entityID string,
	locale string,
	lockTarget bool,
) (legalTargetLocaleState, error) {
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, store, kind, entityID, lockTarget)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, *root.ContentDocumentID, root.SourceLocale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	if snapshot.SourceLocale != root.SourceLocale {
		return legalTargetLocaleState{}, domainerr.Precondition(kind.entity() + " source locale does not match its content document")
	}
	if snapshot.Document.Profile != legalContentDocumentProfile {
		return legalTargetLocaleState{}, domainerr.Precondition(kind.entity() + " target locale requires the policy content profile")
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
		SourceMetadata: Translation{
			Locale: root.SourceLocale,
			Title:  stringPointer(root.Title),
		},
		LocalizedDocument: document,
	}
	if locale == root.SourceLocale {
		return result, nil
	}
	target, found, err := store.Translation(ctx, kind.kind, entityID, locale, lockTarget)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	if !found {
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return legalTargetLocaleState{}, domainerr.Precondition(kind.entity() + " target locale Blocks exist without owning metadata")
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
	metadata Translation,
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
	store Store,
	input legalTargetLocaleMutationInput,
) (legalTargetLocaleMutationResult, error) {
	state, locale, err := lockLegalTargetLocale(ctx, store, input.target())
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
	fence, err := lockedTargetFence(ctx, store, state, input.Fence, "legal source locale changed; reload before saving")
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	write := legalTargetMetadataWrite{
		input: input, state: state, locale: locale, now: targetlocale.WriteTime(input.Now),
	}
	storeResult, err := store.ApplyTargetLocaleContentBatch(ctx, batch, locale, fence, write.apply)
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
	kind                     *policyKind
	entityID                 string
	locale                   string
	expectedDocumentRevision uuid.UUID
	fence                    ContentFence
}

func (input legalTargetLocaleMutationInput) target() legalTargetLocale {
	return legalTargetLocale{
		kind: input.Kind, entityID: input.EntityID, locale: input.Locale,
		expectedDocumentRevision: input.ExpectedDocumentRevision, fence: input.Fence,
	}
}

func (input legalTargetLocaleDeleteInput) target() legalTargetLocale {
	return legalTargetLocale{
		kind: input.Kind, entityID: input.EntityID, locale: input.Locale,
		expectedDocumentRevision: input.ExpectedDocumentRevision, fence: input.Fence,
	}
}

// lockLegalTargetLocale validates a target write and loads its locked state.
func lockLegalTargetLocale(
	ctx context.Context,
	store Store,
	target legalTargetLocale,
) (legalTargetLocaleState, string, error) {
	if target.expectedDocumentRevision == uuid.Nil || target.fence == nil {
		return legalTargetLocaleState{}, "", domainerr.InvalidField("target", "legal target identity, revision, and fence are required")
	}
	if err := validateAIDocumentIdentity(target.kind.entity(), target.entityID, target.locale); err != nil {
		return legalTargetLocaleState{}, "", err
	}
	locale, err := canonicalLegalLocale(target.locale)
	if err != nil {
		return legalTargetLocaleState{}, "", err
	}
	state, err := loadLegalTargetLocaleState(ctx, store, target.kind, target.entityID, locale, true)
	return state, locale, err
}

// lockedTargetFence runs the write's domain fence once and returns the
// fence of the locked document; a source locale that moved answers
// sourceChanged.
func lockedTargetFence(
	ctx context.Context,
	store Store,
	state legalTargetLocaleState,
	fence ContentFence,
	sourceChanged string,
) (ContentFence, error) {
	documentID := *state.Root.ContentDocumentID
	domain, err := fence(ctx, store, documentID)
	if err != nil {
		return nil, err
	}
	if domain.SourceLocale != state.SourceLocale {
		return nil, domainerr.Precondition(sourceChanged)
	}
	return lockedContentFence(documentID, domain), nil
}

// lockedContentFence admits only documentID and answers with the domain
// context the caller already checked under its lock
// (contentblock.LockedFence).
func lockedContentFence(documentID uuid.UUID, domain contentblock.DomainContext) ContentFence {
	return func(_ context.Context, _ Store, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition(legalEntityName + " content document changed; reload before saving")
		}
		return domain, nil
	}
}

// legalTargetMetadataWrite creates or updates the target locale's row with
// the content batch; final is the row the write left.
type legalTargetMetadataWrite struct {
	input  legalTargetLocaleMutationInput
	state  legalTargetLocaleState
	locale string
	now    time.Time
	final  Translation
}

func (w *legalTargetMetadataWrite) apply(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error) {
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
		w.final = Translation{Locale: w.locale, Title: title, UpdatedAt: w.now}
		inserted, err := store.InsertTranslation(ctx, w.input.Kind.kind, w.input.EntityID, w.locale, title, w.now)
		if err != nil {
			return contentblock.MetadataEffect{}, err
		}
		if inserted != 1 {
			return contentblock.MetadataEffect{}, errors.New("legal target locale could not be created")
		}
		return changed, nil
	}
	if !contentChanged && !metadataChanged {
		w.final = *current
		return contentblock.MetadataEffect{}, nil
	}
	updatedAt := translation.NextTargetUpdatedAt(w.now, current.UpdatedAt)
	updated, err := store.UpdateTranslation(ctx, w.input.Kind.kind, w.input.EntityID, w.locale, title, updatedAt)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if updated != 1 {
		return contentblock.MetadataEffect{}, errors.New("legal target locale disappeared while saving")
	}
	w.final = Translation{Locale: w.locale, Title: title, UpdatedAt: updatedAt}
	return changed, nil
}

func deleteLegalTargetLocale(
	ctx context.Context,
	store Store,
	input legalTargetLocaleDeleteInput,
) (contentblock.Result, error) {
	state, locale, err := lockLegalTargetLocale(ctx, store, input.target())
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
	fence, err := lockedTargetFence(ctx, store, state, input.Fence, "legal source locale changed; reload before deleting target")
	if err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: *state.Root.ContentDocumentID, ExpectedRevision: input.ExpectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), input.ContributorMemberIDs...),
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	return store.ApplyTargetLocaleContentBatch(ctx, batch, locale, fence,
		func(ctx context.Context, store Store, _ bool) (contentblock.MetadataEffect, error) {
			deleted, err := store.DeleteTranslation(ctx, input.Kind.kind, input.EntityID, locale)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if deleted != 1 {
				return contentblock.MetadataEffect{}, errors.New("legal target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
}
