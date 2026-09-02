package aidocumentadapter

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Compile applies Page section and nested Rich Text operations to one clone,
// then lets the generated Page flattener validate and produce one Store batch.
func (c *PageCodec) Compile(
	documentID uuid.UUID,
	document *contentv1.LocalizedPageDocument,
	role core.LocaleRole,
	expected core.Revision,
	contributor uuid.UUID,
	operations []core.Operation,
) (contentblock.Batch, []core.OperationIssue, error) {
	if documentID == uuid.Nil || contributor == uuid.Nil {
		return contentblock.Batch{}, nil, errors.New("document and contributor UUIDs are required")
	}
	working, ok := proto.Clone(document).(*contentv1.LocalizedPageDocument)
	if !ok || working == nil || working.GetBase() == nil || working.GetLocaleOverlay() == nil {
		return contentblock.Batch{}, nil, errors.New("localized Page document is required")
	}
	deleted := make(map[string]struct{})
	for index, operation := range operations {
		if operation.Kind == core.OperationCreateTranslation || operation.Kind == core.OperationDeleteTranslation {
			continue
		}
		if operation.InsertRelationItem != nil || operation.DeleteRelationItem != nil || operation.MoveRelationItem != nil {
			return contentblock.Batch{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Message: "Page sections do not expose generic relations"}}, nil
		}
		if err := c.applyPageOperation(working, role, operation, deleted); err != nil {
			return contentblock.Batch{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Message: err.Error()}}, nil
		}
	}
	mutation := &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        string(expected), ContributorMemberIds: []string{contributor.String()},
	}
	if role == core.LocaleRoleSource {
		for _, node := range working.GetBase().GetNodes() {
			sectionUpsert := proto.Clone(node).(*contentv1.PageSectionNode)
			if sectionUpsert.GetSection().GetRichText() != nil {
				sectionUpsert.GetSection().GetRichText().Blocks = &contentv1.RichTextBlockGraph{}
			}
			mutation.BaseMutations = append(mutation.BaseMutations, &contentv1.PageSectionMutation{Operation: &contentv1.PageSectionMutation_Upsert{Upsert: &contentv1.UpsertPageSection{Node: sectionUpsert}}})
			if node.GetSection().GetRichText() != nil {
				for _, richNode := range node.GetSection().GetRichText().GetBlocks().GetNodes() {
					mutation.BaseMutations = append(mutation.BaseMutations, pageRichTextBaseMutation(node.GetSection().GetId(), richNode))
				}
			}
		}
		for sectionID := range deleted {
			if _, _, wasSection := findPageNode(document, sectionID); wasSection {
				mutation.BaseMutations = append(mutation.BaseMutations, &contentv1.PageSectionMutation{Operation: &contentv1.PageSectionMutation_Delete{Delete: &contentv1.DeletePageSection{SectionId: sectionID}}})
			}
		}
		for _, originalSection := range document.GetBase().GetNodes() {
			if originalSection.GetSection().GetRichText() == nil {
				continue
			}
			_, currentSection, sectionExists := findPageNode(working, originalSection.GetSection().GetId())
			if !sectionExists {
				continue
			}
			for _, originalNode := range originalSection.GetSection().GetRichText().GetBlocks().GetNodes() {
				if !pageRichTextBlockExists(currentSection, originalNode.GetBlock().GetId()) {
					mutation.BaseMutations = append(mutation.BaseMutations, pageRichTextBaseDelete(originalSection.GetSection().GetId(), originalNode.GetBlock().GetId()))
				}
			}
		}
	}
	group := &contentv1.PageLocaleMutationGroup{Locale: working.GetLocale()}
	for _, section := range working.GetLocaleOverlay().GetSections() {
		sectionUpsert := proto.Clone(section).(*contentv1.PageSectionLocale)
		if sectionUpsert.GetRichText() != nil {
			sectionUpsert.GetRichText().Blocks = &contentv1.RichTextLocaleOverlay{}
		}
		group.Mutations = append(group.Mutations, &contentv1.PageSectionLocaleMutation{Operation: &contentv1.PageSectionLocaleMutation_Upsert{Upsert: &contentv1.UpsertPageSectionLocale{Section: sectionUpsert}}})
		if section.GetRichText() != nil {
			for _, block := range section.GetRichText().GetBlocks().GetBlocks() {
				group.Mutations = append(group.Mutations, pageRichTextLocaleMutation(section.GetSectionId(), block))
			}
		}
	}
	if len(group.Mutations) != 0 {
		mutation.LocaleMutationGroups = append(mutation.LocaleMutationGroups, group)
	}
	if len(mutation.BaseMutations) == 0 && len(mutation.LocaleMutationGroups) == 0 {
		revision, err := uuid.Parse(string(expected))
		if err != nil || revision == uuid.Nil {
			return contentblock.Batch{}, nil, errors.New("expected revision must be a UUID")
		}
		return contentblock.Batch{DocumentID: documentID, ExpectedRevision: revision, ContributorMemberIDs: []uuid.UUID{contributor}}, nil, nil
	}
	affected := c.pageAffectedLocaleValues(working, operations)
	batch, err := contentblock.BatchFromPageProtoWithAffectedLocaleValues(documentID, mutation, working.GetLocale(), affected)
	if err != nil {
		return contentblock.Batch{}, []core.OperationIssue{{Operation: -1, Code: core.IssueInvalidOperation, Message: err.Error()}}, nil
	}
	return batch, nil, nil
}

func (c *PageCodec) pageAffectedLocaleValues(document *contentv1.LocalizedPageDocument, operations []core.Operation) []*managev1.AIDocumentFieldTarget {
	targets := make([]*managev1.AIDocumentFieldTarget, 0)
	for _, operation := range operations {
		if operation.SetField != nil {
			target := operation.SetField.Target
			if target.Field == pageSectionLocaleField || c.pageRichTextFieldIsLocale(document, target) {
				targets = append(targets, fieldTargetToProto(target))
			}
		}
		if operation.InsertBlock == nil || c.baseCases[operation.InsertBlock.Kind] != nil || operation.InsertBlock.Kind == pageColumnBlockKind {
			continue
		}
		for _, rule := range c.rich.Catalog().Fields {
			if rule.BlockKind == operation.InsertBlock.Kind && rule.Field == "content" && rule.Ownership == core.FieldOwnershipLocale {
				targets = append(targets, fieldTargetToProto(core.FieldTarget{Block: operation.InsertBlock.Block, Field: "content"}))
				break
			}
		}
	}
	sort.Slice(targets, func(left, right int) bool {
		return pageLocaleTargetKey(targets[left]) < pageLocaleTargetKey(targets[right])
	})
	result := targets[:0]
	previous := ""
	for _, target := range targets {
		key := pageLocaleTargetKey(target)
		if key == previous {
			continue
		}
		result = append(result, target)
		previous = key
	}
	return result
}

func (c *PageCodec) pageRichTextFieldIsLocale(document *contentv1.LocalizedPageDocument, target core.FieldTarget) bool {
	var kind core.BlockKind
	for _, section := range document.GetBase().GetNodes() {
		for _, node := range section.GetSection().GetRichText().GetBlocks().GetNodes() {
			if node.GetBlock().GetId() != string(target.Block) {
				continue
			}
			resolved, _, err := c.rich.blockMessage(node.GetBlock().ProtoReflect())
			if err == nil {
				kind = resolved
			}
		}
	}
	if kind == "" {
		return false
	}
	for _, rule := range c.rich.Catalog().Fields {
		if rule.BlockKind == kind && rule.Field == target.Field {
			return rule.Ownership == core.FieldOwnershipLocale
		}
	}
	return false
}

func pageLocaleTargetKey(target *managev1.AIDocumentFieldTarget) string {
	var result strings.Builder
	result.WriteString(target.GetBlockHandle())
	result.WriteByte(0)
	result.WriteString(target.GetFieldHandle())
	for _, segment := range target.GetPath() {
		result.WriteByte(0)
		switch selector := segment.GetSelector().(type) {
		case *managev1.AIDocumentFieldPathSegment_FieldHandle:
			result.WriteByte('f')
			result.WriteString(selector.FieldHandle)
		case *managev1.AIDocumentFieldPathSegment_ItemHandle:
			result.WriteByte('i')
			result.WriteString(selector.ItemHandle)
		}
	}
	return result.String()
}

func pageRichTextBaseMutation(sectionID string, node *contentv1.RichTextBlockNode) *contentv1.PageSectionMutation {
	return &contentv1.PageSectionMutation{Operation: &contentv1.PageSectionMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlock{
		SectionId: sectionID,
		Mutation:  &contentv1.RichTextBlockMutation{Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{Node: node}}},
	}}}
}

func pageRichTextBaseDelete(sectionID, blockID string) *contentv1.PageSectionMutation {
	return &contentv1.PageSectionMutation{Operation: &contentv1.PageSectionMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlock{
		SectionId: sectionID,
		Mutation:  &contentv1.RichTextBlockMutation{Operation: &contentv1.RichTextBlockMutation_Delete{Delete: &contentv1.DeleteRichTextBlock{BlockId: blockID}}},
	}}}
}

func pageRichTextLocaleMutation(sectionID string, block *contentv1.RichTextBlockLocale) *contentv1.PageSectionLocaleMutation {
	return &contentv1.PageSectionLocaleMutation{Operation: &contentv1.PageSectionLocaleMutation_MutateRichTextBlock{MutateRichTextBlock: &contentv1.MutatePageRichTextBlockLocale{
		SectionId: sectionID,
		Mutation:  &contentv1.RichTextBlockLocaleMutation{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: block}}},
	}}}
}

func pageRichTextBlockExists(section *contentv1.PageSectionNode, blockID string) bool {
	for _, node := range section.GetSection().GetRichText().GetBlocks().GetNodes() {
		if node.GetBlock().GetId() == blockID {
			return true
		}
	}
	return false
}

func (c *PageCodec) applyPageOperation(document *contentv1.LocalizedPageDocument, role core.LocaleRole, operation core.Operation, deleted map[string]struct{}) error {
	if role != core.LocaleRoleSource {
		switch operation.Kind {
		case core.OperationInsertBlock, core.OperationDeleteBlock, core.OperationMoveBlock, core.OperationReplaceBlockKind:
			return errors.New("target Page locale cannot alter document topology")
		case core.OperationAttachFile, core.OperationDetachFile:
			return errors.New("target Page locale cannot alter shared Files")
		case core.OperationSetField, core.OperationUnsetField:
			var target core.FieldTarget
			if operation.SetField != nil {
				target = operation.SetField.Target
			} else {
				target = operation.UnsetField.Target
			}
			_, _, pageSection := findPageNode(document, string(target.Block))
			if (pageSection && target.Field != pageSectionLocaleField) || (!pageSection && !c.pageRichTextFieldIsLocale(document, target)) {
				return errors.New("target Page locale accepts only locale scalar fields")
			}
		}
	}
	targetBlock := pageOperationBlock(operation)
	if operation.Kind == core.OperationInsertBlock && operation.InsertBlock.Kind == pageColumnBlockKind {
		return insertPageColumn(document, operation.InsertBlock)
	}
	if _, _, _, isColumn := findPageColumn(document, string(targetBlock)); isColumn {
		return applyPageColumnOperation(document, operation)
	}
	if operation.Kind == core.OperationInsertBlock && c.baseCases[operation.InsertBlock.Kind] == nil {
		if section, rich, ok := c.findRichTextContainer(document, targetBlock, operation); ok {
			return c.applyRichTextOperation(document, section, rich, operation)
		}
		return fmt.Errorf("rich Text parent %q does not exist in a Page rich-text section", targetBlock)
	}
	if targetBlock != "" {
		if _, _, ok := findPageNode(document, string(targetBlock)); !ok {
			if section, rich, ok := c.findRichTextContainer(document, targetBlock, operation); ok {
				return c.applyRichTextOperation(document, section, rich, operation)
			}
		}
	}
	switch operation.Kind {
	case core.OperationSetField:
		return c.setPageField(document, role, operation.SetField.Target, operation.SetField.Value)
	case core.OperationUnsetField:
		return c.unsetPageField(document, operation.UnsetField.Target)
	case core.OperationAttachFile:
		return c.setPageFile(document, operation.AttachFile.Target, operation.AttachFile.File)
	case core.OperationDetachFile:
		return c.setPageFile(document, operation.DetachFile.Target, "")
	case core.OperationInsertBlock:
		return c.insertPageSection(document, operation.InsertBlock)
	case core.OperationDeleteBlock:
		return deletePageSection(document, string(operation.DeleteBlock.Block), deleted)
	case core.OperationMoveBlock:
		return movePageSection(document, operation.MoveBlock)
	case core.OperationReplaceBlockKind:
		return c.replacePageSection(document, operation.ReplaceBlockKind)
	default:
		return fmt.Errorf("unsupported Page operation %q", operation.Kind)
	}
}

func pageOperationBlock(operation core.Operation) core.BlockID {
	switch operation.Kind {
	case core.OperationSetField:
		return operation.SetField.Target.Block
	case core.OperationUnsetField:
		return operation.UnsetField.Target.Block
	case core.OperationAttachFile:
		return operation.AttachFile.Target.Block
	case core.OperationDetachFile:
		return operation.DetachFile.Target.Block
	case core.OperationInsertBlock:
		return operation.InsertBlock.Parent
	case core.OperationDeleteBlock:
		return operation.DeleteBlock.Block
	case core.OperationMoveBlock:
		return operation.MoveBlock.Block
	case core.OperationReplaceBlockKind:
		return operation.ReplaceBlockKind.Block
	default:
		return ""
	}
}

func (c *PageCodec) findRichTextContainer(document *contentv1.LocalizedPageDocument, target core.BlockID, operation core.Operation) (*contentv1.PageSectionNode, *contentv1.PageSectionLocale, bool) {
	for _, section := range document.GetBase().GetNodes() {
		if section.GetSection().GetRichText() == nil {
			continue
		}
		locale, _ := findPageLocaleSection(document, section.GetSection().GetId())
		if target == core.BlockID(section.GetSection().GetId()) && operation.Kind == core.OperationInsertBlock {
			if _, outerKind := c.baseCases[operation.InsertBlock.Kind]; !outerKind {
				return section, locale, true
			}
		}
		for _, node := range section.GetSection().GetRichText().GetBlocks().GetNodes() {
			if target == core.BlockID(node.GetBlock().GetId()) ||
				(operation.Kind == core.OperationInsertBlock && operation.InsertBlock.After == core.BlockID(node.GetBlock().GetId())) {
				return section, locale, true
			}
		}
	}
	return nil, nil, false
}

func (c *PageCodec) applyRichTextOperation(document *contentv1.LocalizedPageDocument, section *contentv1.PageSectionNode, locale *contentv1.PageSectionLocale, operation core.Operation) error {
	if locale == nil {
		if operation.Kind == core.OperationUnsetField {
			return nil
		}
		var err error
		locale, err = c.newPageLocaleSection(section.GetSection().GetId(), "rich-text")
		if err != nil {
			return err
		}
		document.LocaleOverlay.Sections = append(document.LocaleOverlay.Sections, locale)
	}
	rich := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: c.rich.Catalog().Fingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE, Locale: document.GetLocale(),
		Base: section.GetSection().GetRichText().GetBlocks(), LocaleOverlay: locale.GetRichText().GetBlocks(),
	}
	if rich.Base == nil {
		rich.Base = &contentv1.RichTextBlockGraph{}
	}
	if rich.LocaleOverlay == nil {
		rich.LocaleOverlay = &contentv1.RichTextLocaleOverlay{Locale: document.GetLocale()}
		locale.GetRichText().Blocks = rich.LocaleOverlay
	}
	inner := operation
	if inner.InsertBlock != nil && inner.InsertBlock.Parent == core.BlockID(section.GetSection().GetId()) {
		copy := *inner.InsertBlock
		copy.Parent = ""
		inner.InsertBlock = &copy
	}
	if err := c.rich.applyOperation(rich, inner, make(map[string]struct{})); err != nil {
		return err
	}
	section.GetSection().GetRichText().Blocks = rich.Base
	locale.GetRichText().Blocks = rich.LocaleOverlay
	return nil
}

func (c *PageCodec) setPageField(document *contentv1.LocalizedPageDocument, role core.LocaleRole, target core.FieldTarget, value core.Value) error {
	_, node, ok := findPageNode(document, string(target.Block))
	if !ok {
		return errors.New("page section does not exist")
	}
	var message protoreflect.Message
	switch target.Field {
	case pageSectionSettingsField:
		message = node.GetSection().GetSettings().ProtoReflect()
	case pageSectionDataField:
		var err error
		_, message, err = c.sectionMessage(node.GetSection().ProtoReflect())
		if err != nil {
			return err
		}
	case pageSectionLocaleField:
		locale, found := findPageLocaleSection(document, node.GetSection().GetId())
		if !found {
			kind, _, err := c.sectionMessage(node.GetSection().ProtoReflect())
			if err != nil {
				return err
			}
			locale, err = c.newPageLocaleSection(node.GetSection().GetId(), kind)
			if err != nil {
				return err
			}
			document.LocaleOverlay.Sections = append(document.LocaleOverlay.Sections, locale)
		}
		var err error
		_, message, err = c.localeSectionMessage(locale.ProtoReflect())
		if err != nil {
			return err
		}
	default:
		return errors.New("field is not in the Page section catalog")
	}
	if role != core.LocaleRoleSource && target.Field == pageSectionLocaleField {
		_, field, err := pageResolvePath(message, target.Path, true)
		if err != nil {
			return err
		}
		if field == nil || field.IsList() || field.Kind() == protoreflect.MessageKind {
			return errors.New("target Page locale accepts only scalar leaf fields")
		}
	}
	if err := pageSetPath(message, target.Path, value); err != nil {
		return err
	}
	if role == core.LocaleRoleSource && target.Field == pageSectionDataField && pageWholeStableListPath(target.Path, "units") {
		return c.reconcilePageImmersiveLocale(document, node)
	}
	return nil
}

func pageWholeStableListPath(path []core.FieldPathSegment, field core.FieldID) bool {
	return len(path) == 1 && path[0].Field == field && path[0].Item == ""
}

func (c *PageCodec) reconcilePageImmersiveLocale(document *contentv1.LocalizedPageDocument, node *contentv1.PageSectionNode) error {
	if node.GetSection().GetImmersiveScene() == nil {
		return nil
	}
	locale, found := findPageLocaleSection(document, node.GetSection().GetId())
	if !found {
		var err error
		locale, err = c.newPageLocaleSection(node.GetSection().GetId(), "immersive-scene")
		if err != nil {
			return err
		}
		document.LocaleOverlay.Sections = append(document.LocaleOverlay.Sections, locale)
	}
	_, base, err := c.sectionMessage(node.GetSection().ProtoReflect())
	if err != nil {
		return err
	}
	_, localized, err := c.localeSectionMessage(locale.ProtoReflect())
	if err != nil {
		return err
	}
	baseUnits := findMessageField(base, "units")
	localeUnits := findMessageField(localized, "units")
	if baseUnits == nil || localeUnits == nil || !baseUnits.IsList() || !localeUnits.IsList() {
		return errors.New("generated immersive unit lists are required")
	}
	baseID := pageIdentityField(baseUnits.Message())
	localeID := pageIdentityField(localeUnits.Message())
	if baseID == nil || localeID == nil {
		return errors.New("generated immersive unit identities are required")
	}
	existing := make(map[string]protoreflect.Message)
	current := localized.Get(localeUnits).List()
	for index := 0; index < current.Len(); index++ {
		item := current.Get(index).Message()
		existing[item.Get(localeID).String()] = proto.Clone(item.Interface()).ProtoReflect()
	}
	next := localized.Mutable(localeUnits).List()
	next.Truncate(0)
	baseList := base.Get(baseUnits).List()
	for index := 0; index < baseList.Len(); index++ {
		id := baseList.Get(index).Message().Get(baseID).String()
		item := existing[id]
		if item == nil || !item.IsValid() {
			item = next.NewElement().Message()
			item.Set(localeID, protoreflect.ValueOfString(id))
		}
		next.Append(protoreflect.ValueOfMessage(item))
	}
	return nil
}

func (c *PageCodec) unsetPageField(document *contentv1.LocalizedPageDocument, target core.FieldTarget) error {
	_, node, ok := findPageNode(document, string(target.Block))
	if !ok {
		return errors.New("page section does not exist")
	}
	var message protoreflect.Message
	switch target.Field {
	case pageSectionSettingsField:
		message = node.GetSection().GetSettings().ProtoReflect()
	case pageSectionDataField:
		var err error
		_, message, err = c.sectionMessage(node.GetSection().ProtoReflect())
		if err != nil {
			return err
		}
	case pageSectionLocaleField:
		locale, found := findPageLocaleSection(document, node.GetSection().GetId())
		if !found {
			return nil
		}
		var err error
		_, message, err = c.localeSectionMessage(locale.ProtoReflect())
		if err != nil {
			return err
		}
	default:
		return errors.New("field is not in the Page section catalog")
	}
	return pageClearPath(message, target.Path)
}

func (c *PageCodec) setPageFile(document *contentv1.LocalizedPageDocument, target core.FieldTarget, file core.FileReference) error {
	_, node, ok := findPageNode(document, string(target.Block))
	if !ok {
		return errors.New("page section does not exist")
	}
	var message protoreflect.Message
	switch target.Field {
	case pageSectionSettingsField:
		message = node.GetSection().GetSettings().ProtoReflect()
	case pageSectionDataField:
		var err error
		_, message, err = c.sectionMessage(node.GetSection().ProtoReflect())
		if err != nil {
			return err
		}
	default:
		return errors.New("page File must target shared section data")
	}
	return pageSetFilePath(message, target.Path, string(file))
}

func (c *PageCodec) insertPageSection(document *contentv1.LocalizedPageDocument, operation *core.InsertBlock) error {
	if _, _, exists := findPageNode(document, string(operation.Block)); exists {
		return errors.New("page section already exists")
	}
	if c.baseCases[operation.Kind] == nil {
		return fmt.Errorf("unsupported Page section kind %q", operation.Kind)
	}
	section := &contentv1.PageSection{Id: string(operation.Block), Settings: &contentv1.PageSectionSettings{}}
	if err := initializeOneofMessage(section.ProtoReflect(), "value", c.baseCases[operation.Kind].JSONName()); err != nil {
		return err
	}
	locale, err := c.newPageLocaleSection(string(operation.Block), operation.Kind)
	if err != nil {
		return err
	}
	placement, err := resolvePagePlacement(document, string(operation.Parent), string(operation.After))
	if err != nil {
		return err
	}
	node := &contentv1.PageSectionNode{Section: section, Placement: placement}
	document.Base.Nodes = append(document.Base.Nodes, node)
	document.LocaleOverlay.Sections = append(document.LocaleOverlay.Sections, locale)
	placePageNodeAfter(document.Base.Nodes, string(operation.Block), string(operation.After))
	return nil
}

func (c *PageCodec) newPageLocaleSection(sectionID string, kind core.BlockKind) (*contentv1.PageSectionLocale, error) {
	field := c.localeCase[kind]
	if field == nil {
		return nil, fmt.Errorf("page section kind %q has no locale shape", kind)
	}
	section := &contentv1.PageSectionLocale{SectionId: sectionID}
	if err := initializeOneofMessage(section.ProtoReflect(), "value", field.JSONName()); err != nil {
		return nil, err
	}
	if kind == "rich-text" {
		section.GetRichText().Blocks = &contentv1.RichTextLocaleOverlay{}
	}
	return section, nil
}

func deletePageSection(document *contentv1.LocalizedPageDocument, sectionID string, deleted map[string]struct{}) error {
	if _, _, ok := findPageNode(document, sectionID); !ok {
		return errors.New("page section does not exist")
	}
	deleted[sectionID] = struct{}{}
	for changed := true; changed; {
		changed = false
		for _, node := range document.Base.Nodes {
			if _, parentDeleted := deleted[node.GetPlacement().GetParentSectionId()]; parentDeleted {
				if _, seen := deleted[node.GetSection().GetId()]; !seen {
					deleted[node.GetSection().GetId()] = struct{}{}
					changed = true
				}
			}
		}
	}
	base := document.Base.Nodes[:0]
	for _, node := range document.Base.Nodes {
		if _, remove := deleted[node.GetSection().GetId()]; !remove {
			base = append(base, node)
		}
	}
	document.Base.Nodes = base
	locale := document.LocaleOverlay.Sections[:0]
	for _, section := range document.LocaleOverlay.Sections {
		if _, remove := deleted[section.GetSectionId()]; !remove {
			locale = append(locale, section)
		}
	}
	document.LocaleOverlay.Sections = locale
	return nil
}

func movePageSection(document *contentv1.LocalizedPageDocument, operation *core.MoveBlock) error {
	_, node, ok := findPageNode(document, string(operation.Block))
	if !ok {
		return errors.New("page section does not exist")
	}
	placement, err := resolvePagePlacement(document, string(operation.Parent), string(operation.After))
	if err != nil {
		return err
	}
	node.Placement = placement
	placePageNodeAfter(document.Base.Nodes, string(operation.Block), string(operation.After))
	return nil
}

func (c *PageCodec) replacePageSection(document *contentv1.LocalizedPageDocument, operation *core.ReplaceBlockKind) error {
	index, old, ok := findPageNode(document, string(operation.Block))
	if !ok {
		return errors.New("page section does not exist")
	}
	field := c.baseCases[operation.Kind]
	if field == nil {
		return fmt.Errorf("unsupported Page section kind %q", operation.Kind)
	}
	if old.GetSection().GetRichText().GetBlocks().GetNodes() != nil && len(old.GetSection().GetRichText().GetBlocks().GetNodes()) > 0 {
		return errors.New("page section must be empty before replacing its kind")
	}
	if len(old.GetSection().GetColumns().GetProps().GetColumns()) > 0 {
		return errors.New("page section must be empty before replacing its kind")
	}
	for _, candidate := range document.GetBase().GetNodes() {
		if candidate.GetSection().GetId() != old.GetSection().GetId() && candidate.GetPlacement().GetParentSectionId() == old.GetSection().GetId() {
			return errors.New("page section must be empty before replacing its kind")
		}
	}
	section := &contentv1.PageSection{Id: string(operation.Block), Settings: proto.Clone(old.GetSection().GetSettings()).(*contentv1.PageSectionSettings)}
	if err := initializeOneofMessage(section.ProtoReflect(), "value", field.JSONName()); err != nil {
		return err
	}
	document.Base.Nodes[index] = &contentv1.PageSectionNode{Section: section, Placement: proto.Clone(old.GetPlacement()).(*contentv1.PageSectionPlacement)}
	locale, err := c.newPageLocaleSection(string(operation.Block), operation.Kind)
	if err != nil {
		return err
	}
	for localeIndex, candidate := range document.LocaleOverlay.Sections {
		if candidate.GetSectionId() == string(operation.Block) {
			document.LocaleOverlay.Sections[localeIndex] = locale
			return nil
		}
	}
	document.LocaleOverlay.Sections = append(document.LocaleOverlay.Sections, locale)
	return nil
}

func findPageNode(document *contentv1.LocalizedPageDocument, sectionID string) (int, *contentv1.PageSectionNode, bool) {
	for index, node := range document.GetBase().GetNodes() {
		if node.GetSection().GetId() == sectionID {
			return index, node, true
		}
	}
	return 0, nil, false
}

func findPageLocaleSection(document *contentv1.LocalizedPageDocument, sectionID string) (*contentv1.PageSectionLocale, bool) {
	for _, section := range document.GetLocaleOverlay().GetSections() {
		if section.GetSectionId() == sectionID {
			return section, true
		}
	}
	return nil, false
}

func findPageColumn(document *contentv1.LocalizedPageDocument, columnID string) (*contentv1.ColumnsSectionProps_ColumnsItem, *contentv1.PageSectionNode, int, bool) {
	for _, node := range document.GetBase().GetNodes() {
		for index, column := range node.GetSection().GetColumns().GetProps().GetColumns() {
			if column.GetId() == columnID {
				return column, node, index, true
			}
		}
	}
	return nil, nil, 0, false
}

func insertPageColumn(document *contentv1.LocalizedPageDocument, operation *core.InsertBlock) error {
	if _, _, _, exists := findPageColumn(document, string(operation.Block)); exists {
		return errors.New("page column already exists")
	}
	_, parent, ok := findPageNode(document, string(operation.Parent))
	if !ok || parent.GetSection().GetColumns() == nil || parent.GetSection().GetColumns().GetProps() == nil {
		return errors.New("page-column parent must be a Columns section")
	}
	columns := parent.GetSection().GetColumns().GetProps().Columns
	column := &contentv1.ColumnsSectionProps_ColumnsItem{Id: string(operation.Block), Ratio: 1}
	columns = append(columns, column)
	parent.GetSection().GetColumns().GetProps().Columns = placePageColumnAfter(columns, string(operation.Block), string(operation.After))
	return nil
}

func applyPageColumnOperation(document *contentv1.LocalizedPageDocument, operation core.Operation) error {
	column, parent, index, ok := findPageColumn(document, string(pageOperationBlock(operation)))
	if !ok {
		return errors.New("page column does not exist")
	}
	switch operation.Kind {
	case core.OperationSetField:
		if operation.SetField.Target.Field != pageColumnRatioField || len(operation.SetField.Target.Path) != 0 || operation.SetField.Value.Kind != core.ValueKindNumber {
			return errors.New("page-column exposes only the numeric ratio field")
		}
		ratio, err := strconv.ParseFloat(operation.SetField.Value.Text, 64)
		if err != nil {
			return errors.New("page-column ratio must be numeric")
		}
		column.Ratio = ratio
		return nil
	case core.OperationUnsetField:
		return errors.New("page-column ratio is required")
	case core.OperationDeleteBlock:
		for _, node := range document.GetBase().GetNodes() {
			if node.GetPlacement().GetColumnId() == column.GetId() {
				return errors.New("page-column must be empty before deletion")
			}
		}
		columns := parent.GetSection().GetColumns().GetProps().Columns
		parent.GetSection().GetColumns().GetProps().Columns = append(columns[:index], columns[index+1:]...)
		return nil
	case core.OperationMoveBlock:
		if operation.MoveBlock.Parent != core.BlockID(parent.GetSection().GetId()) {
			return errors.New("page-column cannot move between Columns sections")
		}
		parent.GetSection().GetColumns().GetProps().Columns = placePageColumnAfter(
			parent.GetSection().GetColumns().GetProps().Columns,
			column.GetId(), string(operation.MoveBlock.After),
		)
		return nil
	case core.OperationReplaceBlockKind:
		return errors.New("page-column kind cannot be replaced")
	default:
		return errors.New("unsupported page-column operation")
	}
}

func placePageColumnAfter(columns []*contentv1.ColumnsSectionProps_ColumnsItem, columnID, after string) []*contentv1.ColumnsSectionProps_ColumnsItem {
	var moved *contentv1.ColumnsSectionProps_ColumnsItem
	remaining := make([]*contentv1.ColumnsSectionProps_ColumnsItem, 0, len(columns))
	for _, column := range columns {
		if column.GetId() == columnID {
			moved = column
			continue
		}
		remaining = append(remaining, column)
	}
	if moved == nil {
		return columns
	}
	result := make([]*contentv1.ColumnsSectionProps_ColumnsItem, 0, len(columns))
	inserted := false
	if after == "" {
		result, inserted = append(result, moved), true
	}
	for _, column := range remaining {
		result = append(result, column)
		if column.GetId() == after {
			result, inserted = append(result, moved), true
		}
	}
	if !inserted {
		result = append(result, moved)
	}
	return result
}

func resolvePagePlacement(document *contentv1.LocalizedPageDocument, parent, after string) (*contentv1.PageSectionPlacement, error) {
	placement := &contentv1.PageSectionPlacement{}
	if _, columns, _, isColumn := findPageColumn(document, parent); isColumn {
		placement.ParentSectionId = optionalString(columns.GetSection().GetId())
		placement.ColumnId = optionalString(parent)
	} else {
		placement.ParentSectionId = optionalString(parent)
	}
	if after != "" {
		_, sibling, ok := findPageNode(document, after)
		if !ok || sibling.GetPlacement().GetParentSectionId() != placement.GetParentSectionId() ||
			sibling.GetPlacement().GetColumnId() != placement.GetColumnId() {
			return nil, errors.New("page after-section is not a sibling in the same section slot")
		}
		return placement, nil
	}
	if placement.GetParentSectionId() != "" && placement.GetColumnId() == "" {
		_, parent, ok := findPageNode(document, placement.GetParentSectionId())
		if ok && parent.GetSection().GetColumns() != nil {
			return nil, errors.New("columns children must target a stable page-column handle")
		}
	}
	return placement, nil
}

func placePageNodeAfter(nodes []*contentv1.PageSectionNode, sectionID, after string) {
	var moved *contentv1.PageSectionNode
	remaining := make([]*contentv1.PageSectionNode, 0, len(nodes))
	for _, node := range nodes {
		if node.GetSection().GetId() == sectionID {
			moved = node
			continue
		}
		remaining = append(remaining, node)
	}
	if moved == nil {
		return
	}
	result, inserted := make([]*contentv1.PageSectionNode, 0, len(nodes)), false
	if after == "" {
		result, inserted = append(result, moved), true
	}
	for _, node := range remaining {
		result = append(result, node)
		if node.GetSection().GetId() == after {
			result, inserted = append(result, moved), true
		}
	}
	if !inserted {
		result = append(result, moved)
	}
	copy(nodes, result)
	orders := make(map[string]uint32)
	for _, node := range nodes {
		key := node.GetPlacement().GetParentSectionId() + "\x00" + node.GetPlacement().GetColumnId()
		node.Placement.Index = orders[key]
		orders[key]++
	}
}
