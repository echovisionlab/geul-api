package release

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// TranslationJob is the part of a translation job a Release delivery reads.
type TranslationJob struct {
	EntityType          string
	EntityID            string
	SourceLocale        string
	TargetLocale        string
	RequestedByMemberID string
}

// LoadTypedTranslationSourceDocument loads the canonical Release Rich Text
// document and Release-owned credit notes used by translation extraction.
func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	store Store,
	releaseID string,
) (*translation.SourceDocument, error) {
	sourceLocale, err := loadReleaseSourceLocale(ctx, store, releaseID, Unlocked)
	if err != nil {
		return nil, err
	}
	title, err := loadReleaseTranslationTitle(ctx, store, releaseID, sourceLocale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadReleaseContentDocumentID(ctx, store, releaseID, Unlocked)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return nil, normalizeReleaseContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(
		snapshot, sourceLocale,
	)
	if err != nil {
		return nil, normalizeReleaseContentBlockError(err)
	}
	result := &translation.SourceDocument{
		Title:                   title,
		ContentDocumentRevision: snapshot.Document.Revision.String(),
		ContentBlockDocument:    document,
	}
	result.ReleaseCreditNotes, err = store.CreditNotes(ctx, releaseID, sourceLocale)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// loadReleaseTranslationTitle reads the trimmed title of a locale; a
// missing locale is ErrLocaleNotFound.
func loadReleaseTranslationTitle(ctx context.Context, store Store, releaseID, locale string) (string, error) {
	title, err := store.LocaleTitle(ctx, releaseID, locale)
	if err != nil {
		return "", err
	}
	if title == nil {
		return "", nil
	}
	return strings.TrimSpace(*title), nil
}

// ApplyTypedTranslationInterchangeCandidateWithDB applies an interactive
// XLIFF target write with the exact target token observed by the caller.
// The caller records the change it observes.
func ApplyTypedTranslationInterchangeCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
	expectedTargetRevision *string,
	allowLocaleDeletes bool,
) error {
	_, err := applyTypedTranslationCandidate(
		ctx, store, job, candidate, metadataInput, expectedTargetRevision,
		targetlocale.Policy{AllowCreate: true, SeedSource: true, AllowLocaleDeletes: allowLocaleDeletes},
		true,
	)
	return err
}

// releaseTranslationApplied is the locale a translation write changed and
// how; changed is false when it changed nothing.
type releaseTranslationApplied struct {
	changed   bool
	locale    string
	operation sharedtelemetry.AuditItemOperation
}

func applyTypedTranslationCandidate(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
	expectedTargetRevision *string,
	policy targetlocale.Policy,
	allowEmptyCreditNotes bool,
) (releaseTranslationApplied, error) {
	if err := validateReleaseTranslationCandidate(store, job, candidate, metadataInput); err != nil {
		return releaseTranslationApplied{}, err
	}
	protectedTitle, err := loadReleaseTranslationTitle(ctx, store, job.EntityID, job.SourceLocale)
	if err != nil {
		return releaseTranslationApplied{}, err
	}
	metadataInput.Title = &protectedTitle
	if err := prepareReleaseTranslationCreditNotes(ctx, store, job.EntityID, candidate, allowEmptyCreditNotes); err != nil {
		return releaseTranslationApplied{}, err
	}
	apply, err := loadReleaseTranslationApply(ctx, store, job, candidate)
	if err != nil {
		return releaseTranslationApplied{}, err
	}
	apply.now = metadataInput.Now
	if candidate.HasProviderUnitPatch() {
		apply.creditNotePatch = candidate.ReleaseCreditNotes
	} else {
		apply.replaceCreditNotes = &candidate.ReleaseCreditNotes
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == apply.sourceLocale {
		return apply.applySource(ctx, store)
	}
	return apply.applyTarget(ctx, store, metadataInput.Title, expectedTargetRevision, policy)
}

func validateReleaseTranslationCandidate(
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
) error {
	if store == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Release translation candidate and content Block store are required")
	}
	if job.EntityType != releaseContentEntity {
		return domainerr.InvalidField("entity_type", "Release translation is required")
	}
	if metadataInput.Summary != nil || len(metadataInput.ContentJSON) != 0 ||
		metadataInput.ContentHTML != nil || metadataInput.ContentText != nil {
		return domainerr.Precondition("Release translation body is stored as typed Content Blocks")
	}
	return nil
}

// prepareReleaseTranslationCreditNotes takes a provider's credit notes from
// its unit results and checks every note names a credit of the Release.
func prepareReleaseTranslationCreditNotes(
	ctx context.Context,
	store Store,
	releaseID string,
	candidate *translation.Candidate,
	allowEmpty bool,
) error {
	if patch, ok := candidate.ProviderPatch(); ok {
		notes, err := buildReleaseProviderCreditNotePatch(ctx, store, releaseID, patch)
		if err != nil {
			return err
		}
		candidate.ReleaseCreditNotes = notes
	}
	if allowEmpty {
		return validateReleaseCreditNoteIdentityMap(ctx, store, releaseID, candidate.ReleaseCreditNotes)
	}
	return validateReleaseCreditNoteMap(ctx, store, releaseID, candidate.ReleaseCreditNotes)
}

// releaseTranslationApply writes one validated translation batch against the
// current source document.
type releaseTranslationApply struct {
	job                TranslationJob
	documentID         uuid.UUID
	sourceLocale       string
	expectedRevision   uuid.UUID
	batch              contentblock.Batch
	creditNotePatch    map[string]string
	replaceCreditNotes *map[string]string
	now                time.Time
}

// loadReleaseTranslationApply builds the candidate's batch against the
// current source document. The response is allowed to arrive after the
// source document advanced: the job's revision is an artifact observation,
// not a second source authority. Its shape is validated, and the write is
// fenced against the current shared document revision so unrelated source
// edits do not discard a completed target response.
func loadReleaseTranslationApply(
	ctx context.Context,
	store Store,
	job TranslationJob,
	candidate *translation.Candidate,
) (releaseTranslationApply, error) {
	documentID, err := loadReleaseContentDocumentID(ctx, store, job.EntityID, Unlocked)
	if err != nil {
		return releaseTranslationApply{}, err
	}
	sourceLocale, err := loadReleaseSourceLocale(ctx, store, job.EntityID, ShareLock)
	if err != nil {
		return releaseTranslationApply{}, err
	}
	if _, err := uuidFromCanonicalString(candidate.ContentDocumentRevision); err != nil {
		return releaseTranslationApply{}, fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return releaseTranslationApply{}, normalizeReleaseContentBlockError(err)
	}
	if snapshot.SourceLocale != sourceLocale || snapshot.Document.Profile != releaseContentProfile {
		return releaseTranslationApply{}, domainerr.Precondition("Release content document source or profile is inconsistent")
	}
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		batch, err = translation.BuildProviderTargetRichTextBatch(
			snapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, job.TargetLocale, candidate,
		)
	} else {
		batch, err = buildReleaseWholeLocaleBatch(snapshot, documentID, job.TargetLocale, candidate)
	}
	if err != nil {
		return releaseTranslationApply{}, normalizeReleaseContentBlockError(err)
	}
	return releaseTranslationApply{
		job: job, documentID: documentID, sourceLocale: sourceLocale,
		expectedRevision: snapshot.Document.Revision, batch: batch,
	}, nil
}

// buildReleaseWholeLocaleBatch replaces the target locale with the
// candidate: candidate Blocks the source still has are written, and current
// target Blocks the candidate omits are deleted.
func buildReleaseWholeLocaleBatch(
	snapshot contentblock.Snapshot,
	documentID uuid.UUID,
	targetLocale string,
	candidate *translation.Candidate,
) (contentblock.Batch, error) {
	targetDocument, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, targetLocale)
	if err != nil {
		return contentblock.Batch{}, err
	}
	currentBlockIDs := make(map[string]struct{}, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		currentBlockIDs[block.ID.String()] = struct{}{}
	}
	candidateBlockIDs := make(map[string]struct{}, len(candidate.ContentBlockLocaleOverlay.Blocks))
	mutations := make([]*contentv1.RichTextBlockLocaleMutation, 0,
		len(candidate.ContentBlockLocaleOverlay.Blocks)+len(targetDocument.GetLocaleOverlay().GetBlocks()))
	for _, block := range candidate.ContentBlockLocaleOverlay.Blocks {
		if _, exists := currentBlockIDs[block.GetBlockId()]; !exists {
			continue
		}
		candidateBlockIDs[block.GetBlockId()] = struct{}{}
		mutations = append(mutations, &contentv1.RichTextBlockLocaleMutation{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
			Upsert: &contentv1.UpsertRichTextBlockLocale{Block: block},
		}})
	}
	for _, block := range targetDocument.GetLocaleOverlay().GetBlocks() {
		if _, exists := currentBlockIDs[block.GetBlockId()]; !exists {
			continue
		}
		if _, replaced := candidateBlockIDs[block.GetBlockId()]; replaced {
			continue
		}
		mutations = append(mutations, &contentv1.RichTextBlockLocaleMutation{Operation: &contentv1.RichTextBlockLocaleMutation_Delete{
			Delete: &contentv1.DeleteRichTextBlockLocale{BlockId: block.GetBlockId()},
		}})
	}
	batch := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, ExpectedRevision: snapshot.Document.Revision.String(),
	}
	if len(mutations) != 0 {
		batch.LocaleMutationGroups = []*contentv1.RichTextLocaleMutationGroup{{Locale: targetLocale, Mutations: mutations}}
	}
	return contentblock.BatchFromRichTextSystemProto(documentID, batch)
}

// applySource writes a provider result whose target is the source locale:
// the source Blocks and the credit notes change as one source revision,
// attributed to the job's requester.
func (a releaseTranslationApply) applySource(ctx context.Context, store Store) (releaseTranslationApplied, error) {
	job := a.job
	requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return releaseTranslationApplied{}, errors.Join(errors.New("Release translation audit requires canonical requester Member"), parseErr)
	}
	batch := a.batch
	batch.ContributorMemberIDs = []uuid.UUID{requesterID}
	sourceLocale := a.sourceLocale
	result, err := store.ApplyContentBatchWithMetadata(
		ctx, batch, internalReleaseContentFence(job.EntityID),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			changed, metadataErr := applyReleaseSourceAIDocumentMetadata(ctx, store, AIDocumentMutation{
				ReleaseID: job.EntityID, Locale: sourceLocale, ExpectedSource: sourceLocale,
				CreditNotePatch: a.creditNotePatch,
			}, a.now)
			return contentblock.MetadataEffect{
				Changed: changed, AffectsTranslationSource: changed, SourceLocale: sourceLocale,
				ChangedLocales: []string{sourceLocale},
			}, metadataErr
		},
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return releaseTranslationApplied{}, fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	if err != nil {
		return releaseTranslationApplied{}, normalizeReleaseContentBlockError(err)
	}
	if !result.Changed {
		return releaseTranslationApplied{}, nil
	}
	if !result.TranslationSourceChanged {
		return releaseTranslationApplied{}, errors.New("provider source Release translation did not advance source state")
	}
	return releaseTranslationApplied{
		changed: true, locale: sourceLocale, operation: releaseTargetLocaleAuditOperation(false, false, true),
	}, nil
}

// applyTarget creates or overwrites the target locale and its credit notes.
func (a releaseTranslationApply) applyTarget(
	ctx context.Context,
	store Store,
	title *string,
	expectedTargetRevision *string,
	policy targetlocale.Policy,
) (releaseTranslationApplied, error) {
	job := a.job
	target, err := applyReleaseTargetLocaleMutation(ctx, store, releaseTargetLocaleMutationInput{
		ReleaseID: job.EntityID, DocumentID: a.documentID, Locale: job.TargetLocale,
		ExpectedDocumentRevision: a.expectedRevision, ExpectedTargetRevision: expectedTargetRevision,
		Policy: policy,
		Batch:  a.batch, SetTitle: true, Title: title,
		CreditNotePatch: a.creditNotePatch, ReplaceCreditNotes: a.replaceCreditNotes,
		Now: a.now, Fence: internalReleaseContentFence(job.EntityID),
	})
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return releaseTranslationApplied{}, fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	if err != nil {
		return releaseTranslationApplied{}, normalizeReleaseContentBlockError(err)
	}
	return releaseTranslationApplied{
		changed: target.Content.Changed, locale: job.TargetLocale,
		operation: releaseTargetLocaleAuditOperation(target.LocaleCreated, false, !target.LocaleCreated),
	}, nil
}

func buildReleaseProviderCreditNotePatch(
	ctx context.Context,
	store Store,
	releaseID string,
	patch *translation.ProviderUnitPatch,
) (map[string]string, error) {
	creditIDs, err := store.CreditIDs(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	current := make(map[string]struct{}, len(creditIDs))
	for _, creditID := range creditIDs {
		current[creditID] = struct{}{}
	}
	result := make(map[string]string)
	for _, unit := range patch.Units {
		if unit.ContainerType != translation.ContainerTypeRelation || unit.FieldName != "note" {
			continue
		}
		if _, exists := current[unit.ContainerID]; !exists {
			continue
		}
		translated, exists := patch.Results[unit.UnitID]
		if !exists {
			continue
		}
		result[unit.ContainerID] = strings.TrimSpace(
			translation.PreserveSourceEdgeWhitespace(unit.SourceText, translated.TranslatedText),
		)
	}
	return result, nil
}

// BuildTranslationExtractionPlan extracts Release-owned typed Rich Text and
// credit-note units. Release title is context/protected terminology, not a
// translated field.
func BuildTranslationExtractionPlan(
	job TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job.EntityType != releaseContentEntity || source == nil || source.ContentBlockDocument == nil {
		return nil, errors.New("typed Release translation source is required")
	}
	if strings.TrimSpace(job.SourceLocale) == "" || source.ContentBlockDocument.GetLocale() != job.SourceLocale {
		return nil, errors.New("typed Release translation source locale does not match the job")
	}
	overlay := source.ContentBlockDocument.GetLocaleOverlay()
	if overlay == nil || overlay.GetLocale() != job.SourceLocale {
		return nil, errors.New("typed Release source overlay is required")
	}

	units := make([]translation.Unit, 0, 16)
	for _, block := range overlay.GetBlocks() {
		if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
			return nil, errors.New("typed Release translation Block ID is required")
		}
		blockID := block.GetBlockId()
		prefix := "block:" + blockID
		extracted, err := translation.ExtractRichTextUnits(block, translation.RichTextUnitScope{
			EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
			ContainerID: blockID, UnitPrefix: prefix, PathPrefix: prefix,
		})
		if err != nil {
			return nil, err
		}
		units = append(units, extracted...)
	}
	creditIDs := make([]string, 0, len(source.ReleaseCreditNotes))
	for creditID, note := range source.ReleaseCreditNotes {
		if strings.TrimSpace(creditID) != "" && strings.TrimSpace(note) != "" {
			creditIDs = append(creditIDs, creditID)
		}
	}
	sort.Strings(creditIDs)
	for _, creditID := range creditIDs {
		units = append(units, translation.Unit{
			UnitID: "credit-note:" + creditID, EntityType: job.EntityType, EntityID: job.EntityID,
			Path: "credit-note:" + creditID, ContainerType: translation.ContainerTypeRelation,
			ContainerID: creditID, FieldName: "note", SourceText: source.ReleaseCreditNotes[creditID],
			SourceFormat: translation.SourceFormatPlainText, SourceLocale: job.SourceLocale,
		})
	}
	if !translation.HasNonEmptyUnit(units) {
		return nil, translation.ErrNoTranslatableUnits
	}
	return &translation.ExtractionPlan{
		EntityType: job.EntityType, EntityID: job.EntityID,
		SourceLocale: job.SourceLocale, TargetLocale: job.TargetLocale,
		ContextTitle: translation.NonBlankString(strings.TrimSpace(source.Title)),
		Units:        units,
		Bundles: translation.BuildBundles(
			job.EntityType, job.EntityID, job.SourceLocale, job.TargetLocale, units, nil,
		),
	}, nil
}

func validateReleaseCreditNoteMap(
	ctx context.Context,
	store Store,
	releaseID string,
	notes map[string]string,
) error {
	creditIDs, err := store.CreditIDs(ctx, releaseID)
	if err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(creditIDs))
	for _, creditID := range creditIDs {
		allowed[creditID] = struct{}{}
	}
	for creditID, note := range notes {
		if _, ok := allowed[creditID]; !ok {
			return domainerr.InvalidField("credit_notes", "credit_id does not belong to the Release")
		}
		if strings.TrimSpace(note) == "" {
			return domainerr.InvalidField("credit_notes", "note must not be empty")
		}
	}
	return nil
}

func uuidFromCanonicalString(value string) (uuid.UUID, error) {
	revision, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || revision == uuid.Nil || revision.String() != strings.TrimSpace(value) {
		return uuid.Nil, fmt.Errorf("canonical UUID is required")
	}
	return revision, nil
}
