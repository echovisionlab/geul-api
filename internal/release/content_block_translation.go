package release

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LoadTypedTranslationSourceDocument loads the canonical Release Rich Text
// document and Release-owned credit notes used by translation extraction.
func LoadTypedTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	releaseID string,
) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("release translation content Block store is not configured")
	}
	source, err := loadReleaseSourceLocale(ctx, db, releaseID)
	if err != nil {
		return nil, err
	}
	title, err := loadReleaseTranslationTitle(
		ctx, db, releaseID, source.SourceLocale,
	)
	if err != nil {
		return nil, err
	}
	documentID, err := loadReleaseContentDocumentID(ctx, db, releaseID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, source.SourceLocale)
	if err != nil {
		return nil, normalizeReleaseContentBlockError(err)
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(
		snapshot, source.SourceLocale,
	)
	if err != nil {
		return nil, normalizeReleaseContentBlockError(err)
	}
	result := &translation.SourceDocument{
		Title:                   title,
		ContentDocumentRevision: snapshot.Document.Revision.String(),
		ContentBlockDocument:    document,
		ProtectedTerms:          translation.NormalizeProtectedTerms([]string{title}),
	}
	result.ReleaseCreditNotes, err = loadReleaseCreditLocaleNotes(
		ctx, db, releaseID, source.SourceLocale,
	)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func loadReleaseTranslationTitle(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	locale string,
) (string, error) {
	var row struct {
		Title *string `gorm:"column:title"`
	}
	result := db.WithContext(ctx).
		Table("release_translation").
		Select("title").
		Where("entity_id = ? AND locale = ?", releaseID, locale).
		Take(&row)
	if result.Error != nil {
		return "", result.Error
	}
	if row.Title == nil {
		return "", nil
	}
	return strings.TrimSpace(*row.Title), nil
}

// ApplyTypedTranslationCandidateWithDB persists a translated Release locale
// overlay and Release-owned credit notes behind the Release document fence,
// and records the change for the Member who requested the job.
func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
	audit domainaudit.Appender,
) error {
	applied, err := applyTypedTranslationCandidateWithDB(
		ctx, tx, store, job, candidate, metadataInput, nil,
		targetlocale.Policy{
			AllowCreate: true, SeedSource: true, AllowLocaleDeletes: true, RevisionMode: targetlocale.RevisionCurrent,
		},
		false,
	)
	if err != nil || !applied.changed {
		return err
	}
	if strings.TrimSpace(job.RequestedByMemberID) == "" {
		return errors.New("Release translation audit requires requester Member")
	}
	return domainaudit.AppendMember(
		ctx, tx, audit, job.RequestedByMemberID, sharedtelemetry.AuditReleaseUpdated,
		releaseLocaleContentRecord(job.EntityID, applied.locale, applied.operation),
	)
}

// ApplyTypedTranslationInterchangeCandidateWithDB applies an interactive
// XLIFF target write with the exact target token observed by the caller.
// The caller records the change it observes.
func ApplyTypedTranslationInterchangeCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
	expectedTargetRevision *string,
	allowLocaleDeletes bool,
) error {
	_, err := applyTypedTranslationCandidateWithDB(
		ctx, tx, store, job, candidate, metadataInput, expectedTargetRevision,
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

func applyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
	expectedTargetRevision *string,
	policy targetlocale.Policy,
	allowEmptyCreditNotes bool,
) (releaseTranslationApplied, error) {
	if err := validateReleaseTranslationCandidate(store, job, candidate, metadataInput); err != nil {
		return releaseTranslationApplied{}, err
	}
	protectedTitle, err := loadReleaseTranslationTitle(ctx, tx, job.EntityID, job.SourceLocale)
	if err != nil {
		return releaseTranslationApplied{}, err
	}
	metadataInput.Title = &protectedTitle
	if err := prepareReleaseTranslationCreditNotes(ctx, tx, job.EntityID, candidate, allowEmptyCreditNotes); err != nil {
		return releaseTranslationApplied{}, err
	}
	apply, err := loadReleaseTranslationApply(ctx, tx, store, job, candidate)
	if err != nil {
		return releaseTranslationApplied{}, err
	}
	apply.now = metadataInput.Now
	if candidate.HasProviderUnitPatch() {
		apply.creditNotePatch = candidate.ReleaseCreditNotes
	} else {
		apply.replaceCreditNotes = &candidate.ReleaseCreditNotes
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == apply.source.SourceLocale {
		return apply.applySource(ctx, tx)
	}
	return apply.applyTarget(ctx, tx, metadataInput.Title, expectedTargetRevision, policy)
}

func validateReleaseTranslationCandidate(
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
) error {
	if store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
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
	tx *gorm.DB,
	releaseID string,
	candidate *translation.Candidate,
	allowEmpty bool,
) error {
	if patch, ok := candidate.ProviderPatch(); ok {
		notes, err := buildReleaseProviderCreditNotePatch(ctx, tx, releaseID, patch)
		if err != nil {
			return err
		}
		candidate.ReleaseCreditNotes = notes
	}
	if allowEmpty {
		return validateReleaseCreditNoteIdentityMap(ctx, tx, releaseID, candidate.ReleaseCreditNotes)
	}
	return validateReleaseCreditNoteMap(ctx, tx, releaseID, candidate.ReleaseCreditNotes)
}

// releaseTranslationApply writes one validated translation batch against the
// current source document.
type releaseTranslationApply struct {
	store              *contentblock.Store
	job                *model.TranslationJob
	documentID         uuid.UUID
	source             releaseSourceLocale
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
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
) (releaseTranslationApply, error) {
	documentID, err := loadReleaseContentDocumentID(ctx, tx, job.EntityID)
	if err != nil {
		return releaseTranslationApply{}, err
	}
	source, err := loadReleaseSourceLocaleWithStrength(ctx, tx, job.EntityID, clause.LockingStrengthShare)
	if err != nil {
		return releaseTranslationApply{}, err
	}
	if _, err := uuidFromCanonicalString(candidate.ContentDocumentRevision); err != nil {
		return releaseTranslationApply{}, fmt.Errorf("%w: %w", translation.ErrSourceNoLongerCurrent, err)
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return releaseTranslationApply{}, normalizeReleaseContentBlockError(err)
	}
	if snapshot.SourceLocale != source.SourceLocale || snapshot.Document.Profile != releaseContentProfile {
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
		store: store, job: job, documentID: documentID, source: source,
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
func (a releaseTranslationApply) applySource(ctx context.Context, tx *gorm.DB) (releaseTranslationApplied, error) {
	job := a.job
	requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return releaseTranslationApplied{}, errors.Join(errors.New("Release translation audit requires canonical requester Member"), parseErr)
	}
	batch := a.batch
	batch.ContributorMemberIDs = []uuid.UUID{requesterID}
	sourceLocale := a.source.SourceLocale
	result, err := a.store.ApplyBatchWithMetadata(
		ctx, tx, batch, internalReleaseContentFence(job.EntityID),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			changed, metadataErr := applyReleaseSourceAIDocumentMetadata(ctx, tx, AIDocumentMutation{
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
	tx *gorm.DB,
	title *string,
	expectedTargetRevision *string,
	policy targetlocale.Policy,
) (releaseTranslationApplied, error) {
	job := a.job
	target, err := applyReleaseTargetLocaleMutation(ctx, tx, a.store, releaseTargetLocaleMutationInput{
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
	tx *gorm.DB,
	releaseID string,
	patch *translation.ProviderUnitPatch,
) (map[string]string, error) {
	creditIDs, err := loadReleaseCreditIDs(ctx, tx, releaseID)
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
	job *model.TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job == nil || job.EntityType != releaseContentEntity || source == nil || source.ContentBlockDocument == nil {
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
		ContextTitle:   translation.NonBlankString(strings.TrimSpace(source.Title)),
		ProtectedTerms: translation.NormalizeProtectedTerms(source.ProtectedTerms),
		Units:          units,
		Bundles: translation.BuildBundles(
			job.EntityType, job.EntityID, job.SourceLocale, job.TargetLocale, units, nil,
		),
	}, nil
}

// BuildTranslationCandidate applies validated results to a cloned Release
// locale overlay and projects Release-owned credit notes by stable credit ID.
func BuildTranslationCandidate(
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	if plan == nil || plan.EntityType != releaseContentEntity || source == nil || source.ContentBlockDocument == nil ||
		source.ContentBlockDocument.GetLocaleOverlay() == nil {
		return nil, errors.New("typed Release translation source is required")
	}
	if strings.TrimSpace(plan.TargetLocale) == "" {
		return nil, errors.New("typed Release translation target locale is required")
	}
	requestedBlockIDs := make(map[string]struct{})
	for _, unit := range plan.Units {
		if unit.ContainerType == translation.ContainerTypeBlock && strings.TrimSpace(unit.ContainerID) != "" {
			requestedBlockIDs[unit.ContainerID] = struct{}{}
		}
	}
	overlay := &contentv1.RichTextLocaleOverlay{Locale: plan.TargetLocale}
	for _, sourceBlock := range source.ContentBlockDocument.GetLocaleOverlay().GetBlocks() {
		if _, requested := requestedBlockIDs[sourceBlock.GetBlockId()]; !requested {
			continue
		}
		block := proto.Clone(sourceBlock).(*contentv1.RichTextBlockLocale)
		if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
			return nil, errors.New("typed Release translation Block ID is required")
		}
		if err := translation.ApplyRichTextResults(block, "block:"+block.GetBlockId(), results); err != nil {
			return nil, err
		}
		overlay.Blocks = append(overlay.Blocks, block)
	}
	candidate := &translation.Candidate{
		ContentBlockLocaleOverlay: overlay,
		ContentDocumentRevision:   source.ContentDocumentRevision,
		ReleaseCreditNotes:        make(map[string]string, len(source.ReleaseCreditNotes)),
	}
	translation.ApplyCandidateFields(candidate, plan.Bundles, results)
	for creditID, sourceText := range source.ReleaseCreditNotes {
		result, ok := results["credit-note:"+creditID]
		if !ok {
			continue
		}
		candidate.ReleaseCreditNotes[creditID] = strings.TrimSpace(
			translation.PreserveSourceEdgeWhitespace(sourceText, result.TranslatedText),
		)
	}
	return candidate, nil
}

func validateReleaseCreditNoteMap(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
	notes map[string]string,
) error {
	creditIDs, err := loadReleaseCreditIDs(ctx, tx, releaseID)
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
