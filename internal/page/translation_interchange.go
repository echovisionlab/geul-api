package page

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm"
)

type ProjectRichTextInterchangeTargets func(
	*translation.ExtractionPlan,
	*contentv1.LocalizedRichTextDocument,
) (map[string]translation.UnitResult, error)

type BuildRichTextInterchangePatch func(
	*translation.ExtractionPlan,
	*contentv1.LocalizedRichTextDocument,
	*contentv1.LocalizedRichTextDocument,
	map[string]translation.UnitResult,
) (*contentv1.RichTextLocaleOverlay, error)

type InterchangeProtoPathPresent func(protoreflect.Message, []string) bool
type CloneInterchangeUnitResult func(translation.UnitResult) translation.UnitResult
type EmptyInterchangeTargetInline func([]translation.XLIFFInline) []translation.XLIFFInline
type CopyInterchangeProtoPath func(protoreflect.Message, protoreflect.Message, []string) error

// TranslationInterchangeTarget is the Page-owned target projection used by
// XLIFF export. Page translation-row existence remains locale presence.
type TranslationInterchangeTarget struct {
	Exists   bool
	Revision string
	Targets  map[string]translation.UnitResult
}

// TranslationInterchangeMutation is one application-validated XLIFF import.
// Page revalidates identity, locale role and CAS under its own locks.
type TranslationInterchangeMutation struct {
	PageID           string
	SourceLocale     string
	TargetLocale     string
	Mode             managev1.TranslationInterchangeMode
	ExpectedRevision *string
	Source           *translation.SourceDocument
	Plan             *translation.ExtractionPlan
	Targets          map[string]translation.UnitResult
	UnitHandles      []string
	Now              time.Time
	ProjectTargets   ProjectRichTextInterchangeTargets
	BuildPatch       BuildRichTextInterchangePatch
	PathPresent      InterchangeProtoPathPresent
	CloneResult      CloneInterchangeUnitResult
	EmptyInline      EmptyInterchangeTargetInline
	CopyPath         CopyInterchangeProtoPath
}

type TranslationInterchangeResult struct {
	Revision               string
	Changed                bool
	AffectedUnitHandles    []string
	TargetPreviouslyExists bool
}

type pageInterchangeState struct {
	snapshot       contentblock.Snapshot
	metadata       pageAIDocumentLocale
	document       *contentv1.LocalizedPageDocument
	exists         bool
	targets        map[string]translation.UnitResult
	targetRevision string
}

func LoadTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	pageID string,
	locale string,
	plan *translation.ExtractionPlan,
	project ProjectRichTextInterchangeTargets,
	pathPresent InterchangeProtoPathPresent,
	cloneResult CloneInterchangeUnitResult,
	emptyInline EmptyInterchangeTargetInline,
) (TranslationInterchangeTarget, error) {
	state, err := loadPageInterchangeState(
		ctx, tx, store, pageID, locale, plan, "SHARE", project, pathPresent, cloneResult, emptyInline,
	)
	if err != nil {
		return TranslationInterchangeTarget{}, err
	}
	return TranslationInterchangeTarget{
		Exists: state.exists, Revision: state.targetRevision, Targets: state.targets,
	}, nil
}

func ApplyTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	mutation TranslationInterchangeMutation,
) (TranslationInterchangeResult, error) {
	if err := validatePageInterchangeMutation(mutation); err != nil {
		return TranslationInterchangeResult{}, err
	}
	state, err := loadPageInterchangeState(
		ctx, tx, store, mutation.PageID, mutation.TargetLocale, mutation.Plan, "UPDATE",
		mutation.ProjectTargets, mutation.PathPresent, mutation.CloneResult, mutation.EmptyInline,
	)
	if err != nil {
		return TranslationInterchangeResult{}, err
	}
	if err := translation.ValidateExpectedTargetRevision(
		mutation.ExpectedRevision, state.targetRevision, state.exists,
	); err != nil {
		var conflict *translation.TargetRevisionConflict
		if errors.As(err, &conflict) {
			return TranslationInterchangeResult{}, errs.FailedPrecondition(err.Error())
		}
		return TranslationInterchangeResult{}, errs.Internal(err)
	}

	desired := make(map[string]translation.UnitResult, len(state.targets)+len(mutation.Targets))
	if mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH {
		for handle, target := range state.targets {
			desired[handle] = mutation.CloneResult(target)
		}
	}
	for handle, target := range mutation.Targets {
		desired[handle] = mutation.CloneResult(target)
	}
	candidate, err := BuildTranslationCandidate(mutation.Plan, mutation.Source, desired)
	if err != nil {
		return TranslationInterchangeResult{}, errs.InvalidArgument("file_id", err.Error())
	}
	setExactPageInterchangeMetadata(candidate, desired)
	if err := patchPageRichTextInterchange(
		candidate.PageDocument, mutation.Source.PageDocument, state.document,
		mutation.Plan, mutation.Targets, mutation.Mode, mutation.BuildPatch,
	); err != nil {
		return TranslationInterchangeResult{}, errs.InvalidArgument("file_id", err.Error())
	}
	if err := patchPageContainerInterchange(
		candidate.PageDocument, state.document, mutation.UnitHandles, mutation.Mode, mutation.CopyPath,
	); err != nil {
		return TranslationInterchangeResult{}, errs.InvalidArgument("file_id", err.Error())
	}

	selectedSections := pageInterchangeSelectedSectionIDs(mutation.UnitHandles)
	document := candidate.PageDocument
	filtered := make([]*contentv1.PageSectionLocale, 0, len(selectedSections))
	for _, section := range document.GetLocaleOverlay().GetSections() {
		if _, selected := selectedSections[section.GetSectionId()]; selected {
			filtered = append(filtered, section)
		}
	}
	document.LocaleOverlay.Sections = filtered
	batch := contentblock.Batch{
		DocumentID: state.snapshot.Document.ID, ExpectedRevision: state.snapshot.Document.Revision,
	}
	if len(filtered) != 0 || mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		mutations, mutationErr := pageTypedTranslationLocaleMutations(document)
		if mutationErr != nil {
			return TranslationInterchangeResult{}, normalizePageContentBlockError(mutationErr)
		}
		if mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
			mutations = append(mutations, pageInterchangeReplacementDeletes(state.document, document)...)
		}
		if len(mutations) != 0 {
			batch, err = contentblock.BatchFromPageSystemProto(
				state.snapshot.Document.ID,
				&contentv1.PageSectionMutationBatch{
					BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
					ExpectedRevision:        state.snapshot.Document.Revision.String(),
					LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{
						Locale: mutation.TargetLocale, Mutations: mutations,
					}},
				},
			)
			if err != nil {
				return TranslationInterchangeResult{}, normalizePageContentBlockError(err)
			}
		}
	}

	metadataPatch := pageInterchangeMetadataPatch(mutation.Plan, desired)
	result, targetRevision, err := applyPageTargetLocaleBatch(
		ctx, tx, store, mutation.PageID, state.snapshot.Document.ID, mutation.TargetLocale,
		batch, mutation.ExpectedRevision,
		pageTargetMetadataPatch{
			EnsureLocale: metadataPatch.EnsureLocale,
			UpdateTitle:  metadataPatch.SetTitle, Title: metadataPatch.Title,
			UpdateSummary: metadataPatch.SetSummary, Summary: metadataPatch.Summary,
		},
		true,
		mutation.Mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		mutation.Now.UTC(), pageSystemTranslationDocumentFence(mutation.PageID),
	)
	if err != nil {
		return TranslationInterchangeResult{}, normalizePageContentBlockError(err)
	}
	if result.TranslationSourceChanged {
		return TranslationInterchangeResult{}, errs.InternalMsg("target Page interchange changed the source-owned Block view")
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.Page{}).
			Where("id = ?", mutation.PageID).
			UpdateColumn("updated_at", mutation.Now.UTC()).Error; err != nil {
			return TranslationInterchangeResult{}, errs.Internal(err)
		}
	}
	if targetRevision == nil {
		return TranslationInterchangeResult{}, errs.InternalMsg("Page interchange apply did not create its target locale")
	}
	affected := changedPageInterchangeHandles(state.targets, mutation.Targets, mutation.UnitHandles)
	if !result.Changed {
		affected = nil
	}
	return TranslationInterchangeResult{
		Revision: *targetRevision, Changed: result.Changed, AffectedUnitHandles: affected,
		TargetPreviouslyExists: state.exists,
	}, nil
}

func validatePageInterchangeMutation(mutation TranslationInterchangeMutation) error {
	if mutation.PageID == "" || mutation.Source == nil || mutation.Plan == nil || mutation.Targets == nil ||
		mutation.ProjectTargets == nil || mutation.BuildPatch == nil || mutation.PathPresent == nil ||
		mutation.CloneResult == nil || mutation.EmptyInline == nil || mutation.CopyPath == nil {
		return errs.InternalMsg("Page translation interchange mutation is incomplete")
	}
	if mutation.Plan.EntityType != "page" || mutation.Plan.EntityID != mutation.PageID ||
		mutation.Plan.SourceLocale != mutation.SourceLocale ||
		mutation.Plan.TargetLocale != mutation.TargetLocale ||
		mutation.Source.PageDocument == nil ||
		mutation.Source.PageDocument.GetLocale() != mutation.SourceLocale {
		return errs.InvalidArgument("file_id", "Page XLIFF identity does not match the current document")
	}
	if mutation.SourceLocale == mutation.TargetLocale {
		return errs.InvalidArgument("target_locale", "Page source translation cannot be imported as a target")
	}
	if mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH &&
		mutation.Mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
		return errs.InvalidArgument("mode", "PATCH or REPLACE is required")
	}
	known := make(map[string]struct{}, len(mutation.Plan.Units))
	for _, unit := range mutation.Plan.Units {
		known[unit.UnitID] = struct{}{}
	}
	if len(mutation.Targets) != len(mutation.UnitHandles) {
		return errs.InvalidArgument("file_id", "Page XLIFF target set does not match its stable unit manifest")
	}
	seen := make(map[string]struct{}, len(mutation.UnitHandles))
	for _, handle := range mutation.UnitHandles {
		if _, duplicate := seen[handle]; duplicate {
			return errs.InvalidArgument("file_id", "Page XLIFF stable units must be unique")
		}
		seen[handle] = struct{}{}
		if _, ok := known[handle]; !ok {
			return errs.InvalidArgument("file_id", "Page XLIFF contains an unknown stable unit")
		}
		if target, ok := mutation.Targets[handle]; !ok || target.UnitID != handle {
			return errs.InvalidArgument("file_id", "Page XLIFF target identity does not match its stable unit")
		}
	}
	for handle := range mutation.Targets {
		if _, ok := seen[handle]; !ok {
			return errs.InvalidArgument("file_id", "Page XLIFF target set does not match its stable unit manifest")
		}
	}
	return nil
}

func loadPageInterchangeState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	pageID string,
	locale string,
	plan *translation.ExtractionPlan,
	lock string,
	project ProjectRichTextInterchangeTargets,
	pathPresent InterchangeProtoPathPresent,
	cloneResult CloneInterchangeUnitResult,
	emptyInline EmptyInterchangeTargetInline,
) (pageInterchangeState, error) {
	if tx == nil || store == nil || plan == nil || plan.EntityType != "page" ||
		plan.EntityID != pageID || plan.TargetLocale != locale || project == nil ||
		pathPresent == nil || cloneResult == nil || emptyInline == nil {
		return pageInterchangeState{}, errs.InternalMsg("Page translation interchange load identity is invalid")
	}
	_, documentID, err := loadPageAIDocumentRoot(ctx, tx, pageID, lock)
	if err != nil {
		return pageInterchangeState{}, err
	}
	domain, err := loadPageContentDomainContext(ctx, tx, pageID)
	if err != nil {
		return pageInterchangeState{}, err
	}
	if domain.SourceLocale != plan.SourceLocale || locale == domain.SourceLocale {
		return pageInterchangeState{}, errs.InvalidArgument("target_locale", "Page XLIFF locale role no longer matches the owning document")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return pageInterchangeState{}, normalizePageContentBlockError(err)
	}
	metadata, exists, err := loadPageAIDocumentLocale(ctx, tx, pageID, locale, true)
	if err != nil {
		return pageInterchangeState{}, err
	}
	state := pageInterchangeState{
		snapshot: snapshot, metadata: metadata, exists: exists,
		targets: make(map[string]translation.UnitResult),
	}
	document, err := contentblock.SnapshotToLocalizedPageDocument(snapshot, locale)
	if err != nil {
		return pageInterchangeState{}, normalizePageContentBlockError(err)
	}
	state.document = document
	if !exists {
		return state, nil
	}
	state.targets, err = projectPageInterchangeTargets(
		plan, metadata, document, project, pathPresent, cloneResult, emptyInline,
	)
	if err != nil {
		return pageInterchangeState{}, err
	}
	state.targetRevision, err = pageInterchangeTargetRevision(
		ctx, tx, pageID, locale, snapshot.Document.Revision.String(), true,
	)
	if err != nil {
		return pageInterchangeState{}, err
	}
	return state, nil
}

func projectPageInterchangeTargets(
	plan *translation.ExtractionPlan,
	metadata pageAIDocumentLocale,
	document *contentv1.LocalizedPageDocument,
	project ProjectRichTextInterchangeTargets,
	pathPresent InterchangeProtoPathPresent,
	cloneResult CloneInterchangeUnitResult,
	emptyInline EmptyInterchangeTargetInline,
) (map[string]translation.UnitResult, error) {
	targets := make(map[string]translation.UnitResult)
	if metadata.Title != nil {
		targets["entity:title"] = translation.UnitResult{UnitID: "entity:title", TranslatedText: *metadata.Title}
	}
	if metadata.Summary != nil {
		targets["entity:summary"] = translation.UnitResult{UnitID: "entity:summary", TranslatedText: *metadata.Summary}
	}
	targetSource := &translation.SourceDocument{PageDocument: document}
	if metadata.Title != nil {
		targetSource.Title = *metadata.Title
	}
	targetSource.Summary = metadata.Summary
	targetPlan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "page", EntityID: plan.EntityID, SourceLocale: plan.TargetLocale, TargetLocale: plan.SourceLocale,
	}, targetSource)
	if err != nil && !errors.Is(err, translation.ErrNoTranslatableUnits) {
		return nil, err
	}
	if targetPlan != nil {
		for _, unit := range targetPlan.Units {
			targets[unit.UnitID] = translation.UnitResult{
				UnitID: unit.UnitID, TranslatedText: unit.SourceText,
				OriginalData: append([]translation.XLIFFOriginalData(nil), unit.OriginalData...),
				TargetInline: cloneResult(translation.UnitResult{TargetInline: unit.SourceInline}).TargetInline,
			}
		}
	}
	richTargets, err := projectPageRichTextInterchangeTargets(plan, document, project)
	if err != nil {
		return nil, err
	}
	for handle, target := range richTargets {
		targets[handle] = target
	}
	for _, unit := range plan.Units {
		if _, exists := targets[unit.UnitID]; exists || unit.ContainerType == translation.ContainerTypeEntity {
			continue
		}
		_, rest, _ := pageInterchangeSectionHandle(unit.UnitID)
		if strings.HasPrefix(rest, "block:") || !pageInterchangeUnitPresent(document, unit.UnitID, pathPresent) {
			continue
		}
		targets[unit.UnitID] = translation.UnitResult{
			UnitID: unit.UnitID, TranslatedText: "",
			OriginalData: append([]translation.XLIFFOriginalData(nil), unit.OriginalData...),
			TargetInline: emptyInline(unit.SourceInline),
		}
	}
	return targets, nil
}

func projectPageRichTextInterchangeTargets(
	plan *translation.ExtractionPlan,
	document *contentv1.LocalizedPageDocument,
	project ProjectRichTextInterchangeTargets,
) (map[string]translation.UnitResult, error) {
	bySection := make(map[string][]translation.Unit)
	for _, unit := range plan.Units {
		sectionID, rest, ok := pageInterchangeSectionHandle(unit.UnitID)
		if !ok || !strings.HasPrefix(rest, "block:") {
			continue
		}
		blockID, ok := pageInterchangeBlockID(rest)
		if !ok {
			return nil, errs.InvalidArgument("file_id", "Page XLIFF Rich Text stable unit is invalid")
		}
		scoped := unit
		scoped.UnitID = rest
		scoped.ContainerID = blockID
		bySection[sectionID] = append(bySection[sectionID], scoped)
	}
	targetSections := pageInterchangeLocaleSections(document.GetLocaleOverlay())
	baseSections := pageInterchangeBaseSections(document.GetBase())
	result := make(map[string]translation.UnitResult)
	for sectionID, units := range bySection {
		targetSection := targetSections[sectionID]
		if targetSection == nil || targetSection.GetRichText() == nil || targetSection.GetRichText().GetBlocks() == nil {
			continue
		}
		baseSection := baseSections[sectionID]
		if baseSection == nil || baseSection.GetRichText() == nil || baseSection.GetRichText().GetBlocks() == nil {
			return nil, errs.InternalMsg("Page Rich Text target has no matching source graph")
		}
		scopedPlan := &translation.ExtractionPlan{
			EntityType: plan.EntityType, EntityID: plan.EntityID,
			SourceLocale: plan.SourceLocale, TargetLocale: plan.TargetLocale, Units: units,
		}
		document := &contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE,
			Locale:                  plan.TargetLocale, Base: baseSection.GetRichText().GetBlocks(),
			LocaleOverlay: targetSection.GetRichText().GetBlocks(),
		}
		projected, err := project(scopedPlan, document)
		if err != nil {
			return nil, err
		}
		for handle, target := range projected {
			fullHandle := "section:" + sectionID + ":" + handle
			target.UnitID = fullHandle
			result[fullHandle] = target
		}
	}
	return result, nil
}

func pageInterchangeLocaleSections(overlay *contentv1.PageLocaleOverlay) map[string]*contentv1.PageSectionLocale {
	sections := make(map[string]*contentv1.PageSectionLocale)
	for _, section := range overlay.GetSections() {
		if section != nil {
			sections[section.GetSectionId()] = section
		}
	}
	return sections
}

func pageInterchangeBaseSections(graph *contentv1.PageSectionGraph) map[string]*contentv1.PageSection {
	sections := make(map[string]*contentv1.PageSection)
	for _, node := range graph.GetNodes() {
		if section := node.GetSection(); section != nil {
			sections[section.GetId()] = section
		}
	}
	return sections
}

func pageInterchangeBlockID(handle string) (string, bool) {
	if !strings.HasPrefix(handle, "block:") {
		return "", false
	}
	rest := strings.TrimPrefix(handle, "block:")
	index := strings.Index(rest, ":typed:")
	if index <= 0 {
		return "", false
	}
	return rest[:index], true
}

func patchPageRichTextInterchange(
	candidate *contentv1.LocalizedPageDocument,
	source *contentv1.LocalizedPageDocument,
	current *contentv1.LocalizedPageDocument,
	plan *translation.ExtractionPlan,
	imported map[string]translation.UnitResult,
	mode managev1.TranslationInterchangeMode,
	build BuildRichTextInterchangePatch,
) error {
	bySection := make(map[string]map[string]translation.UnitResult)
	for handle, target := range imported {
		sectionID, rest, ok := pageInterchangeSectionHandle(handle)
		if !ok || !strings.HasPrefix(rest, "block:") {
			continue
		}
		if bySection[sectionID] == nil {
			bySection[sectionID] = make(map[string]translation.UnitResult)
		}
		target.UnitID = rest
		bySection[sectionID][rest] = target
	}
	if len(bySection) == 0 {
		return nil
	}
	sourceSections := pageInterchangeLocaleSections(source.GetLocaleOverlay())
	currentSections := pageInterchangeLocaleSections(current.GetLocaleOverlay())
	candidateSections := pageInterchangeLocaleSections(candidate.GetLocaleOverlay())
	baseSections := pageInterchangeBaseSections(source.GetBase())
	for sectionID, targets := range bySection {
		base := baseSections[sectionID]
		sourceSection := sourceSections[sectionID]
		candidateSection := candidateSections[sectionID]
		if base == nil || base.GetRichText() == nil || sourceSection == nil ||
			sourceSection.GetRichText() == nil || sourceSection.GetRichText().GetBlocks() == nil ||
			candidateSection == nil || candidateSection.GetRichText() == nil {
			return fmt.Errorf("page Rich Text interchange section %q is outside the source graph", sectionID)
		}
		units := make([]translation.Unit, 0, len(targets))
		for _, unit := range plan.Units {
			candidateSectionID, rest, ok := pageInterchangeSectionHandle(unit.UnitID)
			if !ok || candidateSectionID != sectionID || !strings.HasPrefix(rest, "block:") {
				continue
			}
			blockID, ok := pageInterchangeBlockID(rest)
			if !ok {
				return fmt.Errorf("page Rich Text interchange unit %q is invalid", unit.UnitID)
			}
			unit.UnitID = rest
			unit.ContainerID = blockID
			units = append(units, unit)
		}
		scopedPlan := &translation.ExtractionPlan{
			EntityType: plan.EntityType, EntityID: plan.EntityID,
			SourceLocale: plan.SourceLocale, TargetLocale: plan.TargetLocale, Units: units,
		}
		currentOverlay := &contentv1.RichTextLocaleOverlay{Locale: plan.TargetLocale}
		if mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
			if section := currentSections[sectionID]; section != nil && section.GetRichText() != nil && section.GetRichText().GetBlocks() != nil {
				currentOverlay = section.GetRichText().GetBlocks()
			}
		}
		sourceDocument := &contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: source.GetBlockCatalogFingerprint(),
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE,
			Locale:                  plan.SourceLocale, Base: base.GetRichText().GetBlocks(),
			LocaleOverlay: sourceSection.GetRichText().GetBlocks(),
		}
		currentDocument := &contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: current.GetBlockCatalogFingerprint(),
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE,
			Locale:                  plan.TargetLocale, Base: base.GetRichText().GetBlocks(), LocaleOverlay: currentOverlay,
		}
		patch, err := build(scopedPlan, sourceDocument, currentDocument, targets)
		if err != nil {
			return err
		}
		candidateSection.GetRichText().Blocks = patch
	}
	return nil
}

func patchPageContainerInterchange(
	candidate *contentv1.LocalizedPageDocument,
	current *contentv1.LocalizedPageDocument,
	handles []string,
	mode managev1.TranslationInterchangeMode,
	copyPath CopyInterchangeProtoPath,
) error {
	paths := make(map[string][][]string)
	for _, handle := range handles {
		sectionID, rest, ok := pageInterchangeSectionHandle(handle)
		if !ok || strings.HasPrefix(rest, "block:") {
			continue
		}
		path, ok := pageInterchangeContainerPath(rest, pageInterchangeLocaleSections(candidate.GetLocaleOverlay())[sectionID])
		if !ok {
			return fmt.Errorf("page interchange unit %q has an invalid section path", handle)
		}
		paths[sectionID] = append(paths[sectionID], path)
	}
	if len(paths) == 0 {
		return nil
	}
	currentSections := pageInterchangeLocaleSections(current.GetLocaleOverlay())
	for index, sourceSection := range candidate.GetLocaleOverlay().GetSections() {
		sectionPaths := paths[sourceSection.GetSectionId()]
		if len(sectionPaths) == 0 {
			continue
		}
		destination := &contentv1.PageSectionLocale{SectionId: sourceSection.GetSectionId()}
		if mode != managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE {
			if existing := currentSections[sourceSection.GetSectionId()]; existing != nil {
				destination = proto.Clone(existing).(*contentv1.PageSectionLocale)
			}
		}
		for _, path := range sectionPaths {
			if err := copyPath(destination.ProtoReflect(), sourceSection.ProtoReflect(), path); err != nil {
				return fmt.Errorf("copy Page interchange section %q: %w", sourceSection.GetSectionId(), err)
			}
		}
		candidate.LocaleOverlay.Sections[index] = destination
	}
	return nil
}

func pageInterchangeReplacementDeletes(
	current *contentv1.LocalizedPageDocument,
	replacement *contentv1.LocalizedPageDocument,
) []*contentv1.PageSectionLocaleMutation {
	currentSections := pageInterchangeLocaleSections(current.GetLocaleOverlay())
	replacementSections := pageInterchangeLocaleSections(replacement.GetLocaleOverlay())
	mutations := make([]*contentv1.PageSectionLocaleMutation, 0)
	sectionIDs := make([]string, 0, len(currentSections))
	for sectionID := range currentSections {
		sectionIDs = append(sectionIDs, sectionID)
	}
	sort.Strings(sectionIDs)
	for _, sectionID := range sectionIDs {
		currentSection := currentSections[sectionID]
		replacementSection := replacementSections[sectionID]
		if replacementSection == nil {
			mutations = append(mutations, &contentv1.PageSectionLocaleMutation{
				Operation: &contentv1.PageSectionLocaleMutation_Delete{
					Delete: &contentv1.DeletePageSectionLocale{SectionId: sectionID},
				},
			})
			continue
		}
		currentBlocks := currentSection.GetRichText().GetBlocks()
		if currentBlocks == nil {
			continue
		}
		replacementBlockIDs := make(map[string]struct{})
		if replacementBlocks := replacementSection.GetRichText().GetBlocks(); replacementBlocks != nil {
			for _, block := range replacementBlocks.GetBlocks() {
				replacementBlockIDs[block.GetBlockId()] = struct{}{}
			}
		}
		for _, block := range currentBlocks.GetBlocks() {
			if _, retained := replacementBlockIDs[block.GetBlockId()]; retained {
				continue
			}
			mutations = append(mutations, &contentv1.PageSectionLocaleMutation{
				Operation: &contentv1.PageSectionLocaleMutation_MutateRichTextBlock{
					MutateRichTextBlock: &contentv1.MutatePageRichTextBlockLocale{
						SectionId: sectionID,
						Mutation: &contentv1.RichTextBlockLocaleMutation{
							Operation: &contentv1.RichTextBlockLocaleMutation_Delete{
								Delete: &contentv1.DeleteRichTextBlockLocale{BlockId: block.GetBlockId()},
							},
						},
					},
				},
			})
		}
	}
	return mutations
}

func pageInterchangeContainerPath(rest string, section *contentv1.PageSectionLocale) ([]string, bool) {
	if strings.HasPrefix(rest, "typed:") {
		path := strings.Split(strings.TrimPrefix(rest, "typed:"), "/")
		return path, len(path) != 0 && path[0] != ""
	}
	if !strings.HasPrefix(rest, "immersive-unit:") || section == nil || section.GetImmersiveScene() == nil {
		return nil, false
	}
	unitRest := strings.TrimPrefix(rest, "immersive-unit:")
	marker := strings.Index(unitRest, ":typed:")
	if marker <= 0 {
		return nil, false
	}
	unitID := unitRest[:marker]
	for _, unit := range section.GetImmersiveScene().GetUnits() {
		if unit.GetUnitId() == unitID {
			path := []string{"immersive_scene", "units", unitID, "props"}
			path = append(path, strings.Split(unitRest[marker+len(":typed:"):], "/")...)
			return path, true
		}
	}
	return nil, false
}

func pageInterchangeUnitPresent(
	document *contentv1.LocalizedPageDocument,
	handle string,
	pathPresent InterchangeProtoPathPresent,
) bool {
	sectionID, rest, ok := pageInterchangeSectionHandle(handle)
	if !ok {
		return false
	}
	var section *contentv1.PageSectionLocale
	for _, candidate := range document.GetLocaleOverlay().GetSections() {
		if candidate.GetSectionId() == sectionID {
			section = candidate
			break
		}
	}
	if section == nil {
		return false
	}
	if strings.HasPrefix(rest, "block:") {
		blockRest := strings.TrimPrefix(rest, "block:")
		index := strings.Index(blockRest, ":typed:")
		if index <= 0 || section.GetRichText() == nil || section.GetRichText().GetBlocks() == nil {
			return false
		}
		blockID := blockRest[:index]
		for _, block := range section.GetRichText().GetBlocks().GetBlocks() {
			if block.GetBlockId() == blockID {
				return pathPresent(
					block.ProtoReflect(), strings.Split(blockRest[index+len(":typed:"):], "/"),
				)
			}
		}
		return false
	}
	if strings.HasPrefix(rest, "immersive-unit:") {
		unitRest := strings.TrimPrefix(rest, "immersive-unit:")
		index := strings.Index(unitRest, ":typed:")
		if index <= 0 || section.GetImmersiveScene() == nil {
			return false
		}
		unitID := unitRest[:index]
		for _, unit := range section.GetImmersiveScene().GetUnits() {
			if unit.GetUnitId() == unitID && unit.GetProps() != nil {
				return pathPresent(
					unit.GetProps().ProtoReflect(), strings.Split(unitRest[index+len(":typed:"):], "/"),
				)
			}
		}
		return false
	}
	if strings.HasPrefix(rest, "typed:") {
		return pathPresent(
			section.ProtoReflect(), strings.Split(strings.TrimPrefix(rest, "typed:"), "/"),
		)
	}
	return false
}

func pageInterchangeSectionHandle(handle string) (string, string, bool) {
	if !strings.HasPrefix(handle, "section:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(handle, "section:")
	index := strings.IndexByte(rest, ':')
	if index <= 0 || index == len(rest)-1 {
		return "", "", false
	}
	return rest[:index], rest[index+1:], true
}

func pageInterchangeSelectedSectionIDs(handles []string) map[string]struct{} {
	selected := make(map[string]struct{})
	for _, handle := range handles {
		sectionID, _, ok := pageInterchangeSectionHandle(handle)
		if ok {
			selected[sectionID] = struct{}{}
		}
	}
	return selected
}

func setExactPageInterchangeMetadata(candidate *translation.Candidate, desired map[string]translation.UnitResult) {
	if target, ok := desired["entity:title"]; ok {
		candidate.Title = pageInterchangeStringPointer(target.TranslatedText)
	} else {
		candidate.Title = nil
	}
	if target, ok := desired["entity:summary"]; ok {
		candidate.Summary = pageInterchangeStringPointer(target.TranslatedText)
	} else {
		candidate.Summary = nil
	}
}

func pageInterchangeMetadataPatch(plan *translation.ExtractionPlan, desired map[string]translation.UnitResult) AIDocumentMetadataPatch {
	patch := AIDocumentMetadataPatch{EnsureLocale: true}
	for _, unit := range plan.Units {
		switch unit.UnitID {
		case "entity:title":
			patch.SetTitle = true
			if target, ok := desired[unit.UnitID]; ok {
				patch.Title = pageInterchangeStringPointer(target.TranslatedText)
			}
		case "entity:summary":
			patch.SetSummary = true
			if target, ok := desired[unit.UnitID]; ok {
				patch.Summary = pageInterchangeStringPointer(target.TranslatedText)
			}
		}
	}
	return patch
}

func pageInterchangeTargetRevision(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	locale string,
	documentRevision string,
	exists bool,
) (string, error) {
	if !exists {
		return translation.DeriveTargetRevision(translation.TargetRevisionFacts{})
	}
	var row struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	result := tx.WithContext(ctx).Table("page_translation").Select("updated_at").
		Where("entity_id = ? AND locale = ?", pageID, locale).Take(&row)
	if result.Error != nil {
		return "", errs.Internal(result.Error)
	}
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &row.UpdatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func changedPageInterchangeHandles(
	current map[string]translation.UnitResult,
	incoming map[string]translation.UnitResult,
	handles []string,
) []string {
	affected := make([]string, 0, len(handles))
	for _, handle := range handles {
		if !reflect.DeepEqual(current[handle], incoming[handle]) {
			affected = append(affected, handle)
		}
	}
	sort.Strings(affected)
	return affected
}

func pageInterchangeStringPointer(value string) *string { return &value }
