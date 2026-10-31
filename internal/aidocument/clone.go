package aidocument

import "sort"

func canonicalNodes(nodes []Node) []Node {
	clones := append([]Node(nil), nodes...)
	ids := make(map[BlockID]struct{}, len(clones))
	for index := range clones {
		ids[clones[index].ID] = struct{}{}
		clones[index].Shared = append([]FieldValue(nil), clones[index].Shared...)
		clones[index].Localized = append([]FieldValue(nil), clones[index].Localized...)
		clones[index].Files = append([]FileBinding(nil), clones[index].Files...)
		clones[index].Relations = append([]Relation(nil), clones[index].Relations...)
		cloneFieldValues(clones[index].Shared)
		cloneFieldValues(clones[index].Localized)
		cloneFileBindings(clones[index].Files)
		sort.Slice(clones[index].Shared, func(a, b int) bool { return clones[index].Shared[a].ID < clones[index].Shared[b].ID })
		sort.Slice(clones[index].Localized, func(a, b int) bool { return clones[index].Localized[a].ID < clones[index].Localized[b].ID })
		sort.Slice(clones[index].Files, func(a, b int) bool {
			left := nestedFieldKey(clones[index].Files[a].Field, clones[index].Files[a].Path)
			right := nestedFieldKey(clones[index].Files[b].Field, clones[index].Files[b].Path)
			if left != right {
				return left < right
			}
			return clones[index].Files[a].File < clones[index].Files[b].File
		})
		for relationIndex := range clones[index].Relations {
			relation := &clones[index].Relations[relationIndex]
			relation.Items = append([]RelationItem(nil), relation.Items...)
			for itemIndex := range relation.Items {
				item := &relation.Items[itemIndex]
				item.Shared = append([]FieldValue(nil), item.Shared...)
				item.Localized = append([]FieldValue(nil), item.Localized...)
				item.Files = append([]FileBinding(nil), item.Files...)
				cloneFieldValues(item.Shared)
				cloneFieldValues(item.Localized)
				cloneFileBindings(item.Files)
				sort.Slice(item.Shared, func(a, b int) bool { return item.Shared[a].ID < item.Shared[b].ID })
				sort.Slice(item.Localized, func(a, b int) bool { return item.Localized[a].ID < item.Localized[b].ID })
				sort.Slice(item.Files, func(a, b int) bool {
					return nestedFieldKey(item.Files[a].Field, item.Files[a].Path) < nestedFieldKey(item.Files[b].Field, item.Files[b].Path)
				})
			}
			sort.SliceStable(relation.Items, func(a, b int) bool {
				if relation.Items[a].Order != relation.Items[b].Order {
					return relation.Items[a].Order < relation.Items[b].Order
				}
				return relation.Items[a].ID < relation.Items[b].ID
			})
		}
		sort.Slice(clones[index].Relations, func(a, b int) bool {
			return clones[index].Relations[a].ID < clones[index].Relations[b].ID
		})
	}
	children := make(map[BlockID][]Node, len(clones))
	roots := make([]Node, 0, len(clones))
	for _, node := range clones {
		if node.Parent == "" {
			roots = append(roots, node)
			continue
		}
		if _, parentIncluded := ids[node.Parent]; !parentIncluded {
			roots = append(roots, node)
			continue
		}
		children[node.Parent] = append(children[node.Parent], node)
	}
	less := func(items []Node) {
		sort.SliceStable(items, func(a, b int) bool {
			if items[a].Parent != items[b].Parent {
				return items[a].Parent < items[b].Parent
			}
			if items[a].Order != items[b].Order {
				return items[a].Order < items[b].Order
			}
			return items[a].ID < items[b].ID
		})
	}
	less(roots)
	for parent := range children {
		less(children[parent])
	}
	result := make([]Node, 0, len(clones))
	var appendSubtree func(Node)
	appendSubtree = func(node Node) {
		result = append(result, node)
		for _, child := range children[node.ID] {
			appendSubtree(child)
		}
	}
	for _, root := range roots {
		appendSubtree(root)
	}
	return result
}

func cloneFieldValues(values []FieldValue) {
	for index := range values {
		values[index].Value = cloneValue(values[index].Value)
	}
}

func cloneFileBindings(values []FileBinding) {
	for index := range values {
		values[index].Path = append([]FieldPathSegment(nil), values[index].Path...)
	}
}

func cloneValue(value Value) Value {
	copy := value
	copy.Inline = cloneInlineItems(value.Inline)
	copy.List = append([]ListItem(nil), value.List...)
	for index := range copy.List {
		copy.List[index].Value = cloneValue(copy.List[index].Value)
	}
	copy.Object = append([]ObjectField(nil), value.Object...)
	for index := range copy.Object {
		copy.Object[index].Value = cloneValue(copy.Object[index].Value)
	}
	return copy
}

func cloneInlineItems(items []InlineItem) []InlineItem {
	copy := append([]InlineItem(nil), items...)
	for index := range copy {
		copy[index].Children = cloneInlineItems(copy[index].Children)
	}
	return copy
}
