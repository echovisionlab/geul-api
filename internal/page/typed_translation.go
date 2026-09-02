package page

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm"
)

// BuildTranslationExtractionPlan extracts Page-owned locale units from the
// canonical typed document.
func BuildTranslationExtractionPlan(
	job *model.TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job == nil || job.EntityType != "page" || source == nil || source.PageDocument == nil {
		return nil, fmt.Errorf("typed Page translation source is required")
	}
	if strings.TrimSpace(job.SourceLocale) == "" || source.PageDocument.GetLocale() != job.SourceLocale {
		return nil, fmt.Errorf("typed Page translation source locale does not match the job")
	}
	overlay := source.PageDocument.GetLocaleOverlay()
	if overlay == nil || overlay.GetLocale() != job.SourceLocale {
		return nil, fmt.Errorf("typed Page source overlay is required")
	}

	units := make([]translation.Unit, 0, 16)
	if title := strings.TrimSpace(source.Title); title != "" {
		units = append(units, translation.NewEntityUnit(job.EntityType, job.EntityID, job.SourceLocale, "title", title))
	}
	if summary := strings.TrimSpace(derefString(source.Summary)); summary != "" {
		units = append(units, translation.NewEntityUnit(job.EntityType, job.EntityID, job.SourceLocale, "summary", summary))
	}
	for _, section := range overlay.GetSections() {
		if section == nil || strings.TrimSpace(section.GetSectionId()) == "" {
			return nil, fmt.Errorf("typed Page translation section ID is required")
		}
		if err := appendPageTypedTranslationUnits(job, section, &units); err != nil {
			return nil, err
		}
	}
	if !translation.HasNonEmptyUnit(units) {
		return nil, translation.ErrNoTranslatableUnits
	}

	return &translation.ExtractionPlan{
		EntityType:     job.EntityType,
		EntityID:       job.EntityID,
		SourceLocale:   job.SourceLocale,
		TargetLocale:   job.TargetLocale,
		ContextTitle:   translation.NonBlankString(strings.TrimSpace(source.Title)),
		ProtectedTerms: translation.NormalizeProtectedTerms(source.ProtectedTerms),
		Units:          units,
		Bundles: translation.BuildBundles(
			job.EntityType,
			job.EntityID,
			job.SourceLocale,
			job.TargetLocale,
			units,
			translation.NonBlankString(strings.TrimSpace(derefString(source.Summary))),
		),
	}, nil
}

func appendPageTypedTranslationUnits(
	job *model.TranslationJob,
	section *contentv1.PageSectionLocale,
	units *[]translation.Unit,
) error {
	sectionID := section.GetSectionId()
	if richText := section.GetRichText(); richText != nil {
		overlay := richText.GetBlocks()
		if overlay == nil || overlay.GetLocale() != job.SourceLocale {
			return fmt.Errorf("typed Page Rich Text source overlay is required")
		}
		for _, block := range overlay.GetBlocks() {
			if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
				return fmt.Errorf("typed Page Rich Text Block ID is required")
			}
			if err := appendPageTypedRichTextTranslationUnits(job, sectionID, block, units); err != nil {
				return err
			}
		}
		return nil
	}
	if immersive := section.GetImmersiveScene(); immersive != nil {
		for _, unit := range immersive.GetUnits() {
			if unit == nil || strings.TrimSpace(unit.GetUnitId()) == "" {
				return fmt.Errorf("typed Page immersive unit ID is required")
			}
			appendPageTypedImmersiveTranslationUnits(job, sectionID, unit, units)
		}
		return nil
	}
	walkPageTypedTranslationStrings(
		section.ProtoReflect(),
		nil,
		func(path []string, sourceText string) {
			fieldName := path[len(path)-1]
			*units = append(*units, translation.Unit{
				UnitID:        pageTypedSectionTranslationUnitID(sectionID, path),
				EntityType:    job.EntityType,
				EntityID:      job.EntityID,
				Path:          "section:" + sectionID + ":" + strings.Join(path, "."),
				ContainerType: translation.ContainerTypeBlock,
				ContainerID:   sectionID,
				FieldName:     fieldName,
				SourceText:    sourceText,
				SourceFormat:  translation.SourceFormatPlainText,
				SourceLocale:  job.SourceLocale,
			})
		},
	)
	return nil
}

func appendPageTypedRichTextTranslationUnits(
	job *model.TranslationJob,
	sectionID string,
	block *contentv1.RichTextBlockLocale,
	units *[]translation.Unit,
) error {
	blockID := block.GetBlockId()
	prefix := "section:" + sectionID + ":block:" + blockID
	extracted, err := translation.ExtractRichTextUnits(block, translation.RichTextUnitScope{
		EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
		ContainerID: sectionID, UnitPrefix: prefix, PathPrefix: prefix,
	})
	if err != nil {
		return err
	}
	*units = append(*units, extracted...)
	return nil
}

func appendPageTypedImmersiveTranslationUnits(
	job *model.TranslationJob,
	sectionID string,
	unit *contentv1.PageImmersiveUnitLocale,
	units *[]translation.Unit,
) {
	if unit.GetProps() == nil {
		return
	}
	unitID := unit.GetUnitId()
	walkPageTypedTranslationStrings(
		unit.GetProps().ProtoReflect(),
		nil,
		func(path []string, sourceText string) {
			*units = append(*units, translation.Unit{
				UnitID:        pageTypedImmersiveTranslationUnitID(sectionID, unitID, path),
				EntityType:    job.EntityType,
				EntityID:      job.EntityID,
				Path:          "section:" + sectionID + ":immersive-unit:" + unitID + ":" + strings.Join(path, "."),
				ContainerType: translation.ContainerTypeBlock,
				ContainerID:   sectionID,
				FieldName:     path[len(path)-1],
				SourceText:    sourceText,
				SourceFormat:  translation.SourceFormatPlainText,
				SourceLocale:  job.SourceLocale,
			})
		},
	)
}

func walkPageTypedTranslationStrings(
	message protoreflect.Message,
	path []string,
	visit func([]string, string),
) {
	descriptor := message.Descriptor()
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		isTranslationString := field.Kind() == protoreflect.StringKind &&
			pageTypedTranslationStringField(descriptor, field)
		if !message.Has(field) && !field.IsList() &&
			(!isTranslationString || field.HasPresence()) {
			continue
		}
		fieldPath := append(append([]string(nil), path...), string(field.Name()))
		if field.IsList() {
			list := message.Get(field).List()
			for itemIndex := 0; itemIndex < list.Len(); itemIndex++ {
				itemPath := append(append([]string(nil), fieldPath...), strconv.Itoa(itemIndex))
				if field.Kind() == protoreflect.MessageKind {
					walkPageTypedTranslationStrings(list.Get(itemIndex).Message(), itemPath, visit)
				}
			}
			continue
		}
		if field.Kind() == protoreflect.MessageKind {
			walkPageTypedTranslationStrings(message.Get(field).Message(), fieldPath, visit)
			continue
		}
		if !isTranslationString {
			continue
		}
		visit(fieldPath, message.Get(field).String())
	}
}

func pageTypedTranslationStringField(
	message protoreflect.MessageDescriptor,
	field protoreflect.FieldDescriptor,
) bool {
	if string(message.Name()) == "PageImmersiveUnitLocaleProps" {
		switch string(field.Name()) {
		case "title", "text":
			return true
		default:
			return false
		}
	}
	return translation.IsRichTextTranslationStringField(message, field)
}

func pageTypedSectionTranslationUnitID(sectionID string, path []string) string {
	return "section:" + sectionID + ":typed:" + strings.Join(path, "/")
}

func pageTypedImmersiveTranslationUnitID(sectionID string, unitID string, path []string) string {
	return "section:" + sectionID + ":immersive-unit:" + unitID + ":typed:" + strings.Join(path, "/")
}

// BuildTranslationCandidate applies validated unit results to a Page target
// locale candidate without changing source-owned structure.
func BuildTranslationCandidate(
	plan *translation.ExtractionPlan,
	source *translation.SourceDocument,
	results map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	if plan == nil || plan.EntityType != "page" || source == nil || source.PageDocument == nil ||
		source.PageDocument.GetLocaleOverlay() == nil {
		return nil, fmt.Errorf("typed Page translation source is required")
	}
	if strings.TrimSpace(plan.TargetLocale) == "" {
		return nil, fmt.Errorf("typed Page translation target locale is required")
	}

	document := proto.Clone(source.PageDocument).(*contentv1.LocalizedPageDocument)
	document.Locale = plan.TargetLocale
	document.LocaleOverlay.Locale = plan.TargetLocale
	for _, section := range document.LocaleOverlay.Sections {
		if section == nil || strings.TrimSpace(section.GetSectionId()) == "" {
			return nil, fmt.Errorf("typed Page translation section ID is required")
		}
		if err := applyPageTypedTranslationResults(section, plan.TargetLocale, results); err != nil {
			return nil, err
		}
	}

	candidate := &translation.Candidate{
		ContentDocumentRevision: source.ContentDocumentRevision,
		PageDocument:            document,
	}
	translation.ApplyCandidateFields(candidate, plan.Bundles, results)
	return candidate, nil
}

func applyPageTypedTranslationResults(
	section *contentv1.PageSectionLocale,
	targetLocale string,
	results map[string]translation.UnitResult,
) error {
	sectionID := section.GetSectionId()
	if richText := section.GetRichText(); richText != nil {
		overlay := richText.GetBlocks()
		if overlay == nil {
			return fmt.Errorf("typed Page Rich Text locale overlay is required")
		}
		overlay.Locale = targetLocale
		for _, block := range overlay.GetBlocks() {
			if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
				return fmt.Errorf("typed Page Rich Text Block ID is required")
			}
			if err := applyPageTypedRichTextTranslationResults(sectionID, block, results); err != nil {
				return err
			}
		}
		return nil
	}
	if immersive := section.GetImmersiveScene(); immersive != nil {
		for _, unit := range immersive.GetUnits() {
			if unit == nil || strings.TrimSpace(unit.GetUnitId()) == "" {
				return fmt.Errorf("typed Page immersive unit ID is required")
			}
			applyPageTypedImmersiveTranslationResults(sectionID, unit, results)
		}
		return nil
	}
	walkMutablePageTypedTranslationStrings(
		section.ProtoReflect(),
		nil,
		func(path []string, message protoreflect.Message, field protoreflect.FieldDescriptor) {
			result, exists := results[pageTypedSectionTranslationUnitID(sectionID, path)]
			if !exists {
				return
			}
			current := message.Get(field).String()
			message.Set(field, protoreflect.ValueOfString(
				translation.PreserveSourceEdgeWhitespace(current, result.TranslatedText),
			))
		},
	)
	return nil
}

func applyPageTypedRichTextTranslationResults(
	sectionID string,
	block *contentv1.RichTextBlockLocale,
	results map[string]translation.UnitResult,
) error {
	prefix := "section:" + sectionID + ":block:" + block.GetBlockId()
	return translation.ApplyRichTextResults(block, prefix, results)
}

func applyPageTypedImmersiveTranslationResults(
	sectionID string,
	unit *contentv1.PageImmersiveUnitLocale,
	results map[string]translation.UnitResult,
) {
	if unit.GetProps() == nil {
		return
	}
	unitID := unit.GetUnitId()
	walkMutablePageTypedTranslationStrings(
		unit.GetProps().ProtoReflect(),
		nil,
		func(path []string, message protoreflect.Message, field protoreflect.FieldDescriptor) {
			result, exists := results[pageTypedImmersiveTranslationUnitID(sectionID, unitID, path)]
			if !exists {
				return
			}
			current := message.Get(field).String()
			message.Set(field, protoreflect.ValueOfString(
				translation.PreserveSourceEdgeWhitespace(current, result.TranslatedText),
			))
		},
	)
}

func walkMutablePageTypedTranslationStrings(
	message protoreflect.Message,
	path []string,
	visit func([]string, protoreflect.Message, protoreflect.FieldDescriptor),
) {
	descriptor := message.Descriptor()
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		if !message.Has(field) && !field.IsList() {
			continue
		}
		fieldPath := append(append([]string(nil), path...), string(field.Name()))
		if field.IsList() {
			list := message.Mutable(field).List()
			for itemIndex := 0; itemIndex < list.Len(); itemIndex++ {
				itemPath := append(append([]string(nil), fieldPath...), strconv.Itoa(itemIndex))
				if field.Kind() == protoreflect.MessageKind {
					walkMutablePageTypedTranslationStrings(list.Get(itemIndex).Message(), itemPath, visit)
				}
			}
			continue
		}
		if field.Kind() == protoreflect.MessageKind {
			walkMutablePageTypedTranslationStrings(message.Mutable(field).Message(), fieldPath, visit)
			continue
		}
		if field.Kind() == protoreflect.StringKind && pageTypedTranslationStringField(descriptor, field) {
			visit(fieldPath, message, field)
		}
	}
}

// ApplyTranslationCandidateWithDB atomically replaces one Page target locale
// document after the shared runtime validates the current source authority.
func ApplyTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if tx == nil || store == nil || job == nil || job.EntityType != "page" ||
		candidate == nil || candidate.PageDocument == nil || candidate.PageDocument.GetLocaleOverlay() == nil {
		return errors.New("typed Page translation candidate and Content Block store are required")
	}
	if candidate.PageDocument.GetLocale() != job.TargetLocale ||
		candidate.PageDocument.GetLocaleOverlay().GetLocale() != job.TargetLocale {
		return errs.FailedPrecondition("typed Page translation target locale does not match the job")
	}
	entry.Title = candidate.Title
	entry.Summary = candidate.Summary

	documentID, err := loadPageContentDocumentID(ctx, tx, job.EntityID)
	if err != nil {
		return err
	}
	expectedRevision, err := parsePageContentUUID(
		"content_document_revision",
		candidate.ContentDocumentRevision,
	)
	if err != nil {
		return translation.ErrSourceNoLongerCurrent
	}
	document := candidate.PageDocument
	providerTargetExists := false
	if providerPatch, ok := candidate.ProviderPatch(); ok {
		domain, fenceErr := pageSystemTranslationDocumentFence(job.EntityID)(ctx, tx, documentID)
		if fenceErr != nil {
			return fenceErr
		}
		if job.TargetLocale == domain.SourceLocale {
			return applyPageProviderSourceCandidate(
				ctx, tx, store, job, candidate, entry, auditWriter, documentID, domain, providerPatch,
			)
		}
		state, loadErr := loadPageTargetLocaleState(
			ctx, tx, store, job.EntityID, documentID, job.TargetLocale, true,
		)
		if loadErr != nil {
			return normalizePageContentBlockError(loadErr)
		}
		snapshot := state.Snapshot
		providerTargetExists = state.TargetMetadata != nil
		expectedRevision = snapshot.Document.Revision
		sourceDocument, sourceErr := contentblock.SnapshotToLocalizedPageDocument(snapshot, state.SourceLocale)
		if sourceErr != nil {
			return normalizePageContentBlockError(sourceErr)
		}
		currentTarget, targetErr := contentblock.SnapshotToLocalizedPageDocument(snapshot, job.TargetLocale)
		if targetErr != nil {
			return normalizePageContentBlockError(targetErr)
		}
		document, err = buildProviderPageTargetDocument(
			sourceDocument, currentTarget, candidate.PageDocument, providerPatch,
		)
		if err != nil {
			return err
		}
	}
	mutations, err := pageTypedTranslationLocaleMutations(document)
	if err != nil {
		return err
	}
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision}
	if !candidate.HasProviderUnitPatch() || len(mutations) != 0 {
		batch, err = contentblock.BatchFromPageSystemProto(documentID, &contentv1.PageSectionMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			ExpectedRevision:        expectedRevision.String(),
			LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{
				Locale: job.TargetLocale, Mutations: mutations,
			}},
		})
	}
	if err != nil {
		return normalizePageContentBlockError(err)
	}
	if candidate.HasProviderUnitPatch() {
		result, _, applyErr := applyPageTargetLocaleBatchUsingCurrentRevision(
			ctx, tx, store, job.EntityID, documentID, job.TargetLocale, batch,
			pageTargetMetadataPatch{
				EnsureLocale:  true,
				UpdateTitle:   candidate.ProviderUnitRequested("entity:title"),
				Title:         candidate.Title,
				UpdateSummary: candidate.ProviderUnitRequested("entity:summary"),
				Summary:       candidate.Summary,
			},
			true, false, entry.Now, pageSystemTranslationDocumentFence(job.EntityID),
		)
		if applyErr != nil {
			return applyErr
		}
		if !result.Changed {
			return nil
		}
		if err := tx.WithContext(ctx).Model(&model.Page{}).
			Where("id = ?", job.EntityID).UpdateColumn("updated_at", entry.Now).Error; err != nil {
			return errs.Internal(err)
		}
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errors.New("page translation provider delivery requires its requesting Member")
		}
		operation := sharedtelemetry.AuditItemOperationUpdated
		if !providerTargetExists {
			operation = sharedtelemetry.AuditItemOperationCreated
		}
		return appendPageMemberLocaleContentAudit(
			ctx, tx, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
			job.EntityID, job.TargetLocale, operation,
		)
	}
	result, err := store.ApplyBatch(
		ctx,
		tx,
		batch,
		pageSystemTranslationDocumentFence(job.EntityID),
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	if err != nil {
		return normalizePageContentBlockError(err)
	}
	if result.TranslationSourceChanged {
		return errs.InternalMsg("target Page translation changed the source-owned Block view")
	}
	if candidate.HasProviderUnitPatch() &&
		(!candidate.ProviderUnitRequested("entity:title") || !candidate.ProviderUnitRequested("entity:summary")) {
		var current struct {
			Title   *string `gorm:"column:title"`
			Summary *string `gorm:"column:summary"`
		}
		loaded := tx.WithContext(ctx).Table("page_translation").
			Select("title", "summary").
			Where("entity_id = ? AND locale = ?", job.EntityID, job.TargetLocale).
			Take(&current)
		if loaded.Error != nil && !errors.Is(loaded.Error, gorm.ErrRecordNotFound) {
			return errs.Internal(loaded.Error)
		}
		if !candidate.ProviderUnitRequested("entity:title") {
			entry.Title = current.Title
		}
		if !candidate.ProviderUnitRequested("entity:summary") {
			entry.Summary = current.Summary
		}
	}
	return UpsertTranslationMetadataEntry(
		ctx,
		tx,
		job.EntityID,
		job.TargetLocale,
		entry,
	)
}

func applyPageProviderSourceCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	entry translation.EntryWrite,
	auditWriter domainaudit.Appender,
	documentID uuid.UUID,
	domain contentblock.DomainContext,
	providerPatch *translation.ProviderUnitPatch,
) error {
	requesterID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || requesterID == uuid.Nil || requesterID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errors.New("page translation provider delivery requires its requesting Member")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return normalizePageContentBlockError(err)
	}
	current, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, domain.SourceLocale)
	if err != nil {
		return normalizePageContentBlockError(err)
	}
	document, err := buildProviderPageTargetDocument(current, current, candidate.PageDocument, providerPatch)
	if err != nil {
		return err
	}
	mutations, err := pageTypedTranslationLocaleMutations(document)
	if err != nil {
		return err
	}
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: snapshot.Document.Revision}
	if len(mutations) != 0 {
		batch, err = contentblock.BatchFromPageSystemProto(documentID, &contentv1.PageSectionMutationBatch{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			ExpectedRevision:        snapshot.Document.Revision.String(),
			LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{
				Locale: domain.SourceLocale, Mutations: mutations,
			}},
		})
		if err != nil {
			return normalizePageContentBlockError(err)
		}
	}
	batch.ContributorMemberIDs = []uuid.UUID{requesterID}
	metadataPatch := AIDocumentMetadataPatch{
		SetTitle: candidate.ProviderUnitRequested("entity:title"), Title: candidate.Title,
		SetSummary: candidate.ProviderUnitRequested("entity:summary"), Summary: candidate.Summary,
	}
	result, err := store.ApplyBatchWithMetadata(
		ctx, tx, batch, pageSystemTranslationDocumentFence(job.EntityID),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return applyPageAIDocumentMetadata(
				ctx, tx, job.EntityID, domain.SourceLocale, true, metadataPatch, entry.Now,
			)
		},
	)
	if errors.Is(err, contentblock.ErrStaleRevision) {
		return translation.ErrSourceNoLongerCurrent
	}
	if err != nil {
		return normalizePageContentBlockError(err)
	}
	if !result.Changed {
		return nil
	}
	if !result.TranslationSourceChanged {
		return errs.InternalMsg("provider source Page translation did not advance source state")
	}
	if err := tx.WithContext(ctx).Model(&model.Page{}).
		Where("id = ?", job.EntityID).UpdateColumn("updated_at", entry.Now).Error; err != nil {
		return errs.Internal(err)
	}
	return appendPageMemberLocaleContentAudit(
		ctx, tx, auditWriter, job.RequestedByMemberID, job.EntityID, domain.SourceLocale,
		sharedtelemetry.AuditItemOperationUpdated,
	)
}

func buildProviderPageTargetDocument(
	source *contentv1.LocalizedPageDocument,
	current *contentv1.LocalizedPageDocument,
	candidate *contentv1.LocalizedPageDocument,
	patch *translation.ProviderUnitPatch,
) (*contentv1.LocalizedPageDocument, error) {
	if source == nil || source.GetLocaleOverlay() == nil || current == nil ||
		current.GetLocaleOverlay() == nil || candidate == nil || candidate.GetLocaleOverlay() == nil || patch == nil {
		return nil, errors.New("provider Page source, target, candidate, and unit patch are required")
	}
	unitsBySection := make(map[string][]translation.Unit)
	for _, unit := range patch.Units {
		if _, translated := patch.Results[unit.UnitID]; !translated {
			continue
		}
		sectionID, _, ok := pageInterchangeSectionHandle(unit.UnitID)
		if ok {
			unitsBySection[sectionID] = append(unitsBySection[sectionID], unit)
		}
	}
	result := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: source.GetBlockCatalogFingerprint(),
		Locale:                  current.GetLocale(), Base: source.GetBase(),
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: current.GetLocale()},
	}
	sourceSections := pageInterchangeLocaleSections(source.GetLocaleOverlay())
	currentSections := pageInterchangeLocaleSections(current.GetLocaleOverlay())
	candidateSections := pageInterchangeLocaleSections(candidate.GetLocaleOverlay())
	baseSections := pageInterchangeBaseSections(source.GetBase())
	for _, sourceSection := range source.GetLocaleOverlay().GetSections() {
		sectionID := sourceSection.GetSectionId()
		units := unitsBySection[sectionID]
		if len(units) == 0 {
			continue
		}
		candidateSection := candidateSections[sectionID]
		if candidateSection == nil || sourceSections[sectionID] == nil {
			continue
		}
		if sourceSection.GetRichText() != nil {
			baseSection := baseSections[sectionID]
			if baseSection == nil || baseSection.GetRichText() == nil {
				return nil, fmt.Errorf("provider Page Rich Text section %q is outside the current graph", sectionID)
			}
			scopedPatch := &translation.ProviderUnitPatch{Results: make(map[string]translation.UnitResult)}
			for _, unit := range units {
				_, rest, ok := pageInterchangeSectionHandle(unit.UnitID)
				if !ok || !strings.HasPrefix(rest, "block:") {
					continue
				}
				blockID, ok := pageInterchangeBlockID(rest)
				if !ok {
					return nil, fmt.Errorf("provider Page Rich Text unit %q is invalid", unit.UnitID)
				}
				scoped := unit
				scoped.UnitID = rest
				scoped.ContainerID = blockID
				scopedPatch.Units = append(scopedPatch.Units, scoped)
				providerResult := patch.Results[unit.UnitID]
				providerResult.UnitID = rest
				scopedPatch.Results[rest] = providerResult
			}
			currentOverlay := &contentv1.RichTextLocaleOverlay{Locale: current.GetLocale()}
			if currentSection := currentSections[sectionID]; currentSection != nil && currentSection.GetRichText() != nil &&
				currentSection.GetRichText().GetBlocks() != nil {
				currentOverlay = currentSection.GetRichText().GetBlocks()
			}
			overlay, err := translation.BuildProviderTargetRichTextOverlay(
				&contentv1.LocalizedRichTextDocument{
					BlockCatalogFingerprint: source.GetBlockCatalogFingerprint(),
					Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE, Locale: source.GetLocale(),
					Base: baseSection.GetRichText().GetBlocks(), LocaleOverlay: sourceSection.GetRichText().GetBlocks(),
				},
				&contentv1.LocalizedRichTextDocument{
					BlockCatalogFingerprint: source.GetBlockCatalogFingerprint(),
					Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE, Locale: current.GetLocale(),
					Base: baseSection.GetRichText().GetBlocks(), LocaleOverlay: currentOverlay,
				},
				scopedPatch,
			)
			if err != nil {
				return nil, err
			}
			destination := &contentv1.PageSectionLocale{SectionId: sectionID}
			if existing := currentSections[sectionID]; existing != nil {
				destination = proto.Clone(existing).(*contentv1.PageSectionLocale)
			} else {
				destination = proto.Clone(candidateSection).(*contentv1.PageSectionLocale)
			}
			destination.GetRichText().Blocks = overlay
			result.LocaleOverlay.Sections = append(result.LocaleOverlay.Sections, destination)
			continue
		}

		destination := &contentv1.PageSectionLocale{SectionId: sectionID}
		if existing := currentSections[sectionID]; existing != nil {
			destination = proto.Clone(existing).(*contentv1.PageSectionLocale)
		}
		for _, unit := range units {
			_, rest, ok := pageInterchangeSectionHandle(unit.UnitID)
			if !ok || strings.HasPrefix(rest, "block:") {
				continue
			}
			path, ok := pageInterchangeContainerPath(rest, candidateSection)
			if !ok {
				return nil, fmt.Errorf("provider Page unit %q has an invalid stable path", unit.UnitID)
			}
			if err := translation.CopyStableProtoPath(
				destination.ProtoReflect(), candidateSection.ProtoReflect(), path,
			); err != nil {
				return nil, fmt.Errorf("copy provider Page unit %q: %w", unit.UnitID, err)
			}
		}
		result.LocaleOverlay.Sections = append(result.LocaleOverlay.Sections, destination)
	}
	return result, nil
}

func pageTypedTranslationLocaleMutations(
	document *contentv1.LocalizedPageDocument,
) ([]*contentv1.PageSectionLocaleMutation, error) {
	if document == nil || document.GetLocaleOverlay() == nil ||
		strings.TrimSpace(document.GetLocale()) == "" ||
		document.GetLocaleOverlay().GetLocale() != document.GetLocale() {
		return nil, fmt.Errorf("typed Page target locale overlay is required")
	}

	mutations := make([]*contentv1.PageSectionLocaleMutation, 0, len(document.LocaleOverlay.Sections))
	for _, section := range document.LocaleOverlay.Sections {
		if section == nil || strings.TrimSpace(section.GetSectionId()) == "" {
			return nil, fmt.Errorf("typed Page translation section ID is required")
		}
		sectionUpsert := proto.Clone(section).(*contentv1.PageSectionLocale)
		if richText := sectionUpsert.GetRichText(); richText != nil {
			richText.Blocks = nil
		}
		mutations = append(mutations, &contentv1.PageSectionLocaleMutation{
			Operation: &contentv1.PageSectionLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertPageSectionLocale{Section: sectionUpsert},
			},
		})

		richText := section.GetRichText()
		if richText == nil {
			continue
		}
		overlay := richText.GetBlocks()
		if overlay == nil || overlay.GetLocale() != document.GetLocale() {
			return nil, fmt.Errorf("typed Page Rich Text target locale overlay is required")
		}
		for _, block := range overlay.GetBlocks() {
			if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
				return nil, fmt.Errorf("typed Page Rich Text Block ID is required")
			}
			mutations = append(mutations, &contentv1.PageSectionLocaleMutation{
				Operation: &contentv1.PageSectionLocaleMutation_MutateRichTextBlock{
					MutateRichTextBlock: &contentv1.MutatePageRichTextBlockLocale{
						SectionId: section.GetSectionId(),
						Mutation: &contentv1.RichTextBlockLocaleMutation{
							Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
								Upsert: &contentv1.UpsertRichTextBlockLocale{Block: block},
							},
						},
					},
				},
			})
			continue
		}
	}
	return mutations, nil
}

func pageSystemTranslationDocumentFence(pageID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := lockPageContentDocumentRoot(ctx, tx, pageID, documentID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadPageContentDomainContext(ctx, tx, pageID)
	}
}
