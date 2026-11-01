package work

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

// workEntityName names the owner in shared content document messages.
const workEntityName = "Work"

type workTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata LocaleMetadata
	TargetMetadata *LocaleMetadata
	TargetRevision string
}

func (state workTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: workEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

type workTargetMetadataPatch struct {
	EnsureLocale  bool
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
	DeleteLocale  bool
}

// workTargetMetadataProjection is the read-time locale projection. A target
// row may intentionally omit one nullable value; that value falls back to the
// current source value for resident/public reads while exact mutation state
// continues to preserve the target row's sparse presence.
func workTargetMetadataProjection(source LocaleMetadata, target *LocaleMetadata, locale string) LocaleMetadata {
	projected := LocaleMetadata{
		Locale:  locale,
		Title:   cloneOptionalString(source.Title),
		Summary: cloneOptionalString(source.Summary),
	}
	if target == nil {
		return projected
	}
	if target.Title != nil {
		projected.Title = cloneOptionalString(target.Title)
	}
	if target.Summary != nil {
		projected.Summary = cloneOptionalString(target.Summary)
	}
	projected.UpdatedAt = target.UpdatedAt
	return projected
}

func normalizeWorkDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func workSourceRoomFence(locale string, base ContentFence) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		domain, err := base(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != locale {
			return contentblock.DomainContext{}, store.LocaleOwnershipConflict(ctx, documentID,
				"Work source locale changed since the room was loaded; reload before saving",
			)
		}
		return domain, nil
	}
}

func deriveWorkTargetRevision(documentRevision string, metadata LocaleMetadata) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
	}
	return revision, nil
}

func validateWorkSourceStorage(
	storage contentv1.ContentStorageMutationBatch,
	sourceLocale string,
) error {
	for _, group := range storage.LocaleGroups {
		if group.Locale != sourceLocale {
			return domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Work source room mutation must match the current source locale",
			)
		}
	}
	return nil
}

func loadWorkTargetLocaleState(ctx context.Context, store Store, workID string, documentID uuid.UUID, locale string, forUpdate bool) (workTargetLocaleState, error) {
	locale, err := normalizeWorkDocumentLocale(locale)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	lock := ShareLock
	if forUpdate {
		lock = UpdateLock
	}
	_, lockedDocumentID, err := loadWorkAIDocumentRoot(ctx, store, workID, lock)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	if lockedDocumentID != documentID {
		return workTargetLocaleState{}, domainerr.Precondition("Work content document changed; reload before saving")
	}
	domain, err := loadWorkContentDomainContext(ctx, store, workID)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return workTargetLocaleState{}, normalizeWorkContentBlockError(err)
	}
	if snapshot.SourceLocale != domain.SourceLocale || snapshot.Document.Profile != workContentDocumentProfile {
		return workTargetLocaleState{}, domainerr.Precondition("Work content document source or profile is inconsistent")
	}
	sourceMetadata, sourceExists, err := store.LocaleMetadata(ctx, workID, domain.SourceLocale, forUpdate)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	if !sourceExists {
		return workTargetLocaleState{}, domainerr.Precondition("Work source locale metadata is missing")
	}
	state := workTargetLocaleState{Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: sourceMetadata}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &sourceMetadata
		return state, nil
	}
	target, exists, err := store.LocaleMetadata(ctx, workID, locale, forUpdate)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	if !exists {
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return workTargetLocaleState{}, domainerr.Precondition("Work target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveWorkTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	return state, nil
}

// workTargetLocaleMutationInput is one write to a Work target locale.
type workTargetLocaleMutationInput struct {
	WorkID                 string
	DocumentID             uuid.UUID
	Locale                 string
	Batch                  contentblock.Batch
	ExpectedTargetRevision *string
	Patch                  workTargetMetadataPatch
	Policy                 targetlocale.Policy
	Now                    time.Time
	Fence                  ContentFence
}

func applyWorkTargetLocaleMutation(
	ctx context.Context,
	store Store,
	input workTargetLocaleMutationInput,
) (contentblock.Result, *string, error) {
	workID, documentID, locale, patch := input.WorkID, input.DocumentID, input.Locale, input.Patch
	domain, err := input.Fence(ctx, store, documentID)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	state, err := loadWorkTargetLocaleState(ctx, store, workID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, nil, domainerr.InvalidField("locale", "Work target locale must differ from source locale")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, nil, domainerr.Precondition("Work source locale changed; reload before saving")
	}
	if patch.DeleteLocale && (patch.EnsureLocale || patch.UpdateTitle || patch.UpdateSummary || len(input.Batch.LocaleGroups) != 0) {
		return contentblock.Result{}, nil, domainerr.InvalidField("operations", "Work translation deletion must be exclusive")
	}
	batch, err := input.Policy.Admit(state.locked(), locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if patch.DeleteLocale && state.TargetMetadata == nil {
		return contentblock.Result{}, nil, domainerr.Precondition("Work target locale must exist before deletion")
	}
	if patch.UpdateTitle && patch.Title != nil {
		trimmed := strings.TrimSpace(*patch.Title)
		patch.Title = &trimmed
	}
	now := targetlocale.WriteTime(input.Now)
	if patch.DeleteLocale {
		batch.LocaleGroups = targetlocale.DeleteGroups(state.Snapshot, locale)
	}
	write := &workTargetMetadataWrite{
		workID: workID, locale: locale, state: state, patch: patch, seedSource: input.Policy.SeedSource, now: now,
	}
	result, err := store.ApplyTargetLocaleContentBatch(ctx, batch, locale, lockedContentFence(documentID, domain), write.apply)
	if err != nil {
		return contentblock.Result{}, nil, normalizeWorkContentBlockError(err)
	}
	if result.Changed {
		if err := store.StampWork(ctx, workID, now); err != nil {
			return contentblock.Result{}, nil, err
		}
	}
	if patch.DeleteLocale {
		return result, nil, nil
	}
	revision, err := deriveWorkTargetRevision(result.DocumentRevision.String(), write.final)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	return result, &revision, nil
}

// workTargetMetadataWrite stores the Work target locale row of one write and
// keeps the row it leaves behind.
type workTargetMetadataWrite struct {
	workID     string
	locale     string
	state      workTargetLocaleState
	patch      workTargetMetadataPatch
	seedSource bool
	now        time.Time
	final      LocaleMetadata
}

func (w *workTargetMetadataWrite) apply(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error) {
	switch {
	case w.patch.DeleteLocale:
		if err := store.DeleteTargetLocale(ctx, w.workID, w.locale); err != nil {
			if errors.Is(err, ErrRowChanged) {
				return contentblock.MetadataEffect{}, errors.New("Work target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{}, err
		}
	case w.state.TargetMetadata == nil:
		if err := w.create(ctx, store); err != nil {
			return contentblock.MetadataEffect{}, err
		}
	default:
		changed, err := w.update(ctx, store, contentChanged)
		if err != nil || !changed {
			return contentblock.MetadataEffect{}, err
		}
	}
	return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.locale}}, nil
}

func (w *workTargetMetadataWrite) create(ctx context.Context, store Store) error {
	title, summary := w.patch.Title, w.patch.Summary
	if w.seedSource {
		title = cloneOptionalString(w.state.SourceMetadata.Title)
		summary = cloneOptionalString(w.state.SourceMetadata.Summary)
		if w.patch.UpdateTitle {
			title = cloneOptionalString(w.patch.Title)
		}
		if w.patch.UpdateSummary {
			summary = cloneOptionalString(w.patch.Summary)
		}
	}
	created := LocaleMetadata{Locale: w.locale, Title: cloneOptionalString(title), Summary: cloneOptionalString(summary), UpdatedAt: w.now}
	if err := store.InsertTargetLocale(ctx, w.workID, created); err != nil {
		if errors.Is(err, ErrRowChanged) {
			return errors.New("Work target locale could not be created")
		}
		return err
	}
	w.final = created
	return nil
}

func (w *workTargetMetadataWrite) update(ctx context.Context, store Store, contentChanged bool) (bool, error) {
	w.final = *w.state.TargetMetadata
	change := LocaleTextChange{}
	metadataChanged := false
	if w.patch.UpdateTitle && !nullableStringEqual(w.final.Title, w.patch.Title) {
		w.final.Title, change.SetTitle, change.Title, metadataChanged = cloneOptionalString(w.patch.Title), true, w.patch.Title, true
	}
	if w.patch.UpdateSummary && !nullableStringEqual(w.final.Summary, w.patch.Summary) {
		w.final.Summary, change.SetSummary, change.Summary, metadataChanged = cloneOptionalString(w.patch.Summary), true, w.patch.Summary, true
	}
	if !contentChanged && !metadataChanged {
		return false, nil
	}
	w.final.UpdatedAt = translation.NextTargetUpdatedAt(w.now, w.final.UpdatedAt)
	change.UpdatedAt = w.final.UpdatedAt
	if err := store.UpdateTargetLocale(ctx, w.workID, w.locale, change); err != nil {
		if errors.Is(err, ErrRowChanged) {
			return false, errors.New("Work target locale disappeared while saving")
		}
		return false, err
	}
	return true, nil
}

func workContributorUUIDs(values []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return nil, domainerr.InvalidField("contributor_member_ids", "must contain canonical Member UUIDs")
		}
		result = append(result, parsed)
	}
	return result, nil
}
