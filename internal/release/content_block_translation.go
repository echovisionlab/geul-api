package release

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
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
// overlay and Release-owned credit notes behind the Release document fence.
func ApplyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if auditWriter == nil {
		return errors.New("release translation audit writer is required")
	}
	return applyTypedTranslationCandidateWithDB(
		ctx, tx, store, job, candidate, metadataInput, nil, true, true, false, auditWriter,
	)
}

// ApplyTypedTranslationInterchangeCandidateWithDB applies an interactive
// XLIFF target write with the exact target token observed by the caller.
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
	return applyTypedTranslationCandidateWithDB(
		ctx, tx, store, job, candidate, metadataInput, expectedTargetRevision, false, allowLocaleDeletes, true, nil,
	)
}

func applyTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	metadataInput translation.EntryWrite,
	expectedTargetRevision *string,
	overwriteCurrentTargetCAS bool,
	allowLocaleDeletes bool,
	allowEmptyCreditNotes bool,
	auditWriter domainaudit.Appender,
) error {
	if store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Release translation candidate and content Block store are required")
	}
	if job.EntityType != releaseContentEntity {
		return errs.InvalidArgument("entity_type", "Release translation is required")
	}
	if metadataInput.Summary != nil || len(metadataInput.ContentJSON) != 0 ||
		metadataInput.ContentHTML != nil || metadataInput.ContentText != nil {
		return errs.FailedPrecondition("Release translation body is stored as typed Content Blocks")
	}
	protectedTitle, err := loadReleaseTranslationTitle(
		ctx,
		tx,
		job.EntityID,
		job.SourceLocale,
	)
	if err != nil {
		return err
	}
	metadataInput.Title = &protectedTitle
	if patch, ok := candidate.ProviderPatch(); ok {
		candidate.ReleaseCreditNotes, err = buildReleaseProviderCreditNotePatch(ctx, tx, job.EntityID, patch)
		if err != nil {
			return err
		}
	}
	var creditNoteValidationErr error
	if allowEmptyCreditNotes {
		creditNoteValidationErr = validateReleaseCreditNoteIdentityMap(ctx, tx, job.EntityID, candidate.ReleaseCreditNotes)
	} else {
		creditNoteValidationErr = validateReleaseCreditNoteMap(ctx, tx, job.EntityID, candidate.ReleaseCreditNotes)
	}
	if creditNoteValidationErr != nil {
		return creditNoteValidationErr
	}
	documentID, err := loadReleaseContentDocumentID(ctx, tx, job.EntityID)
	if err != nil {
		return err
	}
	currentSource, err := loadReleaseSourceLocaleWithStrength(ctx, tx, job.EntityID, "SHARE")
	if err != nil {
		return err
	}
	// The response is allowed to arrive after the source document advanced.
	// The job's revision is an artifact observation, not a second source
	// authority. Validate its shape, then fence this write against the current
	// shared document revision so unrelated source edits do not discard a
	// completed target response.
	if _, err := uuidFromCanonicalString(
		candidate.ContentDocumentRevision,
	); err != nil {
		return errTranslationSourceNoLongerCurrent
	}
	currentSnapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, currentSource.SourceLocale)
	if err != nil {
		return normalizeReleaseContentBlockError(err)
	}
	if currentSnapshot.SourceLocale != currentSource.SourceLocale || currentSnapshot.Document.Profile != releaseContentProfile {
		return errs.FailedPrecondition("Release content document source or profile is inconsistent")
	}
	expectedRevision := currentSnapshot.Document.Revision
	var batch contentblock.Batch
	if candidate.HasProviderUnitPatch() {
		batch, err = translation.BuildProviderTargetRichTextBatch(
			currentSnapshot, contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, job.TargetLocale, candidate,
		)
	} else {
		targetDocument, loadErr := contentblock.SnapshotToLocalizedRichTextDocument(currentSnapshot, job.TargetLocale)
		if loadErr != nil {
			return normalizeReleaseContentBlockError(loadErr)
		}
		currentBlockIDs := make(map[string]struct{}, len(currentSnapshot.Blocks))
		for _, block := range currentSnapshot.Blocks {
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
		mutation := &contentv1.RichTextBlockMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, ExpectedRevision: expectedRevision.String(),
		}
		if len(mutations) != 0 {
			mutation.LocaleMutationGroups = []*contentv1.RichTextLocaleMutationGroup{{Locale: job.TargetLocale, Mutations: mutations}}
		}
		batch, err = contentblock.BatchFromRichTextSystemProto(documentID, mutation)
	}
	if err != nil {
		return normalizeReleaseContentBlockError(err)
	}
	creditNotePatch := map[string]string(nil)
	replaceCreditNotes := &candidate.ReleaseCreditNotes
	if candidate.HasProviderUnitPatch() {
		creditNotePatch = candidate.ReleaseCreditNotes
		replaceCreditNotes = nil
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == currentSource.SourceLocale {
		requesterID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if parseErr != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errs.InternalMsg("Release translation audit requires canonical requester Member")
		}
		batch.ContributorMemberIDs = []uuid.UUID{requesterID}
		result, applyErr := store.ApplyBatchWithMetadata(
			ctx, tx, batch, internalReleaseContentFence(job.EntityID),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				changed, metadataErr := applyReleaseSourceAIDocumentMetadata(ctx, tx, AIDocumentMutation{
					ReleaseID: job.EntityID, Locale: currentSource.SourceLocale, ExpectedSource: currentSource.SourceLocale,
					CreditNotePatch: creditNotePatch,
				}, metadataInput.Now)
				return contentblock.MetadataEffect{
					Changed: changed, AffectsTranslationSource: changed, SourceLocale: currentSource.SourceLocale,
					ChangedLocales: []string{currentSource.SourceLocale},
				}, metadataErr
			},
		)
		if errors.Is(applyErr, contentblock.ErrStaleRevision) {
			return errTranslationSourceNoLongerCurrent
		}
		if applyErr != nil {
			return normalizeReleaseContentBlockError(applyErr)
		}
		if !result.Changed {
			return nil
		}
		if !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source Release translation did not advance source state")
		}
		return appendReleaseMemberTargetLocaleAudit(
			ctx, tx, auditWriter, job.RequestedByMemberID, job.EntityID, currentSource.SourceLocale,
			releaseTargetLocaleAuditOperation(false, false, true),
		)
	}
	target, err := applyReleaseTargetLocaleMutation(ctx, tx, store, releaseTargetLocaleMutationInput{
		ReleaseID: job.EntityID, DocumentID: documentID, Locale: job.TargetLocale,
		ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: expectedTargetRevision,
		AllowCreate: true, OverwriteCurrentTargetCAS: overwriteCurrentTargetCAS,
		AllowLocaleDeletes: allowLocaleDeletes,
		Batch:              batch, SetTitle: true, Title: metadataInput.Title,
		CreditNotePatch: creditNotePatch, ReplaceCreditNotes: replaceCreditNotes,
		Now: metadataInput.Now, Fence: internalReleaseContentFence(job.EntityID),
	})
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return errTranslationSourceNoLongerCurrent
	}
	if err != nil {
		return normalizeReleaseContentBlockError(err)
	}
	if target.Content.Changed && auditWriter != nil {
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errs.InternalMsg("Release translation audit requires requester Member")
		}
		return appendReleaseMemberTargetLocaleAudit(
			ctx,
			tx,
			auditWriter,
			job.RequestedByMemberID,
			job.EntityID,
			job.TargetLocale,
			releaseTargetLocaleAuditOperation(target.LocaleCreated, false, !target.LocaleCreated),
		)
	}
	return nil
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
			return errs.InvalidArgument("credit_notes", "credit_id does not belong to the Release")
		}
		if strings.TrimSpace(note) == "" {
			return errs.InvalidArgument("credit_notes", "note must not be empty")
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
