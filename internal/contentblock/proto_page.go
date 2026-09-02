package contentblock

import (
	"fmt"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// BatchFromPageProtoWithAffectedLocaleValues preserves explicitly authored
// protobuf-default locale leaves across Page Sections and nested Rich Text.
func BatchFromPageProtoWithAffectedLocaleValues(
	documentID uuid.UUID,
	input *contentv1.PageSectionMutationBatch,
	locale string,
	affectedLocaleValues []*managev1.AIDocumentFieldTarget,
) (Batch, error) {
	return batchFromPageProto(documentID, input, locale, affectedLocaleValues)
}

func batchFromPageProto(
	documentID uuid.UUID,
	input *contentv1.PageSectionMutationBatch,
	locale string,
	affectedLocaleValues []*managev1.AIDocumentFieldTarget,
) (Batch, error) {
	storage, err := contentv1.FlattenPageMutationBatchStorage(
		input,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return Batch{}, invalidProtoMutation("flatten Page mutation", err)
	}
	if err := RestorePageAffectedLocaleValues(
		locale,
		&storage,
		affectedLocaleValues,
	); err != nil {
		return Batch{}, invalidProtoMutation("restore Page locale value presence", err)
	}
	return batchFromStorage(documentID, "page", storage)
}

// BatchFromPageSystemProto converts a trusted system mutation. The generated
// system flattener owns the empty-contributor boundary and every other Page
// mutation check.
func BatchFromPageSystemProto(documentID uuid.UUID, input *contentv1.PageSectionMutationBatch) (Batch, error) {
	storage, err := contentv1.FlattenPageSystemMutationBatchStorage(
		input,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return Batch{}, invalidProtoMutation("flatten system Page mutation", err)
	}
	return batchFromStorage(documentID, "page", storage)
}

func SnapshotToPageDocument(input Snapshot) (*contentv1.PageDocument, error) {
	if input.Document.Profile != "page" {
		return nil, invalidProtoMutation("Page profile", fmt.Errorf("document profile is %q", input.Document.Profile))
	}
	document, err := contentv1.MaterializePageDocumentStorage(input.SourceLocale, snapshotToStorageRows(input))
	if err != nil {
		return nil, invalidProtoMutation("materialize Page document", err)
	}
	return document, nil
}

func SnapshotToLocalizedPageDocument(input Snapshot, locale string) (*contentv1.LocalizedPageDocument, error) {
	if input.Document.Profile != "page" {
		return nil, invalidProtoMutation("Page profile", fmt.Errorf("document profile is %q", input.Document.Profile))
	}
	document, err := contentv1.MaterializePageDocumentStorage(input.SourceLocale, snapshotToStorageRows(input))
	if err != nil {
		return nil, invalidProtoMutation("materialize localized Page document", err)
	}
	overlay := &contentv1.PageLocaleOverlay{Locale: locale}
	for _, candidate := range document.GetLocaleOverlays() {
		if candidate.GetLocale() == locale {
			overlay = proto.Clone(candidate).(*contentv1.PageLocaleOverlay)
			break
		}
	}
	return &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Locale:                  locale,
		Base:                    proto.Clone(document.GetBase()).(*contentv1.PageSectionGraph),
		LocaleOverlay:           overlay,
	}, nil
}

// MaterializeSnapshotPageLocale builds the public read projection for one
// requested Page locale. Missing target Sections, nested Rich Text Blocks, and
// optional fields fall back to current source while explicit empty values stay
// empty.
func MaterializeSnapshotPageLocale(input Snapshot, locale string) (*contentv1.LocalizedPageDocument, error) {
	document, err := SnapshotToPageDocument(input)
	if err != nil {
		return nil, err
	}
	localized, err := localizedPageDocumentForMaterialization(document, locale)
	if err != nil {
		return nil, invalidProtoMutation("materialize public Page locale", err)
	}
	return localized, nil
}

func localizedPageDocumentForMaterialization(
	document *contentv1.PageDocument,
	locale string,
) (*contentv1.LocalizedPageDocument, error) {
	if err := contentv1.ValidatePageDocument(
		document,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	); err != nil {
		return nil, err
	}
	var source *contentv1.PageLocaleOverlay
	var target *contentv1.PageLocaleOverlay
	for _, overlay := range document.GetLocaleOverlays() {
		switch overlay.GetLocale() {
		case document.GetSourceLocale():
			source = overlay
		case locale:
			target = overlay
		}
	}
	if source == nil {
		return nil, fmt.Errorf("source Page locale overlay is missing")
	}
	if locale == document.GetSourceLocale() {
		target = source
	}
	sourceBySection := make(map[string]*contentv1.PageSectionLocale, len(source.GetSections()))
	for _, section := range source.GetSections() {
		sourceBySection[section.GetSectionId()] = section
	}
	targetBySection := make(map[string]*contentv1.PageSectionLocale)
	if target != nil {
		for _, section := range target.GetSections() {
			targetBySection[section.GetSectionId()] = section
		}
	}
	merged := &contentv1.PageLocaleOverlay{Locale: locale}
	for _, node := range document.GetBase().GetNodes() {
		sectionID := node.GetSection().GetId()
		sourceSection := sourceBySection[sectionID]
		if sourceSection == nil {
			return nil, fmt.Errorf("source Page locale Section %s is missing", sectionID)
		}
		section, err := mergePageSectionLocale(
			node.GetSection(), sourceSection, targetBySection[sectionID],
			document.GetSourceLocale(), locale,
		)
		if err != nil {
			return nil, err
		}
		merged.Sections = append(merged.Sections, section)
	}
	result := &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Locale:                  locale,
		Base:                    proto.Clone(document.GetBase()).(*contentv1.PageSectionGraph),
		LocaleOverlay:           merged,
	}
	aggregate := &contentv1.PageDocument{
		BlockCatalogFingerprint: result.GetBlockCatalogFingerprint(),
		SourceLocale:            locale,
		Base:                    result.GetBase(),
		LocaleOverlays:          []*contentv1.PageLocaleOverlay{result.GetLocaleOverlay()},
	}
	if err := contentv1.ValidatePageDocument(
		aggregate,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	); err != nil {
		return nil, err
	}
	return result, nil
}

func mergePageSectionLocale(
	base *contentv1.PageSection,
	source *contentv1.PageSectionLocale,
	target *contentv1.PageSectionLocale,
	sourceLocale string,
	targetLocale string,
) (*contentv1.PageSectionLocale, error) {
	if base.GetRichText() != nil {
		if source.GetRichText() == nil || (target != nil && target.GetRichText() == nil) {
			return nil, fmt.Errorf("page rich text section locale kind differs from base")
		}
		targetOverlay := &contentv1.RichTextLocaleOverlay{Locale: targetLocale}
		if target != nil && target.GetRichText().GetBlocks() != nil {
			targetOverlay = proto.Clone(target.GetRichText().GetBlocks()).(*contentv1.RichTextLocaleOverlay)
			targetOverlay.Locale = targetLocale
		}
		richDocument := &contentv1.RichTextDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE,
			SourceLocale:            sourceLocale,
			Base:                    proto.Clone(base.GetRichText().GetBlocks()).(*contentv1.RichTextBlockGraph),
			LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
				proto.Clone(source.GetRichText().GetBlocks()).(*contentv1.RichTextLocaleOverlay),
			},
		}
		if targetLocale != sourceLocale {
			richDocument.LocaleOverlays = append(richDocument.LocaleOverlays, targetOverlay)
		}
		localized, err := localizedRichTextDocumentForMaterialization(richDocument, targetLocale)
		if err != nil {
			return nil, err
		}
		return &contentv1.PageSectionLocale{
			SectionId: base.GetId(),
			Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
				Props:  &contentv1.RichTextSectionLocaleProps{},
				Blocks: localized.GetLocaleOverlay(),
			}},
		}, nil
	}
	if base.GetImmersiveScene() != nil {
		if source.GetImmersiveScene() == nil || (target != nil && target.GetImmersiveScene() == nil) {
			return nil, fmt.Errorf("page immersive scene section locale kind differs from base")
		}
		return mergePageImmersiveSceneLocale(base, source, target), nil
	}
	if target == nil {
		return proto.Clone(source).(*contentv1.PageSectionLocale), nil
	}
	result := proto.Clone(source).(*contentv1.PageSectionLocale)
	proto.Merge(result, target)
	return result, nil
}

func mergePageImmersiveSceneLocale(
	base *contentv1.PageSection,
	source *contentv1.PageSectionLocale,
	target *contentv1.PageSectionLocale,
) *contentv1.PageSectionLocale {
	sourceValue := source.GetImmersiveScene()
	resultValue := &contentv1.ImmersiveSceneSectionLocale{}
	if sourceValue.GetProps() != nil {
		resultValue.Props = proto.Clone(sourceValue.GetProps()).(*contentv1.ImmersiveSceneSectionLocaleProps)
	}
	if target != nil && target.GetImmersiveScene().GetProps() != nil {
		if resultValue.Props == nil {
			resultValue.Props = &contentv1.ImmersiveSceneSectionLocaleProps{}
		}
		proto.Merge(resultValue.Props, target.GetImmersiveScene().GetProps())
	}
	sourceByUnit := make(map[string]*contentv1.PageImmersiveUnitLocale, len(sourceValue.GetUnits()))
	for _, unit := range sourceValue.GetUnits() {
		sourceByUnit[unit.GetUnitId()] = unit
	}
	targetByUnit := make(map[string]*contentv1.PageImmersiveUnitLocale)
	if target != nil {
		for _, unit := range target.GetImmersiveScene().GetUnits() {
			targetByUnit[unit.GetUnitId()] = unit
		}
	}
	for _, unit := range base.GetImmersiveScene().GetUnits() {
		sourceUnit := sourceByUnit[unit.GetId()]
		if sourceUnit == nil {
			continue
		}
		mergedUnit := proto.Clone(sourceUnit).(*contentv1.PageImmersiveUnitLocale)
		if targetUnit := targetByUnit[unit.GetId()]; targetUnit != nil {
			proto.Merge(mergedUnit, targetUnit)
		}
		resultValue.Units = append(resultValue.Units, mergedUnit)
	}
	return &contentv1.PageSectionLocale{
		SectionId: base.GetId(),
		Value:     &contentv1.PageSectionLocale_ImmersiveScene{ImmersiveScene: resultValue},
	}
}

// ReplaceFromLocalizedPageProtoWithUnavailableAttachments converts a Page
// Version snapshot after rewriting only explicitly unavailable active File
// references to generated, restore-only missing_attachment values. The input
// snapshot is not mutated.
func ReplaceFromLocalizedPageProtoWithUnavailableAttachments(
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	input *contentv1.LocalizedPageDocument,
	unavailable map[uuid.UUID]contentv1.MissingAttachmentMediaKind,
) (ReplaceInput, error) {
	return replaceFromLocalizedPageProto(documentID, expectedRevision, input, unavailable)
}

func replaceFromLocalizedPageProto(
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	input *contentv1.LocalizedPageDocument,
	unavailable map[uuid.UUID]contentv1.MissingAttachmentMediaKind,
) (ReplaceInput, error) {
	if input == nil {
		return ReplaceInput{}, invalidProtoMutation("localized Page document", fmt.Errorf("document is required"))
	}
	aggregate := &contentv1.PageDocument{
		BlockCatalogFingerprint: input.GetBlockCatalogFingerprint(),
		SourceLocale:            input.GetLocale(),
		Base:                    input.GetBase(),
		LocaleOverlays:          []*contentv1.PageLocaleOverlay{input.GetLocaleOverlay()},
	}
	rows, err := contentv1.FlattenPageDocumentStorage(
		aggregate,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	)
	if err != nil {
		return ReplaceInput{}, invalidProtoMutation("flatten localized Page document", err)
	}
	if len(unavailable) != 0 {
		unavailableStorage, unavailableErr := missingAttachmentKindsToStorage(unavailable)
		if unavailableErr != nil {
			return ReplaceInput{}, invalidProtoMutation("unavailable Page attachments", unavailableErr)
		}
		rows, err = contentv1.RewriteContentStorageMissingAttachments(
			"page",
			input.GetLocale(),
			rows,
			unavailableStorage,
		)
		if err != nil {
			return ReplaceInput{}, invalidProtoMutation("rewrite unavailable Page attachments", err)
		}
	}
	return replaceFromStorage(documentID, expectedRevision, rows)
}
