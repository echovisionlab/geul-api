package aidocument

import (
	"errors"
	"fmt"
	"strings"
)

func (i DocumentIdentity) validate() error {
	if _, ok := supportedDomainSet[i.Domain]; !ok {
		return fmt.Errorf("unsupported document domain %q", i.Domain)
	}
	if err := validateOpaque("document reference", string(i.Reference), 256); err != nil {
		return err
	}
	return nil
}

func validateLocale(locale Locale) error {
	value := string(locale)
	if value == "" || len(value) > 35 {
		return fmt.Errorf("locale must contain 1 to 35 characters")
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (character == '-' && index > 0 && index < len(value)-1) {
			continue
		}
		return fmt.Errorf("locale %q is not a canonical locale identifier", locale)
	}
	return nil
}

func validateOpaque(name, value string, max int) error {
	if value == "" || len(value) > max {
		return fmt.Errorf("%s must contain 1 to %d characters", name, max)
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("%s contains whitespace outside its opaque value", name)
	}
	return nil
}

// Length limits for stable protocol identities.
const (
	// maxHandleLength bounds document instance handles: block, relation item,
	// list item and placeholder IDs.
	maxHandleLength = 160
	// maxCatalogIDLength bounds field and relation IDs declared by the catalog.
	maxCatalogIDLength = 120
	// maxKindLength bounds block and relation item kinds.
	maxKindLength = 80
)

// Stable protocol identities must not be array positions or paths derived
// from positions. Domain adapters have to map positional persistence shapes to
// stable semantic handles before entering this package.
func validateStableID(name, value string, max int) error {
	if err := validateOpaque(name, value, max); err != nil {
		return err
	}
	if strings.ContainsAny(value, "[]") {
		return fmt.Errorf("%s %q contains a positional path", name, value)
	}
	for _, segment := range strings.FieldsFunc(value, func(character rune) bool {
		return character == '/' || character == '.' || character == ':'
	}) {
		allDigits := segment != ""
		for _, character := range segment {
			if character < '0' || character > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return fmt.Errorf("%s %q contains a positional index", name, value)
		}
	}
	return nil
}

func (d Document) validate() error {
	if err := d.Identity.validate(); err != nil {
		return err
	}
	if err := validateOpaque("document revision", string(d.DocumentRevision), 256); err != nil {
		return err
	}
	if err := validateLocale(d.SourceLocale); err != nil {
		return fmt.Errorf("source locale: %w", err)
	}
	if err := validateLocale(d.Locale); err != nil {
		return fmt.Errorf("requested locale: %w", err)
	}
	if err := validateLocaleState(d.Role(), d.LocaleExists, d.TargetRevision); err != nil {
		return err
	}
	if d.TargetRevision != nil {
		if err := validateOpaque("target revision", string(*d.TargetRevision), 256); err != nil {
			return err
		}
	}
	if d.Role() == LocaleRoleNonSource && !d.LocaleExists {
		if err := validateAbsentLocaleValues(d.Nodes); err != nil {
			return err
		}
	}
	if err := d.Catalog.validate(); err != nil {
		return err
	}
	if err := validateNodes(d.Nodes); err != nil {
		return err
	}
	return validateDocumentCatalogValues(d.Catalog, d.Nodes)
}

// validateLocaleState checks the locale existence and target revision
// combination that a locale role permits.
func validateLocaleState(role LocaleRole, exists bool, target *Revision) error {
	if role == LocaleRoleSource && !exists {
		return errors.New("source locale must exist")
	}
	if role == LocaleRoleSource && target != nil {
		return errors.New("source locale cannot carry a target revision")
	}
	if role == LocaleRoleNonSource && exists && target == nil {
		return errors.New("existing target locale requires a target revision")
	}
	if role == LocaleRoleNonSource && !exists && target != nil {
		return errors.New("absent target locale cannot carry a target revision")
	}
	return nil
}

// validateAbsentLocaleValues rejects locale-owned values in nodes read for a
// translation locale that does not exist yet.
func validateAbsentLocaleValues(nodes []Node) error {
	for _, node := range nodes {
		if len(node.Localized) != 0 {
			return errors.New("absent translation locale cannot contain locale-owned values")
		}
		for _, relation := range node.Relations {
			for _, item := range relation.Items {
				if len(item.Localized) != 0 {
					return errors.New("absent translation locale cannot contain relation-item locale-owned values")
				}
			}
		}
	}
	return nil
}

// validateNodes extends the projected shape checks, which already reject
// parent cycles among the given nodes, with the full-document requirement that
// every parent is present.
func validateNodes(nodes []Node) error {
	if err := validateProjectedNodes(nodes); err != nil {
		return err
	}
	ids := make(map[BlockID]struct{}, len(nodes))
	for _, node := range nodes {
		ids[node.ID] = struct{}{}
	}
	for _, node := range nodes {
		if node.Parent == "" {
			continue
		}
		if _, ok := ids[node.Parent]; !ok {
			return fmt.Errorf("block %q has missing parent %q", node.ID, node.Parent)
		}
	}
	return nil
}

func validateProjectedNodes(nodes []Node) error {
	parents := make(map[BlockID]BlockID, len(nodes))
	for _, node := range nodes {
		if err := validateStableID("block ID", string(node.ID), maxHandleLength); err != nil {
			return err
		}
		if _, duplicate := parents[node.ID]; duplicate {
			return fmt.Errorf("duplicate block ID %q", node.ID)
		}
		parents[node.ID] = node.Parent
		if err := validateProjectedNode(node); err != nil {
			return err
		}
	}
	return detectParentCycles(nodes, parents)
}

func validateProjectedNode(node Node) error {
	if node.Parent != "" {
		if err := validateStableID("parent block ID", string(node.Parent), maxHandleLength); err != nil {
			return err
		}
	}
	if node.Order < 0 {
		return fmt.Errorf("block %q has negative order", node.ID)
	}
	if err := validateProjectedFieldSet(fmt.Sprintf("block %q", node.ID), node.Shared, node.Localized, node.Files); err != nil {
		return err
	}
	return validateProjectedRelations(node)
}

func validateProjectedRelations(node Node) error {
	relations := make(map[RelationID]struct{}, len(node.Relations))
	items := make(map[RelationItemID]struct{})
	for _, relation := range node.Relations {
		if err := validateStableID("relation ID", string(relation.ID), maxCatalogIDLength); err != nil {
			return err
		}
		if _, duplicate := relations[relation.ID]; duplicate {
			return fmt.Errorf("block %q has duplicate relation %q", node.ID, relation.ID)
		}
		relations[relation.ID] = struct{}{}
		for _, item := range relation.Items {
			if err := validateStableID("relation item ID", string(item.ID), maxHandleLength); err != nil {
				return err
			}
			if err := validateStableID("relation item kind", string(item.Kind), maxKindLength); err != nil {
				return err
			}
			if _, duplicate := items[item.ID]; duplicate {
				return fmt.Errorf("block %q has duplicate relation item ID %q", node.ID, item.ID)
			}
			items[item.ID] = struct{}{}
			if item.Order < 0 {
				return fmt.Errorf("relation item %q has negative order", item.ID)
			}
			if err := validateProjectedFieldSet(fmt.Sprintf("relation item %q", item.ID), item.Shared, item.Localized, item.Files); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateProjectedFieldSet checks the catalog-independent shape of one
// block's or relation item's values and file bindings.
func validateProjectedFieldSet(owner string, shared, localized []FieldValue, files []FileBinding) error {
	if err := validateFieldValues(shared); err != nil {
		return fmt.Errorf("%s shared fields: %w", owner, err)
	}
	if err := validateFieldValues(localized); err != nil {
		return fmt.Errorf("%s localized fields: %w", owner, err)
	}
	return validateFileBindings(owner, files)
}

func validateFileBindings(owner string, files []FileBinding) error {
	targets := make(map[string]struct{}, len(files))
	for _, binding := range files {
		key := nestedFieldKey(binding.Field, binding.Path)
		if _, duplicate := targets[key]; duplicate {
			return fmt.Errorf("%s has duplicate file target %q", owner, key)
		}
		targets[key] = struct{}{}
		if err := validateStableID("file field ID", string(binding.Field), maxCatalogIDLength); err != nil {
			return err
		}
		for _, segment := range binding.Path {
			if err := segment.validate(); err != nil {
				return err
			}
		}
		if err := validateFileReference(binding.File); err != nil {
			return err
		}
	}
	return nil
}

// detectParentCycles follows parent links among the given nodes only. A
// projection may omit ancestors, so a parent outside it ends the walk.
func detectParentCycles(nodes []Node, parents map[BlockID]BlockID) error {
	for _, node := range nodes {
		visited := map[BlockID]struct{}{node.ID: {}}
		for parent := node.Parent; parent != ""; parent = parents[parent] {
			if _, included := parents[parent]; !included {
				break
			}
			if _, cycle := visited[parent]; cycle {
				return fmt.Errorf("block %q is part of a parent cycle", node.ID)
			}
			visited[parent] = struct{}{}
		}
	}
	return nil
}

func nestedFieldKey(field FieldID, path []FieldPathSegment) string {
	key := string(field)
	for _, segment := range path {
		if segment.Field != "" {
			key += "/field:" + string(segment.Field)
		} else {
			key += "/item:" + string(segment.Item)
		}
	}
	return key
}

func validateFieldValues(fields []FieldValue) error {
	seen := make(map[FieldID]struct{}, len(fields))
	for _, field := range fields {
		if err := validateStableID("field ID", string(field.ID), maxCatalogIDLength); err != nil {
			return err
		}
		if _, duplicate := seen[field.ID]; duplicate {
			return fmt.Errorf("duplicate field %q", field.ID)
		}
		seen[field.ID] = struct{}{}
		if err := field.Value.validate(); err != nil {
			return fmt.Errorf("field %q: %w", field.ID, err)
		}
	}
	return nil
}

func validateFileReference(reference FileReference) error {
	value := string(reference)
	if err := validateOpaque("file reference", value, 256); err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		return errors.New("inline data file payloads are forbidden")
	}
	return nil
}
