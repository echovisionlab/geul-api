package translation

import (
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// BuildProviderTargetRichTextBatch converts one accepted provider candidate
// into a target-only Content Block batch. Source Blocks removed or replaced
// with another kind since the request was made are ignored; the owning domain
// retains lock, CAS, persistence, and Audit authority.
func BuildProviderTargetRichTextBatch(
	snapshot contentblock.Snapshot,
	profile contentv1.RichTextProfile,
	targetLocale string,
	candidate *Candidate,
) (contentblock.Batch, error) {
	if candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return contentblock.Batch{}, errors.New("provider Rich Text target candidate is required")
	}
	targetLocale = strings.TrimSpace(targetLocale)
	if targetLocale == "" {
		return contentblock.Batch{}, errors.New("provider Rich Text target locale is required")
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != targetLocale {
		return contentblock.Batch{}, fmt.Errorf(
			"provider Rich Text overlay locale %q does not match target locale %q",
			candidate.ContentBlockLocaleOverlay.GetLocale(),
			targetLocale,
		)
	}

	mutations := candidate.RichTextLocaleMutations()
	if candidate.providerUnitPatch != nil {
		var err error
		mutations, err = buildProviderUnitPatchMutations(snapshot, targetLocale, candidate.providerUnitPatch)
		if err != nil {
			return contentblock.Batch{}, err
		}
	}
	if len(mutations) == 0 {
		return contentblock.Batch{
			DocumentID:       snapshot.Document.ID,
			ExpectedRevision: snapshot.Document.Revision,
		}, nil
	}
	protoBatch := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 profile,
		ExpectedRevision:        snapshot.Document.Revision.String(),
	}
	protoBatch.LocaleMutationGroups = []*contentv1.RichTextLocaleMutationGroup{{
		Locale: targetLocale, Mutations: mutations,
	}}
	batch, err := contentblock.BatchFromRichTextSystemProto(snapshot.Document.ID, protoBatch)
	if err != nil {
		return contentblock.Batch{}, err
	}
	if len(batch.LocaleGroups) == 0 {
		return batch, nil
	}

	currentKinds := make(map[uuid.UUID]string, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		currentKinds[block.ID] = block.Kind
	}
	group := &batch.LocaleGroups[0]
	upserts := group.Upserts[:0]
	for _, update := range group.Upserts {
		if currentKind, exists := currentKinds[update.BlockID]; exists && currentKind == update.ExpectedKind {
			upserts = append(upserts, update)
		}
	}
	group.Upserts = upserts
	deletes := group.Deletes[:0]
	for _, blockID := range group.Deletes {
		if _, exists := currentKinds[blockID]; exists {
			deletes = append(deletes, blockID)
		}
	}
	group.Deletes = deletes
	if len(group.Upserts) == 0 && len(group.Deletes) == 0 {
		batch.LocaleGroups = nil
	}
	return batch, nil
}

func buildProviderUnitPatchMutations(
	snapshot contentblock.Snapshot,
	targetLocale string,
	patch *ProviderUnitPatch,
) ([]*contentv1.RichTextBlockLocaleMutation, error) {
	if patch == nil {
		return nil, nil
	}
	sourceDocument, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, snapshot.SourceLocale)
	if err != nil {
		return nil, err
	}
	targetDocument, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, targetLocale)
	if err != nil {
		return nil, err
	}
	overlay, err := BuildProviderTargetRichTextOverlay(sourceDocument, targetDocument, patch)
	if err != nil {
		return nil, err
	}
	mutations := make([]*contentv1.RichTextBlockLocaleMutation, 0, len(overlay.GetBlocks()))
	for _, block := range overlay.GetBlocks() {
		mutations = append(mutations, &contentv1.RichTextBlockLocaleMutation{
			Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertRichTextBlockLocale{Block: block},
			},
		})
	}
	return mutations, nil
}

// BuildProviderTargetRichTextOverlay patches only surviving requested units
// onto the current sparse target. Current-only source units are omitted and
// unrelated target units remain byte-for-byte semantically present.
func BuildProviderTargetRichTextOverlay(
	sourceDocument *contentv1.LocalizedRichTextDocument,
	targetDocument *contentv1.LocalizedRichTextDocument,
	patch *ProviderUnitPatch,
) (*contentv1.RichTextLocaleOverlay, error) {
	if sourceDocument == nil || sourceDocument.GetLocaleOverlay() == nil ||
		targetDocument == nil || targetDocument.GetLocaleOverlay() == nil || patch == nil {
		return nil, errors.New("provider Rich Text source, target, and unit patch are required")
	}
	sourceByID := richTextBlocksByID(sourceDocument.GetLocaleOverlay())
	targetByID := richTextBlocksByID(targetDocument.GetLocaleOverlay())
	baseByID := richTextBaseBlocksByID(sourceDocument.GetBase())
	unitsByBlock := make(map[string][]Unit)
	for _, unit := range patch.Units {
		if unit.ContainerType != ContainerTypeBlock || strings.TrimSpace(unit.ContainerID) == "" {
			continue
		}
		if _, translated := patch.Results[unit.UnitID]; !translated {
			continue
		}
		unitsByBlock[unit.ContainerID] = append(unitsByBlock[unit.ContainerID], unit)
	}

	overlay := &contentv1.RichTextLocaleOverlay{Locale: targetDocument.GetLocale()}
	for _, sourceBlock := range sourceDocument.GetLocaleOverlay().GetBlocks() {
		blockID := sourceBlock.GetBlockId()
		requested := unitsByBlock[blockID]
		baseBlock := baseByID[blockID]
		if len(requested) == 0 || sourceByID[blockID] == nil || baseBlock == nil {
			continue
		}
		requested, err := survivingProviderBlockUnits(sourceBlock, requested, baseBlock)
		if err != nil {
			return nil, err
		}
		if len(requested) == 0 {
			continue
		}
		patched, err := buildProviderPatchedRichTextBlock(
			sourceBlock, targetByID[blockID], baseBlock, requested, patch.Results,
		)
		if err != nil {
			return nil, err
		}
		overlay.Blocks = append(overlay.Blocks, patched)
	}
	return overlay, nil
}

func survivingProviderBlockUnits(
	source *contentv1.RichTextBlockLocale,
	requested []Unit,
	currentBase *contentv1.RichTextBlock,
) ([]Unit, error) {
	if source == nil || currentBase == nil || len(requested) == 0 {
		return nil, nil
	}
	prefix, ok := richTextUnitPrefix(requested[0].UnitID)
	if !ok {
		return nil, fmt.Errorf("provider Rich Text unit %q has no typed stable handle", requested[0].UnitID)
	}
	current, err := ExtractRichTextUnits(source, RichTextUnitScope{
		EntityType:   requested[0].EntityType,
		EntityID:     requested[0].EntityID,
		SourceLocale: requested[0].SourceLocale,
		ContainerID:  requested[0].ContainerID,
		UnitPrefix:   prefix,
		PathPrefix:   prefix,
	})
	if err != nil {
		return nil, err
	}
	currentIDs := make(map[string]struct{}, len(current))
	for _, unit := range current {
		currentIDs[unit.UnitID] = struct{}{}
	}
	surviving := make([]Unit, 0, len(requested))
	for _, unit := range requested {
		if _, exists := currentIDs[unit.UnitID]; exists && providerUnitExistsInCurrentBase(unit, currentBase) {
			surviving = append(surviving, unit)
		}
	}
	return surviving, nil
}

func buildProviderPatchedRichTextBlock(
	source *contentv1.RichTextBlockLocale,
	target *contentv1.RichTextBlockLocale,
	currentBase *contentv1.RichTextBlock,
	requested []Unit,
	providerResults map[string]UnitResult,
) (*contentv1.RichTextBlockLocale, error) {
	block := proto.Clone(source).(*contentv1.RichTextBlockLocale)

	allowedUnitIDs := make(map[string]struct{}, len(requested))
	for _, unit := range requested {
		allowedUnitIDs[unit.UnitID] = struct{}{}
	}
	if target != nil {
		prefix, ok := richTextUnitPrefix(requested[0].UnitID)
		if !ok {
			return nil, fmt.Errorf("provider Rich Text unit %q has no typed stable handle", requested[0].UnitID)
		}
		targetUnits, err := ExtractRichTextUnits(target, RichTextUnitScope{
			EntityType: requested[0].EntityType, EntityID: requested[0].EntityID,
			SourceLocale: requested[0].SourceLocale, ContainerID: requested[0].ContainerID,
			UnitPrefix: prefix, PathPrefix: prefix,
		})
		if err != nil {
			return nil, err
		}
		targetUnits = filterProviderUnitsByCurrentBase(targetUnits, currentBase)
		targetResults := make(map[string]UnitResult, len(targetUnits))
		for _, unit := range targetUnits {
			allowedUnitIDs[unit.UnitID] = struct{}{}
			targetResults[unit.UnitID] = UnitResult{
				UnitID: unit.UnitID, TranslatedText: unit.SourceText,
				OriginalData: unit.OriginalData, TargetInline: unit.SourceInline,
			}
		}
		if err := ApplyRichTextResults(block, prefix, targetResults); err != nil {
			return nil, err
		}
	}
	prefix, ok := richTextUnitPrefix(requested[0].UnitID)
	if !ok {
		return nil, fmt.Errorf("provider Rich Text unit %q has no typed stable handle", requested[0].UnitID)
	}
	requestedResults := make(map[string]UnitResult, len(requested))
	for _, unit := range requested {
		if result, exists := providerResults[unit.UnitID]; exists {
			requestedResults[unit.UnitID] = result
		}
	}
	if err := ApplyRichTextResults(block, prefix, requestedResults); err != nil {
		return nil, err
	}
	clearUnselectedRichTextSemanticSegments(block, prefix, allowedUnitIDs)
	clearUnselectedRichTextTranslationStrings(block, prefix, allowedUnitIDs)
	pruneProviderTableCells(block, requested[0].UnitID, allowedUnitIDs)
	return block, nil
}

func richTextBlocksByID(overlay *contentv1.RichTextLocaleOverlay) map[string]*contentv1.RichTextBlockLocale {
	blocks := make(map[string]*contentv1.RichTextBlockLocale)
	for _, block := range overlay.GetBlocks() {
		if block != nil {
			blocks[block.GetBlockId()] = block
		}
	}
	return blocks
}

func richTextBaseBlocksByID(graph *contentv1.RichTextBlockGraph) map[string]*contentv1.RichTextBlock {
	blocks := make(map[string]*contentv1.RichTextBlock)
	for _, node := range graph.GetNodes() {
		if block := node.GetBlock(); block != nil && strings.TrimSpace(block.GetId()) != "" {
			blocks[block.GetId()] = block
		}
	}
	return blocks
}

func filterProviderUnitsByCurrentBase(units []Unit, currentBase *contentv1.RichTextBlock) []Unit {
	filtered := units[:0]
	for _, unit := range units {
		if providerUnitExistsInCurrentBase(unit, currentBase) {
			filtered = append(filtered, unit)
		}
	}
	return filtered
}

func providerUnitExistsInCurrentBase(unit Unit, currentBase *contentv1.RichTextBlock) bool {
	if currentBase == nil {
		return false
	}
	_, path, ok := strings.Cut(unit.UnitID, ":typed:")
	if !ok || !strings.HasPrefix(path, "table/content/rows/") {
		return true
	}
	segments := strings.Split(path, "/")
	if len(segments) != 7 || segments[0] != "table" || segments[1] != "content" ||
		segments[2] != "rows" || segments[4] != "cells" || segments[6] != "content" {
		return false
	}
	rowID, cellID := segments[3], segments[5]
	for _, row := range currentBase.GetTable().GetContent().GetRows() {
		if row.GetId() != rowID {
			continue
		}
		for _, cell := range row.GetCells() {
			if cell.GetId() == cellID {
				return true
			}
		}
		return false
	}
	return false
}

func clearUnselectedRichTextTranslationStrings(
	block *contentv1.RichTextBlockLocale,
	prefix string,
	allowed map[string]struct{},
) {
	walkMutableRichTextTranslationStrings(block.ProtoReflect(), nil, func(
		path []string,
		message protoreflect.Message,
		field protoreflect.FieldDescriptor,
	) {
		if _, keep := allowed[richTextUnitID(prefix, path)]; !keep {
			message.Clear(field)
		}
	})
}

func clearUnselectedRichTextSemanticSegments(
	block *contentv1.RichTextBlockLocale,
	prefix string,
	allowed map[string]struct{},
) {
	for _, segment := range richTextSemanticSegments(block) {
		if _, keep := allowed[richTextUnitID(prefix, segment.path)]; !keep {
			segment.replace(nil)
		}
	}
}

func richTextUnitPrefix(unitID string) (string, bool) {
	prefix, _, ok := strings.Cut(unitID, ":typed:")
	return prefix, ok && strings.TrimSpace(prefix) != ""
}

func pruneProviderTableCells(
	block *contentv1.RichTextBlockLocale,
	unitID string,
	allowed map[string]struct{},
) {
	table := block.GetTable().GetContent()
	if table == nil {
		return
	}
	prefix, ok := richTextUnitPrefix(unitID)
	if !ok {
		return
	}
	rows := table.Rows[:0]
	for _, row := range table.GetRows() {
		cells := row.Cells[:0]
		for _, cell := range row.GetCells() {
			cellUnitID := richTextUnitID(prefix, []string{
				"table", "content", "rows", row.GetRowId(), "cells", cell.GetCellId(), "content",
			})
			if _, keep := allowed[cellUnitID]; keep {
				cells = append(cells, cell)
			}
		}
		row.Cells = cells
		if len(cells) != 0 {
			rows = append(rows, row)
		}
	}
	table.Rows = rows
}
