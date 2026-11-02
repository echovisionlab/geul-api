package aidocument

// catalogIndex is the keyed lookup view of a Catalog shared by operation
// validation, field reads and document value validation.
type catalogIndex struct {
	kinds          map[BlockKind]struct{}
	fields         map[string]FieldRule
	relations      map[string]map[RelationItemKind]struct{}
	relationFields map[string]FieldRule
}

func newCatalogIndex(catalog Catalog) catalogIndex {
	index := catalogIndex{
		kinds:          make(map[BlockKind]struct{}, len(catalog.BlockKinds)),
		fields:         make(map[string]FieldRule, len(catalog.Fields)),
		relations:      make(map[string]map[RelationItemKind]struct{}, len(catalog.Relations)),
		relationFields: make(map[string]FieldRule, len(catalog.RelationFields)),
	}
	for _, kind := range catalog.BlockKinds {
		index.kinds[kind] = struct{}{}
	}
	for _, rule := range catalog.Fields {
		index.fields[fieldRuleKey(rule.BlockKind, rule.Field)] = rule
	}
	for _, rule := range catalog.Relations {
		allowed := make(map[RelationItemKind]struct{}, len(rule.ItemKinds))
		for _, kind := range rule.ItemKinds {
			allowed[kind] = struct{}{}
		}
		index.relations[relationRuleKey(rule.BlockKind, rule.Relation)] = allowed
	}
	for _, rule := range catalog.RelationFields {
		index.relationFields[relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)] = rule.fieldRule()
	}
	return index
}

func (c catalogIndex) hasBlockKind(kind BlockKind) bool {
	_, ok := c.kinds[kind]
	return ok
}

func (c catalogIndex) field(kind BlockKind, field FieldID) (FieldRule, bool) {
	rule, ok := c.fields[fieldRuleKey(kind, field)]
	return rule, ok
}

// relationItemKinds returns the item kinds a block kind's relation allows.
func (c catalogIndex) relationItemKinds(kind BlockKind, relation RelationID) (map[RelationItemKind]struct{}, bool) {
	allowed, ok := c.relations[relationRuleKey(kind, relation)]
	return allowed, ok
}

func (c catalogIndex) relationField(kind BlockKind, relation RelationID, itemKind RelationItemKind, field FieldID) (FieldRule, bool) {
	rule, ok := c.relationFields[relationFieldRuleKey(kind, relation, itemKind, field)]
	return rule, ok
}

// fieldRule drops the relation coordinates, which the index key already
// carries, so block and relation-item fields resolve through one rule shape.
func (r RelationFieldRule) fieldRule() FieldRule {
	return FieldRule{
		BlockKind: r.BlockKind, Field: r.Field, ValueKind: r.ValueKind, Ownership: r.Ownership,
		Translatable: r.Translatable, File: r.File, Schema: r.Schema,
	}
}

func fieldRuleKey(kind BlockKind, field FieldID) string {
	return string(kind) + "\x00" + string(field)
}

func relationRuleKey(kind BlockKind, relation RelationID) string {
	return string(kind) + "\x00" + string(relation)
}

func relationFieldRuleKey(kind BlockKind, relation RelationID, itemKind RelationItemKind, field FieldID) string {
	return string(kind) + "\x00" + string(relation) + "\x00" + string(itemKind) + "\x00" + string(field)
}
