package label

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
)

// labelEntityName names the owner in shared content document messages.
const labelEntityName = "Label"

type labelTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata LocaleTitle
	TargetMetadata *LocaleTitle
	TargetRevision string
}

func (state labelTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: labelEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

type labelTargetLocaleMutationInput struct {
	LabelID                  string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	Batch                    contentblock.Batch
	Policy                   targetlocale.Policy
	Now                      time.Time
	Fence                    ContentFence
}

type labelTargetLocaleMutationResult struct {
	Content        contentblock.Result
	TargetRevision string
	Created        bool
}

func loadLabelTargetLocaleState(
	ctx context.Context,
	store Store,
	labelID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (labelTargetLocaleState, error) {
	if store == nil || documentID == uuid.Nil {
		return labelTargetLocaleState{}, errors.New("Label target locale dependencies are required")
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return labelTargetLocaleState{}, domainerr.Required("locale")
	}
	canonicalLocale, err := normalizeLabelDocumentLocale(locale)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	locale = canonicalLocale
	lock := Unlocked
	if forUpdate {
		lock = UpdateLock
	}
	sourceLocale, err := loadLabelSourceLocale(ctx, store, labelID, lock)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if locale == sourceLocale {
		return labelTargetLocaleState{}, domainerr.InvalidField("locale", "Label target locale must differ from the source locale")
	}
	if forUpdate {
		if err := store.LockContentDocument(ctx, documentID); err != nil {
			return labelTargetLocaleState{}, normalizeLabelContentBlockError(err)
		}
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return labelTargetLocaleState{}, normalizeLabelContentBlockError(err)
	}
	if snapshot.Document.Profile != creativeContentProfile {
		return labelTargetLocaleState{}, domainerr.Precondition("Label target locale requires the compact content profile")
	}
	sourceMetadata, exists, err := store.LocaleTitle(ctx, labelID, sourceLocale, forUpdate)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if !exists {
		return labelTargetLocaleState{}, errors.New("Label source locale metadata is missing")
	}
	target, exists, err := store.LocaleTitle(ctx, labelID, locale, forUpdate)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if !exists && targetlocale.SnapshotHasLocale(snapshot, locale) {
		return labelTargetLocaleState{}, domainerr.Precondition("Label target locale Blocks exist without owning metadata")
	}
	state := labelTargetLocaleState{Snapshot: snapshot, SourceLocale: sourceLocale, SourceMetadata: sourceMetadata}
	if exists {
		state.TargetMetadata = &target
		state.TargetRevision, err = deriveLabelTargetRevision(snapshot.Document.Revision.String(), target.UpdatedAt)
		if err != nil {
			return labelTargetLocaleState{}, err
		}
	}
	return state, nil
}

func deriveLabelTargetRevision(documentRevision string, updatedAt time.Time) (string, error) {
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
	}
	return revision, nil
}

func applyLabelTargetLocaleMutation(
	ctx context.Context,
	store Store,
	input labelTargetLocaleMutationInput,
) (labelTargetLocaleMutationResult, error) {
	if input.ExpectedDocumentRevision == uuid.Nil || strings.TrimSpace(input.LabelID) == "" ||
		strings.TrimSpace(input.Locale) == "" || input.Fence == nil {
		return labelTargetLocaleMutationResult{}, domainerr.InvalidField("target", "Label target identity, revision, and fence are required")
	}
	documentID, err := loadLabelContentDocumentID(ctx, store, input.LabelID)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	domain, err := input.Fence(ctx, store, documentID)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	state, err := loadLabelTargetLocaleState(ctx, store, input.LabelID, documentID, input.Locale, true)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	if state.SourceLocale != domain.SourceLocale {
		return labelTargetLocaleMutationResult{}, domainerr.Precondition("Label source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return labelTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	batch, err := input.Policy.Admit(state.locked(), input.Locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	now := targetlocale.WriteTime(input.Now)
	var finalUpdatedAt time.Time
	result, err := store.ApplyTargetLocaleContentBatch(ctx, batch, input.Locale, lockedContentFence(documentID, domain),
		func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				created, err := store.CreateTargetLocale(ctx, input.LabelID, input.Locale, now)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				if !created {
					return contentblock.MetadataEffect{}, errors.New("Label target locale could not be created")
				}
				finalUpdatedAt = now
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
			}
			finalUpdatedAt = state.TargetMetadata.UpdatedAt
			if !contentChanged {
				return contentblock.MetadataEffect{}, nil
			}
			finalUpdatedAt = translation.NextTargetUpdatedAt(now, state.TargetMetadata.UpdatedAt)
			updated, err := store.TouchTargetLocale(ctx, input.LabelID, input.Locale, finalUpdatedAt)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if !updated {
				return contentblock.MetadataEffect{}, errors.New("Label target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		})
	if err != nil {
		return labelTargetLocaleMutationResult{}, normalizeLabelContentBlockError(err)
	}
	if finalUpdatedAt.IsZero() && state.TargetMetadata != nil {
		finalUpdatedAt = state.TargetMetadata.UpdatedAt
	}
	targetRevision, err := deriveLabelTargetRevision(result.DocumentRevision.String(), finalUpdatedAt)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	return labelTargetLocaleMutationResult{
		Content: result, TargetRevision: targetRevision, Created: state.TargetMetadata == nil,
	}, nil
}

func deleteLabelTargetLocale(
	ctx context.Context,
	store Store,
	labelID string,
	locale string,
	expectedDocumentRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	fence ContentFence,
) (contentblock.Result, error) {
	documentID, err := loadLabelContentDocumentID(ctx, store, labelID)
	if err != nil {
		return contentblock.Result{}, err
	}
	domain, err := fence(ctx, store, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadLabelTargetLocaleState(ctx, store, labelID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if state.Snapshot.Document.Revision != expectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked().CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil {
		return contentblock.Result{DocumentRevision: state.Snapshot.Document.Revision}, nil
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision, ContributorMemberIDs: contributors,
		LocaleGroups: targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	return store.ApplyTargetLocaleContentBatch(ctx, batch, locale, lockedContentFence(documentID, domain),
		func(ctx context.Context, store Store, _ bool) (contentblock.MetadataEffect, error) {
			deleted, err := store.DeleteTargetLocale(ctx, labelID, locale)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if !deleted {
				return contentblock.MetadataEffect{}, errors.New("Label target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		})
}
