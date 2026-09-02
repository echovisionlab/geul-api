package aidocumentadapter

import (
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// Compile applies a validated content-only batch to a clone, flattens it
// through the generated mutation contract, and returns the exact Store batch.
// Domain metadata operations must be removed by the owning registration.
func (c *RichTextCodec) Compile(
	documentID uuid.UUID,
	document *contentv1.LocalizedRichTextDocument,
	role core.LocaleRole,
	expected core.Revision,
	contributor uuid.UUID,
	operations []core.Operation,
) (contentblock.Batch, []core.OperationIssue, error) {
	if documentID == uuid.Nil || contributor == uuid.Nil {
		return contentblock.Batch{}, nil, errors.New("document and contributor UUIDs are required")
	}
	working, ok := proto.Clone(document).(*contentv1.LocalizedRichTextDocument)
	if !ok || working == nil {
		return contentblock.Batch{}, nil, errors.New("localized Rich Text document is required")
	}
	deleted := make(map[string]struct{})
	for index, operation := range operations {
		if operation.Kind == core.OperationCreateTranslation || operation.Kind == core.OperationDeleteTranslation {
			continue
		}
		if operation.InsertRelationItem != nil || operation.DeleteRelationItem != nil || operation.MoveRelationItem != nil {
			return contentblock.Batch{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Message: "Rich Text Blocks do not expose generic relations"}}, nil
		}
		if err := c.applyOperation(working, operation, deleted); err != nil {
			return contentblock.Batch{}, []core.OperationIssue{{Operation: index, Code: core.IssueInvalidOperation, Message: err.Error()}}, nil
		}
	}
	mutation := &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: c.catalog.Fingerprint, Profile: c.profile,
		ExpectedRevision: string(expected), ContributorMemberIds: []string{contributor.String()},
	}
	if role == core.LocaleRoleSource {
		for _, node := range working.GetBase().GetNodes() {
			mutation.BaseMutations = append(mutation.BaseMutations, &contentv1.RichTextBlockMutation{Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{Node: node}}})
		}
		for blockID := range deleted {
			mutation.BaseMutations = append(mutation.BaseMutations, &contentv1.RichTextBlockMutation{Operation: &contentv1.RichTextBlockMutation_Delete{Delete: &contentv1.DeleteRichTextBlock{BlockId: blockID}}})
		}
	}
	group := &contentv1.RichTextLocaleMutationGroup{Locale: working.GetLocale()}
	for _, block := range working.GetLocaleOverlay().GetBlocks() {
		group.Mutations = append(group.Mutations, &contentv1.RichTextBlockLocaleMutation{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{Block: block}}})
	}
	if len(group.Mutations) != 0 {
		mutation.LocaleMutationGroups = append(mutation.LocaleMutationGroups, group)
	}
	if len(mutation.BaseMutations) == 0 && len(mutation.LocaleMutationGroups) == 0 {
		revision, err := uuid.Parse(string(expected))
		if err != nil || revision == uuid.Nil {
			return contentblock.Batch{}, nil, errors.New("expected revision must be a UUID")
		}
		return contentblock.Batch{
			DocumentID:           documentID,
			ExpectedRevision:     revision,
			ContributorMemberIDs: []uuid.UUID{contributor},
		}, nil, nil
	}
	batch, err := contentblock.BatchFromRichTextProto(documentID, mutation)
	if err != nil {
		return contentblock.Batch{}, []core.OperationIssue{{Operation: -1, Code: core.IssueInvalidOperation, Message: err.Error()}}, nil
	}
	return batch, nil, nil
}

func (c *RichTextCodec) applyOperation(document *contentv1.LocalizedRichTextDocument, operation core.Operation, deleted map[string]struct{}) error {
	switch operation.Kind {
	case core.OperationSetField:
		return c.setField(document, operation.SetField.Target, operation.SetField.Value)
	case core.OperationUnsetField:
		return c.unsetField(document, operation.UnsetField.Target)
	case core.OperationAttachFile:
		return c.setFile(document, operation.AttachFile.Target, operation.AttachFile.File)
	case core.OperationDetachFile:
		return c.setFile(document, operation.DetachFile.Target, "")
	case core.OperationInsertBlock:
		op := operation.InsertBlock
		if _, _, ok := findBaseNode(document, string(op.Block)); ok {
			return errors.New("block already exists")
		}
		node, locale, err := c.newBlock(op.Kind, string(op.Block))
		if err != nil {
			return err
		}
		node.Placement = &contentv1.ContentBlockPlacement{ParentBlockId: optionalString(string(op.Parent))}
		document.Base.Nodes = append(document.Base.Nodes, node)
		document.LocaleOverlay.Blocks = append(document.LocaleOverlay.Blocks, locale)
		placeProtoNodeAfter(document.Base.Nodes, string(op.Block), string(op.After))
		return nil
	case core.OperationDeleteBlock:
		blockID := string(operation.DeleteBlock.Block)
		deleted[blockID] = struct{}{}
		removeBlockAndDescendants(document, blockID, deleted)
		return nil
	case core.OperationMoveBlock:
		op := operation.MoveBlock
		_, node, ok := findBaseNode(document, string(op.Block))
		if !ok {
			return errors.New("block does not exist")
		}
		node.Placement.ParentBlockId = optionalString(string(op.Parent))
		placeProtoNodeAfter(document.Base.Nodes, string(op.Block), string(op.After))
		return nil
	case core.OperationReplaceBlockKind:
		op := operation.ReplaceBlockKind
		index, old, ok := findBaseNode(document, string(op.Block))
		if !ok {
			return errors.New("block does not exist")
		}
		node, locale, err := c.newBlock(op.Kind, string(op.Block))
		if err != nil {
			return err
		}
		node.Placement = proto.Clone(old.Placement).(*contentv1.ContentBlockPlacement)
		document.Base.Nodes[index] = node
		replaceLocaleBlock(document, locale)
		return nil
	default:
		return fmt.Errorf("unsupported Rich Text operation %q", operation.Kind)
	}
}

func (c *RichTextCodec) setField(document *contentv1.LocalizedRichTextDocument, target core.FieldTarget, value core.Value) error {
	if target.Relation != "" || target.Item != "" {
		return errors.New("rich Text field cannot target a generic relation")
	}
	_, node, ok := findBaseNode(document, string(target.Block))
	if !ok {
		return errors.New("field block does not exist")
	}
	kind, base, err := c.blockMessage(node.Block.ProtoReflect())
	if err != nil {
		return err
	}
	rule, ok := c.fieldRules[richTextFieldKey(kind, target.Field)]
	if !ok {
		return errors.New("field is not in the Rich Text catalog")
	}
	message := base
	if rule.Ownership == core.FieldOwnershipLocale {
		locale, err := c.ensureLocaleBlock(document, node.Block.GetId(), kind)
		if err != nil {
			return err
		}
		_, message, err = c.localeBlockMessage(locale.ProtoReflect())
		if err != nil {
			return err
		}
	}
	return setRichTextField(message, c.descriptor, c.blocks[kind], target, value)
}

func (c *RichTextCodec) unsetField(document *contentv1.LocalizedRichTextDocument, target core.FieldTarget) error {
	_, node, ok := findBaseNode(document, string(target.Block))
	if !ok {
		return errors.New("field block does not exist")
	}
	kind, base, err := c.blockMessage(node.Block.ProtoReflect())
	if err != nil {
		return err
	}
	rule, ok := c.fieldRules[richTextFieldKey(kind, target.Field)]
	if !ok {
		return errors.New("field is not in the Rich Text catalog")
	}
	message := base
	if rule.Ownership == core.FieldOwnershipLocale {
		locale, found := findLocaleBlock(document, node.Block.GetId())
		if !found {
			return nil
		}
		_, message, err = c.localeBlockMessage(locale.ProtoReflect())
		if err != nil {
			return err
		}
	}
	return clearRichTextField(message, c.blocks[kind], target)
}

func (c *RichTextCodec) setFile(document *contentv1.LocalizedRichTextDocument, target core.FieldTarget, file core.FileReference) error {
	_, node, ok := findBaseNode(document, string(target.Block))
	if !ok {
		return errors.New("file block does not exist")
	}
	kind, base, err := c.blockMessage(node.Block.ProtoReflect())
	if err != nil {
		return err
	}
	return setRichTextFile(base, c.blocks[kind], target, file)
}

func (c *RichTextCodec) newBlock(kind core.BlockKind, id string) (*contentv1.RichTextBlockNode, *contentv1.RichTextBlockLocale, error) {
	protoCase, ok := c.protoCases[kind]
	if !ok {
		return nil, nil, fmt.Errorf("unsupported Rich Text block kind %q", kind)
	}
	base := &contentv1.RichTextBlock{Id: id}
	if err := initializeOneofMessage(base.ProtoReflect(), "value", protoCase); err != nil {
		return nil, nil, err
	}
	locale := &contentv1.RichTextBlockLocale{BlockId: id}
	if err := initializeOneofMessage(locale.ProtoReflect(), "value", protoCase); err != nil {
		return nil, nil, err
	}
	return &contentv1.RichTextBlockNode{Block: base}, locale, nil
}

func (c *RichTextCodec) ensureLocaleBlock(document *contentv1.LocalizedRichTextDocument, blockID string, kind core.BlockKind) (*contentv1.RichTextBlockLocale, error) {
	if block, ok := findLocaleBlock(document, blockID); ok {
		return block, nil
	}
	_, locale, err := c.newBlock(kind, blockID)
	if err != nil {
		return nil, err
	}
	document.LocaleOverlay.Blocks = append(document.LocaleOverlay.Blocks, locale)
	return locale, nil
}

func findBaseNode(document *contentv1.LocalizedRichTextDocument, blockID string) (int, *contentv1.RichTextBlockNode, bool) {
	for index, node := range document.GetBase().GetNodes() {
		if node.GetBlock().GetId() == blockID {
			return index, node, true
		}
	}
	return 0, nil, false
}

func findLocaleBlock(document *contentv1.LocalizedRichTextDocument, blockID string) (*contentv1.RichTextBlockLocale, bool) {
	for _, block := range document.GetLocaleOverlay().GetBlocks() {
		if block.GetBlockId() == blockID {
			return block, true
		}
	}
	return nil, false
}

func replaceLocaleBlock(document *contentv1.LocalizedRichTextDocument, replacement *contentv1.RichTextBlockLocale) {
	for index, block := range document.LocaleOverlay.Blocks {
		if block.GetBlockId() == replacement.GetBlockId() {
			document.LocaleOverlay.Blocks[index] = replacement
			return
		}
	}
	document.LocaleOverlay.Blocks = append(document.LocaleOverlay.Blocks, replacement)
}

func placeProtoNodeAfter(nodes []*contentv1.RichTextBlockNode, blockID, after string) {
	var moved *contentv1.RichTextBlockNode
	remaining := make([]*contentv1.RichTextBlockNode, 0, len(nodes))
	for _, node := range nodes {
		if node.GetBlock().GetId() == blockID {
			moved = node
			continue
		}
		remaining = append(remaining, node)
	}
	if moved == nil {
		return
	}
	parent := moved.GetPlacement().GetParentBlockId()
	result := make([]*contentv1.RichTextBlockNode, 0, len(nodes))
	inserted := false
	if after == "" {
		result = append(result, moved)
		inserted = true
	}
	for _, node := range remaining {
		result = append(result, node)
		if node.GetBlock().GetId() == after {
			result = append(result, moved)
			inserted = true
		}
	}
	if !inserted {
		result = append(result, moved)
	}
	copy(nodes, result)
	orders := make(map[string]uint32)
	for _, node := range nodes {
		if node.GetPlacement().GetParentBlockId() == parent {
			node.Placement.Index = orders[parent]
			orders[parent]++
		}
	}
}

func removeBlockAndDescendants(document *contentv1.LocalizedRichTextDocument, root string, deleted map[string]struct{}) {
	for changed := true; changed; {
		changed = false
		for _, node := range document.Base.Nodes {
			if _, parentDeleted := deleted[node.GetPlacement().GetParentBlockId()]; parentDeleted {
				if _, already := deleted[node.GetBlock().GetId()]; !already {
					deleted[node.GetBlock().GetId()] = struct{}{}
					changed = true
				}
			}
		}
	}
	base := document.Base.Nodes[:0]
	for _, node := range document.Base.Nodes {
		if _, remove := deleted[node.GetBlock().GetId()]; !remove {
			base = append(base, node)
		}
	}
	document.Base.Nodes = base
	locale := document.LocaleOverlay.Blocks[:0]
	for _, block := range document.LocaleOverlay.Blocks {
		if _, remove := deleted[block.GetBlockId()]; !remove {
			locale = append(locale, block)
		}
	}
	document.LocaleOverlay.Blocks = locale
	_ = root
}
