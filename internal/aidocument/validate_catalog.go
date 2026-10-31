package aidocument

import (
	"errors"
	"fmt"
)

func (c Catalog) validate() error {
	if err := validateOpaque("catalog fingerprint", c.Fingerprint, 256); err != nil {
		return err
	}
	kinds, err := validateBlockKinds(c.BlockKinds)
	if err != nil {
		return err
	}
	if err := validateFieldRules(c.Fields, kinds); err != nil {
		return err
	}
	relations, err := validateRelationRules(c.Relations, kinds)
	if err != nil {
		return err
	}
	return validateRelationFieldRules(c.RelationFields, relations)
}

func validateBlockKinds(blockKinds []BlockKind) (map[BlockKind]struct{}, error) {
	kinds := make(map[BlockKind]struct{}, len(blockKinds))
	for _, kind := range blockKinds {
		if err := validateStableID("block kind", string(kind), maxKindLength); err != nil {
			return nil, err
		}
		if _, duplicate := kinds[kind]; duplicate {
			return nil, fmt.Errorf("duplicate block kind %q", kind)
		}
		kinds[kind] = struct{}{}
	}
	return kinds, nil
}

func validateFieldRules(fields []FieldRule, kinds map[BlockKind]struct{}) error {
	rules := make(map[string]struct{}, len(fields))
	for _, rule := range fields {
		if _, ok := kinds[rule.BlockKind]; !ok {
			return fmt.Errorf("field %q refers to unknown block kind %q", rule.Field, rule.BlockKind)
		}
		if err := validateStableID("field ID", string(rule.Field), maxCatalogIDLength); err != nil {
			return err
		}
		key := fieldRuleKey(rule.BlockKind, rule.Field)
		if _, duplicate := rules[key]; duplicate {
			return fmt.Errorf("duplicate field rule %q on %q", rule.Field, rule.BlockKind)
		}
		rules[key] = struct{}{}
		if err := validateRuleSchema("field", rule); err != nil {
			return err
		}
	}
	return nil
}

// validateRelationRules returns the allowed item kinds of each relation rule
// by relationRuleKey.
func validateRelationRules(relationRules []RelationRule, kinds map[BlockKind]struct{}) (map[string]map[RelationItemKind]struct{}, error) {
	relations := make(map[string]map[RelationItemKind]struct{}, len(relationRules))
	for _, rule := range relationRules {
		if _, ok := kinds[rule.BlockKind]; !ok {
			return nil, fmt.Errorf("relation %q refers to unknown block kind %q", rule.Relation, rule.BlockKind)
		}
		if err := validateStableID("relation ID", string(rule.Relation), maxCatalogIDLength); err != nil {
			return nil, err
		}
		key := relationRuleKey(rule.BlockKind, rule.Relation)
		if _, duplicate := relations[key]; duplicate {
			return nil, fmt.Errorf("duplicate relation rule %q on %q", rule.Relation, rule.BlockKind)
		}
		itemKinds := make(map[RelationItemKind]struct{}, len(rule.ItemKinds))
		for _, kind := range rule.ItemKinds {
			if err := validateStableID("relation item kind", string(kind), maxKindLength); err != nil {
				return nil, err
			}
			if _, duplicate := itemKinds[kind]; duplicate {
				return nil, fmt.Errorf("duplicate relation item kind %q on relation %q", kind, rule.Relation)
			}
			itemKinds[kind] = struct{}{}
		}
		if len(itemKinds) == 0 {
			return nil, fmt.Errorf("relation %q must allow at least one item kind", rule.Relation)
		}
		relations[key] = itemKinds
	}
	return relations, nil
}

func validateRelationFieldRules(relationFields []RelationFieldRule, relations map[string]map[RelationItemKind]struct{}) error {
	seen := make(map[string]struct{}, len(relationFields))
	for _, rule := range relationFields {
		itemKinds, ok := relations[relationRuleKey(rule.BlockKind, rule.Relation)]
		if !ok {
			return fmt.Errorf("relation field %q refers to unknown relation %q", rule.Field, rule.Relation)
		}
		if _, ok := itemKinds[rule.ItemKind]; !ok {
			return fmt.Errorf("relation field %q refers to unsupported item kind %q", rule.Field, rule.ItemKind)
		}
		if err := validateStableID("relation field ID", string(rule.Field), maxCatalogIDLength); err != nil {
			return err
		}
		key := relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate relation field rule %q", rule.Field)
		}
		seen[key] = struct{}{}
		if err := validateRuleSchema("relation field", rule.fieldRule()); err != nil {
			return err
		}
	}
	return nil
}

// validateRuleSchema checks a field rule's shape and, when it declares a
// recursive schema, that the schema root repeats the rule.
func validateRuleSchema(label string, rule FieldRule) error {
	if err := validateFieldRuleShape(rule.Field, rule.ValueKind, rule.Ownership, rule.Translatable, rule.File); err != nil {
		return err
	}
	if rule.Schema == nil {
		return nil
	}
	if rule.Schema.Kind != rule.ValueKind || rule.Schema.Ownership != rule.Ownership || rule.Schema.Translatable != rule.Translatable || rule.Schema.File != rule.File {
		return fmt.Errorf("%s %q recursive schema root does not match its field rule", label, rule.Field)
	}
	if err := validateFieldSchema(*rule.Schema, 0); err != nil {
		return fmt.Errorf("%s %q schema: %w", label, rule.Field, err)
	}
	return nil
}

func validateFieldSchema(schema FieldSchema, depth int) error {
	if depth > 32 {
		return errors.New("field schema nesting exceeds 32 levels")
	}
	if err := validateFieldRuleShape("nested", schema.Kind, schema.Ownership, schema.Translatable, schema.File); err != nil {
		return err
	}
	if schema.File {
		if schema.Item != nil || len(schema.Fields) != 0 || schema.Identity.Kind != "" {
			return errors.New("file schema cannot contain child shape")
		}
		return nil
	}
	switch schema.Kind {
	case ValueKindList:
		if schema.Item == nil || len(schema.Fields) != 0 {
			return errors.New("list schema requires exactly one item schema")
		}
		if err := validateListIdentity(schema.Identity); err != nil {
			return err
		}
		return validateFieldSchema(*schema.Item, depth+1)
	case ValueKindObject:
		if schema.Item != nil || schema.Identity.Kind != "" || len(schema.Fields) == 0 {
			return errors.New("object schema requires fields and cannot declare list shape")
		}
		seen := make(map[FieldID]struct{}, len(schema.Fields))
		for _, field := range schema.Fields {
			if err := validateStableID("nested field ID", string(field.Field), maxCatalogIDLength); err != nil {
				return err
			}
			if _, duplicate := seen[field.Field]; duplicate {
				return fmt.Errorf("duplicate nested field %q", field.Field)
			}
			seen[field.Field] = struct{}{}
			if err := validateFieldSchema(field.Schema, depth+1); err != nil {
				return fmt.Errorf("nested field %q: %w", field.Field, err)
			}
		}
		return nil
	default:
		if schema.Item != nil || len(schema.Fields) != 0 || schema.Identity.Kind != "" {
			return fmt.Errorf("scalar schema %q cannot contain nested shape", schema.Kind)
		}
		return nil
	}
}

func validateListIdentity(identity ListIdentityRule) error {
	switch identity.Kind {
	case ListIdentityPositional:
		if identity.Field != "" || len(identity.Handles) != 0 {
			return errors.New("positional list identity cannot carry handles or a field")
		}
	case ListIdentityValue:
		if identity.Field != "" || len(identity.Handles) != 0 {
			return errors.New("value list identity cannot carry handles or a field")
		}
	case ListIdentityField:
		if err := validateStableID("list identity field", string(identity.Field), maxCatalogIDLength); err != nil {
			return err
		}
		if len(identity.Handles) != 0 {
			return errors.New("field list identity cannot carry fixed handles")
		}
	case ListIdentityFixed:
		if identity.Field != "" || len(identity.Handles) == 0 {
			return errors.New("fixed list identity requires handles and no field")
		}
		seen := make(map[RelationItemID]struct{}, len(identity.Handles))
		for _, handle := range identity.Handles {
			if err := validateStableID("fixed list handle", string(handle), maxHandleLength); err != nil {
				return err
			}
			if _, duplicate := seen[handle]; duplicate {
				return fmt.Errorf("duplicate fixed list handle %q", handle)
			}
			seen[handle] = struct{}{}
		}
	default:
		return fmt.Errorf("unsupported list identity %q", identity.Kind)
	}
	return nil
}

func validateFieldRuleShape(field FieldID, valueKind ValueKind, ownership FieldOwnership, translatable, file bool) error {
	if file {
		if translatable || (ownership != FieldOwnershipShared && ownership != FieldOwnershipSource) || valueKind != "" {
			return fmt.Errorf("file field %q must be shared or source-owned and cannot declare a scalar kind", field)
		}
		return nil
	}
	if ownership != FieldOwnershipShared && ownership != FieldOwnershipSource && ownership != FieldOwnershipLocale {
		return fmt.Errorf("field %q has unsupported ownership %q", field, ownership)
	}
	if translatable && ownership != FieldOwnershipLocale {
		return fmt.Errorf("translatable field %q must be locale-owned", field)
	}
	if err := (Value{Kind: valueKind}).validateKindOnly(); err != nil {
		return fmt.Errorf("field %q: %w", field, err)
	}
	return nil
}

func (v Value) validateKindOnly() error {
	switch v.Kind {
	case ValueKindText, ValueKindBoolean, ValueKindNumber, ValueKindInline, ValueKindList, ValueKindObject:
		return nil
	default:
		return fmt.Errorf("unsupported value kind %q", v.Kind)
	}
}

func validateDocumentCatalogValues(catalog Catalog, nodes []Node) error {
	index := newCatalogIndex(catalog)
	for _, node := range nodes {
		if !index.hasBlockKind(node.Kind) {
			return fmt.Errorf("block %q has unknown kind %q", node.ID, node.Kind)
		}
		lookup := func(field FieldID) (FieldRule, bool) { return index.field(node.Kind, field) }
		if err := validateFieldSet(fmt.Sprintf("block %q", node.ID), node.Shared, node.Localized, node.Files, lookup); err != nil {
			return err
		}
		if err := validateRelations(index, node); err != nil {
			return err
		}
	}
	return nil
}

func validateRelations(index catalogIndex, node Node) error {
	for _, relation := range node.Relations {
		allowed, ok := index.relationItemKinds(node.Kind, relation.ID)
		if !ok {
			return fmt.Errorf("block %q relation %q does not match the catalog", node.ID, relation.ID)
		}
		for _, item := range relation.Items {
			if _, ok := allowed[item.Kind]; !ok {
				return fmt.Errorf("relation item %q kind %q does not match the catalog", item.ID, item.Kind)
			}
			lookup := func(field FieldID) (FieldRule, bool) {
				return index.relationField(node.Kind, relation.ID, item.Kind, field)
			}
			if err := validateFieldSet(fmt.Sprintf("relation item %q", item.ID), item.Shared, item.Localized, item.Files, lookup); err != nil {
				return err
			}
		}
	}
	return nil
}

// fieldRuleLookup resolves the catalog rule of one block's or relation item's
// field.
type fieldRuleLookup func(FieldID) (FieldRule, bool)

// valueScope names the half of a field set a value is stored in, which its
// rule's ownership must agree with.
type valueScope string

const (
	sharedScope    valueScope = "shared"
	localizedScope valueScope = "localized"
)

func (s valueScope) owns(ownership FieldOwnership) bool {
	if s == localizedScope {
		return ownership == FieldOwnershipLocale
	}
	return ownership == FieldOwnershipShared || ownership == FieldOwnershipSource
}

// validateFieldSet checks the values and file bindings of one block or
// relation item, named by owner in errors, against its catalog rules.
func validateFieldSet(owner string, shared, localized []FieldValue, files []FileBinding, lookup fieldRuleLookup) error {
	if err := validateScopedValues(owner, sharedScope, shared, lookup); err != nil {
		return err
	}
	if err := validateScopedValues(owner, localizedScope, localized, lookup); err != nil {
		return err
	}
	return validateCatalogFiles(owner, files, lookup)
}

func validateScopedValues(owner string, scope valueScope, values []FieldValue, lookup fieldRuleLookup) error {
	for _, field := range values {
		rule, ok := lookup(field.ID)
		if !ok || rule.File || !scope.owns(rule.Ownership) || rule.ValueKind != field.Value.Kind {
			return fmt.Errorf("%s %s field %q does not match the catalog", owner, scope, field.ID)
		}
		if rule.Schema != nil {
			if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
				return fmt.Errorf("%s %s field %q: %w", owner, scope, field.ID, err)
			}
		}
	}
	return nil
}

func validateCatalogFiles(owner string, files []FileBinding, lookup fieldRuleLookup) error {
	for _, file := range files {
		rule, ok := lookup(file.Field)
		if !ok {
			return fmt.Errorf("%s file field %q does not match the catalog", owner, file.Field)
		}
		resolved, err := resolveNestedFieldRule(rule, file.Path)
		if err != nil || !resolved.file {
			return fmt.Errorf("%s file target %q does not match the catalog", owner, nestedFieldKey(file.Field, file.Path))
		}
	}
	return nil
}

func validateValueForSchema(value Value, schema FieldSchema, depth int) error {
	if depth > 32 {
		return errors.New("typed value nesting exceeds 32 levels")
	}
	if schema.File {
		return errors.New("file schema requires a typed file binding")
	}
	if value.Kind != schema.Kind {
		return fmt.Errorf("value kind %q does not match schema kind %q", value.Kind, schema.Kind)
	}
	if err := value.validateDepth(depth); err != nil {
		return err
	}
	switch schema.Kind {
	case ValueKindList:
		return validateListForSchema(value.List, schema, depth)
	case ValueKindObject:
		return validateObjectForSchema(value.Object, schema, depth)
	}
	return nil
}

func validateListForSchema(items []ListItem, schema FieldSchema, depth int) error {
	identity := schema.Identity
	stable := identity.Kind != ListIdentityPositional
	if len(items) > 0 && (items[0].ID != "") != stable {
		return fmt.Errorf("list identity %q does not match item handles", identity.Kind)
	}
	if identity.Kind == ListIdentityFixed {
		if len(items) != len(identity.Handles) {
			return errors.New("fixed list length does not match its catalog handles")
		}
		for index, item := range items {
			if item.ID != identity.Handles[index] {
				return fmt.Errorf("fixed list item %d must use handle %q", index, identity.Handles[index])
			}
		}
	}
	for _, item := range items {
		if err := validateListItemIdentity(item, identity); err != nil {
			return err
		}
		if err := validateValueForSchema(item.Value, *schema.Item, depth+1); err != nil {
			return fmt.Errorf("list item %q: %w", item.ID, err)
		}
	}
	return nil
}

// validateListItemIdentity requires a value- or field-identified item's
// handle to equal the scalar it is identified by.
func validateListItemIdentity(item ListItem, identity ListIdentityRule) error {
	switch identity.Kind {
	case ListIdentityValue:
		if item.Value.Kind != ValueKindText && item.Value.Kind != ValueKindNumber {
			return errors.New("value-identified list items must be canonical text or number scalars")
		}
		if item.ID != RelationItemID(item.Value.Text) {
			return errors.New("value-identified list item handle must equal its canonical scalar value")
		}
	case ListIdentityField:
		value, ok := objectField(item.Value, identity.Field)
		if !ok || value.Kind != ValueKindText || item.ID != RelationItemID(value.Text) {
			return fmt.Errorf("field-identified list item handle must equal text field %q", identity.Field)
		}
	}
	return nil
}

func validateObjectForSchema(fields []ObjectField, schema FieldSchema, depth int) error {
	rules := make(map[FieldID]FieldSchema, len(schema.Fields))
	for _, field := range schema.Fields {
		rules[field.Field] = field.Schema
	}
	for _, field := range fields {
		rule, ok := rules[field.ID]
		if !ok {
			return fmt.Errorf("object field %q is not in the catalog", field.ID)
		}
		if rule.File {
			return fmt.Errorf("object file field %q requires a typed file binding", field.ID)
		}
		if err := validateValueForSchema(field.Value, rule, depth+1); err != nil {
			return fmt.Errorf("object field %q: %w", field.ID, err)
		}
	}
	return nil
}

func objectField(value Value, fieldID FieldID) (Value, bool) {
	if value.Kind != ValueKindObject {
		return Value{}, false
	}
	for _, field := range value.Object {
		if field.ID == fieldID {
			return field.Value, true
		}
	}
	return Value{}, false
}
