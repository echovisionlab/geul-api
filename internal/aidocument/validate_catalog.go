package aidocument

import (
	"errors"
	"fmt"
)

func (c Catalog) validate() error {
	if err := validateOpaque("catalog fingerprint", c.Fingerprint, 256); err != nil {
		return err
	}
	kinds := make(map[BlockKind]struct{}, len(c.BlockKinds))
	for _, kind := range c.BlockKinds {
		if err := validateStableID("block kind", string(kind), 80); err != nil {
			return err
		}
		if _, duplicate := kinds[kind]; duplicate {
			return fmt.Errorf("duplicate block kind %q", kind)
		}
		kinds[kind] = struct{}{}
	}
	rules := make(map[string]struct{}, len(c.Fields))
	for _, rule := range c.Fields {
		if _, ok := kinds[rule.BlockKind]; !ok {
			return fmt.Errorf("field %q refers to unknown block kind %q", rule.Field, rule.BlockKind)
		}
		if err := validateStableID("field ID", string(rule.Field), 120); err != nil {
			return err
		}
		key := string(rule.BlockKind) + "\x00" + string(rule.Field)
		if _, duplicate := rules[key]; duplicate {
			return fmt.Errorf("duplicate field rule %q on %q", rule.Field, rule.BlockKind)
		}
		rules[key] = struct{}{}
		if err := validateFieldRuleShape(rule.Field, rule.ValueKind, rule.Ownership, rule.Translatable, rule.File); err != nil {
			return err
		}
		if rule.Schema != nil {
			if rule.Schema.Kind != rule.ValueKind || rule.Schema.Ownership != rule.Ownership || rule.Schema.Translatable != rule.Translatable || rule.Schema.File != rule.File {
				return fmt.Errorf("field %q recursive schema root does not match its field rule", rule.Field)
			}
			if err := validateFieldSchema(*rule.Schema, 0); err != nil {
				return fmt.Errorf("field %q schema: %w", rule.Field, err)
			}
		}
	}
	relations := make(map[string]map[RelationItemKind]struct{}, len(c.Relations))
	for _, rule := range c.Relations {
		if _, ok := kinds[rule.BlockKind]; !ok {
			return fmt.Errorf("relation %q refers to unknown block kind %q", rule.Relation, rule.BlockKind)
		}
		if err := validateStableID("relation ID", string(rule.Relation), 120); err != nil {
			return err
		}
		key := relationRuleKey(rule.BlockKind, rule.Relation)
		if _, duplicate := relations[key]; duplicate {
			return fmt.Errorf("duplicate relation rule %q on %q", rule.Relation, rule.BlockKind)
		}
		itemKinds := make(map[RelationItemKind]struct{}, len(rule.ItemKinds))
		for _, kind := range rule.ItemKinds {
			if err := validateStableID("relation item kind", string(kind), 80); err != nil {
				return err
			}
			if _, duplicate := itemKinds[kind]; duplicate {
				return fmt.Errorf("duplicate relation item kind %q on relation %q", kind, rule.Relation)
			}
			itemKinds[kind] = struct{}{}
		}
		if len(itemKinds) == 0 {
			return fmt.Errorf("relation %q must allow at least one item kind", rule.Relation)
		}
		relations[key] = itemKinds
	}
	relationFields := make(map[string]struct{}, len(c.RelationFields))
	for _, rule := range c.RelationFields {
		itemKinds, ok := relations[relationRuleKey(rule.BlockKind, rule.Relation)]
		if !ok {
			return fmt.Errorf("relation field %q refers to unknown relation %q", rule.Field, rule.Relation)
		}
		if _, ok := itemKinds[rule.ItemKind]; !ok {
			return fmt.Errorf("relation field %q refers to unsupported item kind %q", rule.Field, rule.ItemKind)
		}
		if err := validateStableID("relation field ID", string(rule.Field), 120); err != nil {
			return err
		}
		key := relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)
		if _, duplicate := relationFields[key]; duplicate {
			return fmt.Errorf("duplicate relation field rule %q", rule.Field)
		}
		relationFields[key] = struct{}{}
		if err := validateFieldRuleShape(rule.Field, rule.ValueKind, rule.Ownership, rule.Translatable, rule.File); err != nil {
			return err
		}
		if rule.Schema != nil {
			if rule.Schema.Kind != rule.ValueKind || rule.Schema.Ownership != rule.Ownership || rule.Schema.Translatable != rule.Translatable || rule.Schema.File != rule.File {
				return fmt.Errorf("relation field %q recursive schema root does not match its field rule", rule.Field)
			}
			if err := validateFieldSchema(*rule.Schema, 0); err != nil {
				return fmt.Errorf("relation field %q schema: %w", rule.Field, err)
			}
		}
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
			if err := validateStableID("nested field ID", string(field.Field), 120); err != nil {
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
		if err := validateStableID("list identity field", string(identity.Field), 120); err != nil {
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
			if err := validateStableID("fixed list handle", string(handle), 160); err != nil {
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
	kinds := make(map[BlockKind]struct{}, len(catalog.BlockKinds))
	for _, kind := range catalog.BlockKinds {
		kinds[kind] = struct{}{}
	}
	rules := make(map[string]FieldRule, len(catalog.Fields))
	for _, rule := range catalog.Fields {
		rules[fieldRuleKey(rule.BlockKind, rule.Field)] = rule
	}
	relations := make(map[string]map[RelationItemKind]struct{}, len(catalog.Relations))
	for _, rule := range catalog.Relations {
		allowed := make(map[RelationItemKind]struct{}, len(rule.ItemKinds))
		for _, kind := range rule.ItemKinds {
			allowed[kind] = struct{}{}
		}
		relations[relationRuleKey(rule.BlockKind, rule.Relation)] = allowed
	}
	relationFields := make(map[string]RelationFieldRule, len(catalog.RelationFields))
	for _, rule := range catalog.RelationFields {
		relationFields[relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)] = rule
	}
	for _, node := range nodes {
		if _, ok := kinds[node.Kind]; !ok {
			return fmt.Errorf("block %q has unknown kind %q", node.ID, node.Kind)
		}
		for _, field := range node.Shared {
			rule, ok := rules[fieldRuleKey(node.Kind, field.ID)]
			if !ok || rule.File || (rule.Ownership != FieldOwnershipShared && rule.Ownership != FieldOwnershipSource) || rule.ValueKind != field.Value.Kind {
				return fmt.Errorf("block %q shared field %q does not match the catalog", node.ID, field.ID)
			}
			if rule.Schema != nil {
				if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
					return fmt.Errorf("block %q shared field %q: %w", node.ID, field.ID, err)
				}
			}
		}
		for _, field := range node.Localized {
			rule, ok := rules[fieldRuleKey(node.Kind, field.ID)]
			if !ok || rule.File || rule.Ownership != FieldOwnershipLocale || rule.ValueKind != field.Value.Kind {
				return fmt.Errorf("block %q localized field %q does not match the catalog", node.ID, field.ID)
			}
			if rule.Schema != nil {
				if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
					return fmt.Errorf("block %q localized field %q: %w", node.ID, field.ID, err)
				}
			}
		}
		for _, file := range node.Files {
			rule, ok := rules[fieldRuleKey(node.Kind, file.Field)]
			if !ok {
				return fmt.Errorf("block %q file field %q does not match the catalog", node.ID, file.Field)
			}
			resolved, err := resolveNestedFieldRule(rule.ValueKind, rule.Ownership, rule.Translatable, rule.File, rule.Schema, file.Path)
			if err != nil || !resolved.file {
				return fmt.Errorf("block %q file target %q does not match the catalog", node.ID, nestedFieldKey(file.Field, file.Path))
			}
		}
		for _, relation := range node.Relations {
			allowed, ok := relations[relationRuleKey(node.Kind, relation.ID)]
			if !ok {
				return fmt.Errorf("block %q relation %q does not match the catalog", node.ID, relation.ID)
			}
			for _, item := range relation.Items {
				if _, ok := allowed[item.Kind]; !ok {
					return fmt.Errorf("relation item %q kind %q does not match the catalog", item.ID, item.Kind)
				}
				for _, field := range item.Shared {
					rule, ok := relationFields[relationFieldRuleKey(node.Kind, relation.ID, item.Kind, field.ID)]
					if !ok || rule.File || (rule.Ownership != FieldOwnershipShared && rule.Ownership != FieldOwnershipSource) || rule.ValueKind != field.Value.Kind {
						return fmt.Errorf("relation item %q shared field %q does not match the catalog", item.ID, field.ID)
					}
					if rule.Schema != nil {
						if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
							return fmt.Errorf("relation item %q shared field %q: %w", item.ID, field.ID, err)
						}
					}
				}
				for _, field := range item.Localized {
					rule, ok := relationFields[relationFieldRuleKey(node.Kind, relation.ID, item.Kind, field.ID)]
					if !ok || rule.File || rule.Ownership != FieldOwnershipLocale || rule.ValueKind != field.Value.Kind {
						return fmt.Errorf("relation item %q localized field %q does not match the catalog", item.ID, field.ID)
					}
					if rule.Schema != nil {
						if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
							return fmt.Errorf("relation item %q localized field %q: %w", item.ID, field.ID, err)
						}
					}
				}
				for _, file := range item.Files {
					rule, ok := relationFields[relationFieldRuleKey(node.Kind, relation.ID, item.Kind, file.Field)]
					if !ok {
						return fmt.Errorf("relation item %q file field %q does not match the catalog", item.ID, file.Field)
					}
					resolved, err := resolveNestedFieldRule(rule.ValueKind, rule.Ownership, rule.Translatable, rule.File, rule.Schema, file.Path)
					if err != nil || !resolved.file {
						return fmt.Errorf("relation item %q file target %q does not match the catalog", item.ID, nestedFieldKey(file.Field, file.Path))
					}
				}
			}
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
		stable := schema.Identity.Kind != ListIdentityPositional
		if len(value.List) > 0 && (value.List[0].ID != "") != stable {
			return fmt.Errorf("list identity %q does not match item handles", schema.Identity.Kind)
		}
		if schema.Identity.Kind == ListIdentityFixed {
			if len(value.List) != len(schema.Identity.Handles) {
				return errors.New("fixed list length does not match its catalog handles")
			}
			for index, item := range value.List {
				if item.ID != schema.Identity.Handles[index] {
					return fmt.Errorf("fixed list item %d must use handle %q", index, schema.Identity.Handles[index])
				}
			}
		}
		for _, item := range value.List {
			if schema.Identity.Kind == ListIdentityValue {
				if item.Value.Kind != ValueKindText && item.Value.Kind != ValueKindNumber {
					return errors.New("value-identified list items must be canonical text or number scalars")
				}
				if item.ID != RelationItemID(item.Value.Text) {
					return errors.New("value-identified list item handle must equal its canonical scalar value")
				}
			}
			if schema.Identity.Kind == ListIdentityField {
				identity, ok := objectField(item.Value, schema.Identity.Field)
				if !ok || identity.Kind != ValueKindText || item.ID != RelationItemID(identity.Text) {
					return fmt.Errorf("field-identified list item handle must equal text field %q", schema.Identity.Field)
				}
			}
			if err := validateValueForSchema(item.Value, *schema.Item, depth+1); err != nil {
				return fmt.Errorf("list item %q: %w", item.ID, err)
			}
		}
	case ValueKindObject:
		rules := make(map[FieldID]FieldSchema, len(schema.Fields))
		for _, field := range schema.Fields {
			rules[field.Field] = field.Schema
		}
		for _, field := range value.Object {
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
