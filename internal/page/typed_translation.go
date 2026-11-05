package page

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/echovisionlab/geul-api/internal/translation"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// BuildTranslationExtractionPlan extracts Page-owned locale units from the
// canonical typed document.
func BuildTranslationExtractionPlan(
	job TranslationJob,
	source *translation.SourceDocument,
) (*translation.ExtractionPlan, error) {
	if job.EntityType != "page" || source == nil || source.PageDocument == nil {
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
		EntityType:   job.EntityType,
		EntityID:     job.EntityID,
		SourceLocale: job.SourceLocale,
		TargetLocale: job.TargetLocale,
		ContextTitle: translation.NonBlankString(strings.TrimSpace(source.Title)),
		Units:        units,
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
	job TranslationJob,
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
	job TranslationJob,
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
	job TranslationJob,
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

// pageTranslationLocaleBatch is the system batch that upserts mutations in
// locale at expectedRevision.
func pageTranslationLocaleBatch(
	documentID, expectedRevision uuid.UUID,
	locale string,
	mutations []*contentv1.PageSectionLocaleMutation,
) (contentblock.Batch, error) {
	return contentblock.BatchFromPageSystemProto(documentID, &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        expectedRevision.String(),
		LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{
			Locale: locale, Mutations: mutations,
		}},
	})
}

// providerPageTargetBuild merges provider results into the current target
// sections of one Page document.
type providerPageTargetBuild struct {
	source, current   *contentv1.LocalizedPageDocument
	patch             *translation.ProviderUnitPatch
	sourceSections    map[string]*contentv1.PageSectionLocale
	currentSections   map[string]*contentv1.PageSectionLocale
	candidateSections map[string]*contentv1.PageSectionLocale
	baseSections      map[string]*contentv1.PageSection
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

func pageSystemTranslationDocumentFence(pageID string) ContentFence {
	return lockedPageContentFence(pageID)
}
