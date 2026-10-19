package aidocumentadapter

import core "github.com/echovisionlab/geul-api/internal/aidocument"

func (c *PageCodec) validateRichOperations(nodes []core.Node, operations []core.Operation) []core.OperationIssue {
	graph := make(map[core.BlockID]core.Node, len(nodes))
	for _, node := range nodes {
		graph[node.ID] = node
	}
	richKinds := make(map[core.BlockKind]bool)
	for _, kind := range c.rich.Catalog().BlockKinds {
		richKinds[kind] = true
	}
	var issues []core.OperationIssue
	for index, operation := range operations {
		var block core.BlockID
		var node core.Node
		switch operation.Kind {
		case core.OperationInsertBlock:
			insert := operation.InsertBlock
			block, node = insert.Block, core.Node{ID: insert.Block, Kind: insert.Kind, Parent: insert.Parent}
		case core.OperationMoveBlock:
			block = operation.MoveBlock.Block
			node = graph[block]
			node.Parent = operation.MoveBlock.Parent
		case core.OperationReplaceBlockKind:
			block = operation.ReplaceBlockKind.Block
			node = graph[block]
			node.Kind = operation.ReplaceBlockKind.Kind
		case core.OperationDeleteBlock:
			deleted := map[core.BlockID]bool{operation.DeleteBlock.Block: true}
			for changed := true; changed; {
				changed = false
				for id, existing := range graph {
					if deleted[id] || deleted[existing.Parent] {
						deleted[id] = true
						delete(graph, id)
						changed = true
					}
				}
			}
		case core.OperationSetField:
			if !c.rich.descriptor.InlineMath && pageValueContainsMath(operation.SetField.Value) {
				issues = append(issues, core.OperationIssue{Operation: index, Code: core.IssueInvalidOperation, Handle: string(operation.SetField.Target.Block), Message: "Page rich text does not allow inline math (inline_math=false)"})
			}
		}
		if block != "" {
			if richKinds[node.Kind] && (node.Parent == "" || graph[node.Parent].Kind != "rich-text") {
				issues = append(issues, core.OperationIssue{Operation: index, Code: core.IssueInvalidBlockRelation, Handle: string(block), Message: "Page rich blocks require a rich-text section parent"})
			}
			graph[block] = node
		}
	}
	return issues
}

func pageValueContainsMath(value core.Value) bool {
	var contains func([]core.InlineItem) bool
	contains = func(items []core.InlineItem) bool {
		for _, item := range items {
			if item.Kind == core.InlineKindMath || contains(item.Children) {
				return true
			}
		}
		return false
	}
	if contains(value.Inline) {
		return true
	}
	for _, field := range value.Object {
		if pageValueContainsMath(field.Value) {
			return true
		}
	}
	for _, item := range value.List {
		if pageValueContainsMath(item.Value) {
			return true
		}
	}
	return false
}
