package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// AIDocumentItem is Menu's domain-owned, transport-neutral item tree. The root
// owns stable structure and presentation fields; locale-owned label values are
// projected separately through AIDocumentSnapshot.Labels.
type AIDocumentItem struct {
	ID               string
	Label            string
	LinkType         string
	URL              *string
	TargetID         *string
	TargetSlug       *string
	OpenInNewTab     *bool
	VisibilityMode   *string
	VisibilityRoles  []string
	LocalizationMode *string
	FixedLocale      *string
	Children         []AIDocumentItem
}

// AIDocumentSnapshot is the exact Menu aggregate observed by DCDP. Labels is
// values-only: map absence means missing and a present empty string is an
// explicit empty locale value.
type AIDocumentSnapshot struct {
	ID           string
	Name         string
	SourceLocale string
	Locale       string
	LocaleExists bool
	// SourceValuesStored is true only when the current source locale has its
	// own values-only row. The source path never borrows an older root label.
	SourceValuesStored bool
	DocumentRevision   string
	TargetRevision     *string
	LocaleUpdatedAt    time.Time
	Items              []AIDocumentItem
	Labels             map[string]string
	contentDocumentID  uuid.UUID
	rootUpdatedAt      time.Time
	sourceLabels       map[string]string
}

type compiledMenuAIDocument struct {
	snapshot            AIDocumentSnapshot
	changed             bool
	sourceChanged       bool
	sourceValuesChanged bool
	targetChanged       bool
	deleteTranslation   bool
}

func (s *MenuService) compileAIDocument(
	snapshot AIDocumentSnapshot,
	operations []AIDocumentOperation,
) (compiledMenuAIDocument, []AIDocumentIssue) {
	working := cloneMenuAIDocumentSnapshot(snapshot)
	result := compiledMenuAIDocument{snapshot: working}
	graph, err := newMenuAIDocumentGraph(working.ID, working.Items)
	if err != nil {
		return result, []AIDocumentIssue{{Operation: -1, Code: AIDocumentIssueInvalidOperation, Handle: "menu:" + working.ID, Message: err.Error()}}
	}
	isSource := working.Locale == working.SourceLocale
	bootstrapTarget := !isSource && !working.LocaleExists && menuAIDocumentCreatesTarget(operations)
	if bootstrapTarget {
		working.Labels = seedMenuTargetLabels(working)
	}
	for index, operation := range operations {
		issue := func(handle, message string) []AIDocumentIssue {
			return []AIDocumentIssue{{Operation: index, Code: AIDocumentIssueInvalidOperation, Handle: handle, Message: message}}
		}
		targetIssue := func(handle, message string) []AIDocumentIssue {
			return []AIDocumentIssue{{Operation: index, Code: AIDocumentIssueTargetForbidden, Handle: handle, Message: message}}
		}
		switch operation.Kind {
		case AIDocumentSetName:
			if !isSource {
				return result, targetIssue("menu:"+working.ID, "only the source locale may change the Menu name")
			}
			if operation.Value.Kind != AIDocumentText {
				return result, issue("menu:"+working.ID+"/name", "Menu name requires text")
			}
			if working.Name != operation.Value.Text {
				working.Name = operation.Value.Text
				result.changed, result.sourceChanged = true, true
			}
		case AIDocumentNoop:
			// The Menu catalog has one item kind. A shared kind replacement
			// normalized to the same menu_item kind is intentionally a no-op.
		case AIDocumentSetItemField, AIDocumentUnsetItemField:
			item, ok := graph.items[operation.ItemID]
			if !ok {
				return result, issue("item:"+operation.ItemID, "Menu item does not exist")
			}
			if operation.Field == "label" {
				if operation.Kind == AIDocumentSetItemField && operation.Value.Kind != AIDocumentText {
					return result, issue("item:"+operation.ItemID+"/label", "Menu item label requires text")
				}
				if operation.Kind == AIDocumentSetItemField && len(operation.Value.Text) > menuItemLabelMaxLength {
					return result, issue(
						"item:"+operation.ItemID+"/label",
						fmt.Sprintf("Menu item label must be at most %d characters", menuItemLabelMaxLength),
					)
				}
				if !itemOwnsLocaleLabel(item, working.Locale) {
					return result, targetIssue("item:"+operation.ItemID+"/label", "fixed-locale Menu item labels may be edited only in their fixed locale")
				}
				if isSource && !working.SourceValuesStored {
					return result, issue("translation:"+working.Locale, "Menu source locale values are not initialized")
				}
				current, exists := working.Labels[operation.ItemID]
				if operation.Kind == AIDocumentUnsetItemField {
					if exists {
						delete(working.Labels, operation.ItemID)
						result.changed = true
						if isSource {
							result.sourceValuesChanged = true
						} else {
							result.targetChanged = true
						}
					}
				} else if !exists || current != operation.Value.Text {
					working.Labels[operation.ItemID] = operation.Value.Text
					result.changed = true
					if isSource {
						result.sourceValuesChanged = true
					} else {
						result.targetChanged = true
					}
				}
				continue
			}
			if !isSource {
				return result, targetIssue("item:"+operation.ItemID+"/"+operation.Field, "non-source locales may change only translated labels")
			}
			changed, fieldErr := applyMenuAIDocumentSourceField(item, operation)
			if fieldErr != nil {
				return result, issue("item:"+operation.ItemID+"/"+operation.Field, fieldErr.Error())
			}
			if changed {
				result.changed, result.sourceChanged = true, true
			}
		case AIDocumentInsertItem:
			if !isSource {
				return result, targetIssue("item:"+operation.ItemID, "only the source locale may insert Menu items")
			}
			if err := graph.insert(operation.ItemID, operation.ParentID, operation.AfterID); err != nil {
				return result, issue("item:"+operation.ItemID, err.Error())
			}
			result.changed, result.sourceChanged = true, true
		case AIDocumentDeleteItem:
			if !isSource {
				return result, targetIssue("item:"+operation.ItemID, "only the source locale may delete Menu items")
			}
			if err := graph.delete(operation.ItemID); err != nil {
				return result, issue("item:"+operation.ItemID, err.Error())
			}
			result.changed, result.sourceChanged = true, true
		case AIDocumentMoveItem:
			if !isSource {
				return result, targetIssue("item:"+operation.ItemID, "only the source locale may move Menu items")
			}
			changed, err := graph.move(operation.ItemID, operation.ParentID, operation.AfterID)
			if err != nil {
				return result, issue("item:"+operation.ItemID, err.Error())
			}
			if changed {
				result.changed, result.sourceChanged = true, true
			}
		case AIDocumentCreateTranslation:
			if isSource || working.LocaleExists || len(operations) != 1 {
				return result, issue("translation:"+working.Locale, "translation create requires one absent non-source locale")
			}
			working.LocaleExists = true
			result.changed, result.targetChanged = true, true
		case AIDocumentDeleteTranslation:
			if isSource || !working.LocaleExists || len(operations) != 1 {
				return result, issue("translation:"+working.Locale, "translation delete requires one existing non-source locale")
			}
			working.LocaleExists = false
			working.Labels = map[string]string{}
			result.changed, result.targetChanged, result.deleteTranslation = true, true, true
		default:
			return result, issue("menu:"+working.ID, "unsupported Menu AI document operation")
		}
	}
	if bootstrapTarget && !working.LocaleExists {
		working.LocaleExists = true
		result.changed, result.targetChanged = true, true
	}

	working.Items, err = graph.build()
	if err != nil {
		return result, []AIDocumentIssue{{Operation: -1, Code: AIDocumentIssueInvalidOperation, Handle: "menu:" + working.ID, Message: err.Error()}}
	}
	result.snapshot = working
	if result.sourceChanged {
		validationOperation := lastMenuAIDocumentMutationIndex(operations)
		if err := s.validateMenuName(working.Name); err != nil {
			return result, []AIDocumentIssue{{Operation: validationOperation, Code: AIDocumentIssueInvalidOperation, Handle: "menu:" + working.ID + "/name", Message: err.Error()}}
		}
		if err := validateAIDocumentItemConfiguration(working.Items); err != nil {
			return result, []AIDocumentIssue{{Operation: validationOperation, Code: AIDocumentIssueInvalidOperation, Handle: "menu:" + working.ID + "/items", Message: err.Error()}}
		}
		validationItems := aiDocumentItemsToModel(working.Items)
		fillMissingMenuLabelsForStructuralValidation(validationItems)
		if err := s.validateMenuItems(s.modelItemsToProto(validationItems), 0); err != nil {
			return result, []AIDocumentIssue{{Operation: validationOperation, Code: AIDocumentIssueInvalidOperation, Handle: "menu:" + working.ID + "/items", Message: err.Error()}}
		}
	}
	if result.sourceValuesChanged || result.targetChanged {
		if err := validateAIDocumentLocaleLabels(working.Labels); err != nil {
			return result, []AIDocumentIssue{{
				Operation: lastMenuAIDocumentMutationIndex(operations),
				Code:      AIDocumentIssueInvalidOperation,
				Handle:    "menu:" + working.ID + "/labels",
				Message:   err.Error(),
			}}
		}
	}
	return result, nil
}

func menuAIDocumentCreatesTarget(operations []AIDocumentOperation) bool {
	for _, operation := range operations {
		if operation.Kind == AIDocumentCreateTranslation ||
			(operation.Kind == AIDocumentSetItemField && operation.Field == "label") {
			return true
		}
	}
	return false
}

func seedMenuTargetLabels(snapshot AIDocumentSnapshot) map[string]string {
	labels := make(map[string]string)
	var collect func([]AIDocumentItem)
	collect = func(items []AIDocumentItem) {
		for index := range items {
			item := items[index]
			if itemOwnsLocaleLabel(&item, snapshot.Locale) {
				labels[item.ID] = snapshot.sourceLabels[item.ID]
			}
			collect(item.Children)
		}
	}
	collect(snapshot.Items)
	return labels
}

func fillMissingMenuLabelsForStructuralValidation(items []model.MenuItem) {
	for i := range items {
		if strings.TrimSpace(items[i].Label) == "" {
			items[i].Label = "_"
		}
		fillMissingMenuLabelsForStructuralValidation(items[i].Children)
	}
}

func validateAIDocumentLocaleLabels(labels map[string]string) error {
	for id, label := range labels {
		if len(label) > menuItemLabelMaxLength {
			return fmt.Errorf("menu item %q label must be at most %d characters", id, menuItemLabelMaxLength)
		}
	}
	return nil
}

func lastMenuAIDocumentMutationIndex(operations []AIDocumentOperation) int {
	for index := len(operations) - 1; index >= 0; index-- {
		if operations[index].Kind != AIDocumentNoop {
			return index
		}
	}
	return -1
}

func (s *MenuService) persistAIDocumentSource(
	ctx context.Context,
	tx *gorm.DB,
	previous AIDocumentSnapshot,
	next AIDocumentSnapshot,
	now time.Time,
) error {
	fields := structured.Fields{}
	if previous.Name != next.Name {
		name := next.Name
		if err := ensureUpdatedMenuNameAvailable(tx, previous.ID, &name); err != nil {
			return err
		}
		fields["name"] = next.Name
	}
	itemsChanged := !equalAIDocumentItems(previous.Items, next.Items)
	if itemsChanged {
		itemsJSON, err := json.Marshal(aiDocumentItemsToModel(next.Items))
		if err != nil {
			return errs.Internal(err)
		}
		fields["items"] = itemsJSON
	}
	if len(fields) == 0 {
		return nil
	}
	fields["updated_at"] = translation.NextTargetUpdatedAt(now, previous.rootUpdatedAt)
	var changedName *string
	if previous.Name != next.Name {
		changedName = &next.Name
	}
	if err := updateMenuRow(tx.WithContext(ctx), previous.ID, changedName, fields); err != nil {
		return err
	}
	if itemsChanged {
		if err := ReconcileTranslationRowsWithSourceItems(
			ctx, tx, previous.ID, aiDocumentItemsToModel(next.Items), now,
		); err != nil {
			return err
		}
	}
	return nil
}

func persistMenuAIDocumentSourceValues(
	ctx context.Context,
	tx *gorm.DB,
	previous AIDocumentSnapshot,
	next AIDocumentSnapshot,
	now time.Time,
) error {
	if !previous.SourceValuesStored || previous.Locale != previous.SourceLocale {
		return errs.FailedPrecondition("Menu source locale values are not initialized")
	}
	body, err := EncodeTranslationLabelValues(next.Labels)
	if err != nil {
		return errs.Internal(err)
	}
	result := tx.WithContext(ctx).Exec(
		`UPDATE menu_translation
		 SET items_json = ?, updated_at = ?
		 WHERE entity_id = ? AND locale = ?`,
		string(body), translation.NextTargetUpdatedAt(now, previous.LocaleUpdatedAt), previous.ID, previous.Locale,
	)
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("Menu source locale values are not initialized")
	}
	return nil
}

func persistMenuAIDocumentTarget(
	ctx context.Context,
	tx *gorm.DB,
	previous AIDocumentSnapshot,
	next AIDocumentSnapshot,
	deleteTranslation bool,
	now time.Time,
) error {
	if deleteTranslation {
		result := tx.WithContext(ctx).Exec(
			"DELETE FROM menu_translation WHERE entity_id = ? AND locale = ?",
			previous.ID, previous.Locale,
		)
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return errs.FailedPrecondition("Menu translation no longer exists")
		}
		return nil
	}
	labels := canonicalMenuAIDocumentLabels(next.Items, previous.Locale, next.Labels)
	body, err := EncodeTranslationLabelValues(labels)
	if err != nil {
		return errs.Internal(err)
	}
	nextUpdatedAt := now.UTC().Truncate(time.Microsecond)
	if previous.LocaleExists {
		var row struct {
			UpdatedAt time.Time `gorm:"column:updated_at"`
		}
		if err := tx.WithContext(ctx).Table("menu_translation").Select("updated_at").
			Where("entity_id = ? AND locale = ?", previous.ID, previous.Locale).Take(&row).Error; err != nil {
			return errs.Internal(err)
		}
		nextUpdatedAt = translation.NextTargetUpdatedAt(nextUpdatedAt, row.UpdatedAt)
	}
	result := tx.WithContext(ctx).Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (entity_id, locale) DO UPDATE SET
		 items_json = EXCLUDED.items_json, updated_at = EXCLUDED.updated_at`,
		previous.ID, previous.Locale, string(body), nextUpdatedAt, nextUpdatedAt,
	)
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	return nil
}

func canonicalMenuAIDocumentLabels(
	items []AIDocumentItem,
	locale string,
	labels map[string]string,
) map[string]string {
	canonical := make(map[string]string)
	var collect func([]AIDocumentItem)
	collect = func(current []AIDocumentItem) {
		for index := range current {
			item := current[index]
			if itemOwnsLocaleLabel(&item, locale) {
				if label, present := labels[item.ID]; present {
					canonical[item.ID] = label
				}
			}
			collect(item.Children)
		}
	}
	collect(items)
	return canonical
}

func appendMenuAIDocumentAudit(
	ctx context.Context,
	tx *gorm.DB,
	auditWriter domainaudit.Appender,
	previous AIDocumentSnapshot,
	compiled compiledMenuAIDocument,
) error {
	if compiled.targetChanged {
		operation := sharedtelemetry.AuditItemOperationUpdated
		switch {
		case !previous.LocaleExists && compiled.snapshot.LocaleExists:
			operation = sharedtelemetry.AuditItemOperationCreated
		case previous.LocaleExists && !compiled.snapshot.LocaleExists:
			operation = sharedtelemetry.AuditItemOperationDeleted
		}
		return domainaudit.AppendOptionalRequest(
			ctx, tx, auditWriter, sharedtelemetry.AuditMenuUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMenuLocaleContentAuditRecord(
					metadata, previous.ID, previous.Locale, operation,
				)
			},
		)
	}

	fields := make([]string, 0, 2)
	if previous.Name != compiled.snapshot.Name {
		fields = append(fields, "name")
	}
	if !equalAIDocumentItems(previous.Items, compiled.snapshot.Items) || compiled.sourceValuesChanged {
		fields = append(fields, "items")
	}
	sort.Strings(fields)
	return domainaudit.AppendOptionalRequest(
		ctx, tx, auditWriter, sharedtelemetry.AuditMenuUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, previous.ID, fields)
		},
	)
}

func loadMenuAIDocumentSnapshot(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	locale string,
	lock bool,
) (AIDocumentSnapshot, error) {
	menuID, locale = strings.TrimSpace(menuID), strings.TrimSpace(locale)
	if menuID == "" || locale == "" {
		return AIDocumentSnapshot{}, errs.InvalidArgument("document", "Menu ID and locale are required")
	}
	query := db.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var root model.Menu
	if err := query.First(&root, "id = ?", menuID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AIDocumentSnapshot{}, errs.NotFound("menu", menuID)
		}
		return AIDocumentSnapshot{}, errs.Internal(err)
	}
	return loadMenuAIDocumentSnapshotFromRoot(ctx, db, root, locale, lock)
}

func loadMenuAIDocumentSnapshotFromRoot(
	ctx context.Context,
	db *gorm.DB,
	root model.Menu,
	locale string,
	lock bool,
) (AIDocumentSnapshot, error) {
	menuID, locale := strings.TrimSpace(root.ID), strings.TrimSpace(locale)
	if menuID == "" || locale == "" {
		return AIDocumentSnapshot{}, errs.InvalidArgument("document", "Menu ID and locale are required")
	}

	document, err := loadMenuContentDocumentStateFromRoot(ctx, db, root, lock)
	if err != nil {
		return AIDocumentSnapshot{}, err
	}

	items, err := modelItemsToAIDocument(root.Items)
	if err != nil {
		return AIDocumentSnapshot{}, errs.Internal(err)
	}
	snapshot := AIDocumentSnapshot{
		ID: root.ID, Name: root.Name, SourceLocale: document.SourceLocale, Locale: locale,
		DocumentRevision: document.Revision.String(), contentDocumentID: document.ID,
		rootUpdatedAt: root.UpdatedAt, Items: items, Labels: map[string]string{},
	}
	sourceRow, sourceExists, err := loadMenuAIDocumentLocaleRow(
		ctx, db, menuID, document.SourceLocale, lock,
	)
	if err != nil {
		return AIDocumentSnapshot{}, err
	}
	if !sourceExists {
		return AIDocumentSnapshot{}, errs.FailedPrecondition("Menu source locale values are not initialized")
	}
	snapshot.sourceLabels = cloneMenuLabels(sourceRow.Labels)
	if locale == document.SourceLocale {
		snapshot.LocaleExists = true
		snapshot.SourceValuesStored = true
		snapshot.LocaleUpdatedAt = sourceRow.UpdatedAt
		snapshot.Labels = cloneMenuLabels(sourceRow.Labels)
	} else {
		targetRow, targetExists, err := loadMenuAIDocumentLocaleRow(ctx, db, menuID, locale, lock)
		if err != nil {
			return AIDocumentSnapshot{}, err
		}
		if targetExists {
			snapshot.LocaleExists = true
			snapshot.LocaleUpdatedAt = targetRow.UpdatedAt
			snapshot.Labels = cloneMenuLabels(targetRow.Labels)
		}
	}
	if locale != document.SourceLocale && snapshot.LocaleExists {
		targetRevision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
			LocaleExists: true, DocumentRevision: snapshot.DocumentRevision,
			LocaleUpdatedAt: &snapshot.LocaleUpdatedAt,
		})
		if err != nil {
			return AIDocumentSnapshot{}, errs.Internal(err)
		}
		snapshot.TargetRevision = &targetRevision
	}
	return snapshot, nil
}

type menuAIDocumentLocaleRow struct {
	Labels    map[string]string
	UpdatedAt time.Time
}

func loadMenuAIDocumentLocaleRow(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	locale string,
	lock bool,
) (menuAIDocumentLocaleRow, bool, error) {
	query := db.WithContext(ctx).Table("menu_translation").
		Select("items_json, updated_at").Where("entity_id = ? AND locale = ?", menuID, locale)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var stored struct {
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := query.Take(&stored).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return menuAIDocumentLocaleRow{}, false, nil
		}
		return menuAIDocumentLocaleRow{}, false, errs.Internal(err)
	}
	labels, err := DecodeTranslationLabelValues(stored.ItemsJSON)
	if err != nil {
		return menuAIDocumentLocaleRow{}, false, errs.Internal(err)
	}
	return menuAIDocumentLocaleRow{Labels: labels, UpdatedAt: stored.UpdatedAt}, true, nil
}

func cloneMenuLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for id, label := range labels {
		cloned[id] = label
	}
	return cloned
}

type menuAIDocumentGraph struct {
	root     string
	items    map[string]*AIDocumentItem
	parent   map[string]string
	children map[string][]string
}

func newMenuAIDocumentGraph(root string, items []AIDocumentItem) (*menuAIDocumentGraph, error) {
	graph := &menuAIDocumentGraph{
		root: root, items: map[string]*AIDocumentItem{}, parent: map[string]string{}, children: map[string][]string{},
	}
	var add func(string, []AIDocumentItem) error
	add = func(parent string, current []AIDocumentItem) error {
		for index := range current {
			item := current[index]
			if strings.TrimSpace(item.ID) == "" {
				return errors.New("menu item ID is required")
			}
			if _, exists := graph.items[item.ID]; exists {
				return fmt.Errorf("duplicate Menu item ID %q", item.ID)
			}
			children := item.Children
			item.Children = nil
			graph.items[item.ID] = &item
			graph.parent[item.ID] = parent
			graph.children[parent] = append(graph.children[parent], item.ID)
			if err := add(item.ID, children); err != nil {
				return err
			}
		}
		return nil
	}
	return graph, add(root, items)
}

func (g *menuAIDocumentGraph) insert(id, parent, after string) error {
	id, parent, after = strings.TrimSpace(id), strings.TrimSpace(parent), strings.TrimSpace(after)
	if id == "" || id == g.root {
		return errors.New("a new stable Menu item ID is required")
	}
	if _, exists := g.items[id]; exists {
		return fmt.Errorf("menu item %q already exists", id)
	}
	if parent == "" {
		parent = g.root
	}
	if parent != g.root {
		if _, exists := g.items[parent]; !exists {
			return fmt.Errorf("menu parent %q does not exist", parent)
		}
	}
	index, err := insertionIndex(g.children[parent], after)
	if err != nil {
		return err
	}
	item := &AIDocumentItem{ID: id}
	g.items[id], g.parent[id] = item, parent
	g.children[parent] = insertString(g.children[parent], index, id)
	return nil
}

func (g *menuAIDocumentGraph) delete(id string) error {
	if _, exists := g.items[id]; !exists {
		return fmt.Errorf("menu item %q does not exist", id)
	}
	var remove func(string)
	remove = func(current string) {
		for _, child := range g.children[current] {
			remove(child)
		}
		delete(g.children, current)
		delete(g.parent, current)
		delete(g.items, current)
	}
	parent := g.parent[id]
	g.children[parent] = removeString(g.children[parent], id)
	remove(id)
	return nil
}

func (g *menuAIDocumentGraph) move(id, parent, after string) (bool, error) {
	if _, exists := g.items[id]; !exists {
		return false, fmt.Errorf("menu item %q does not exist", id)
	}
	if parent == "" {
		parent = g.root
	}
	if parent != g.root {
		if _, exists := g.items[parent]; !exists {
			return false, fmt.Errorf("menu parent %q does not exist", parent)
		}
		for candidate := parent; candidate != g.root && candidate != ""; candidate = g.parent[candidate] {
			if candidate == id {
				return false, errors.New("menu item move would create a cycle")
			}
		}
	}
	currentParent := g.parent[id]
	currentSiblings := removeString(g.children[currentParent], id)
	targetSiblings := g.children[parent]
	if currentParent == parent {
		targetSiblings = currentSiblings
	}
	index, err := insertionIndex(targetSiblings, after)
	if err != nil {
		return false, err
	}
	nextSiblings := insertString(targetSiblings, index, id)
	if currentParent == parent && equalStrings(g.children[parent], nextSiblings) {
		return false, nil
	}
	g.children[currentParent] = currentSiblings
	g.children[parent] = nextSiblings
	g.parent[id] = parent
	return true, nil
}

func (g *menuAIDocumentGraph) build() ([]AIDocumentItem, error) {
	var build func(string, int) ([]AIDocumentItem, error)
	build = func(parent string, depth int) ([]AIDocumentItem, error) {
		if depth > menuMaxNestingDepth {
			return nil, fmt.Errorf("menu nesting depth exceeds maximum of %d levels", menuMaxNestingDepth)
		}
		result := make([]AIDocumentItem, 0, len(g.children[parent]))
		for _, id := range g.children[parent] {
			item := *g.items[id]
			children, err := build(id, depth+1)
			if err != nil {
				return nil, err
			}
			item.Children = children
			result = append(result, item)
		}
		return result, nil
	}
	return build(g.root, 0)
}

func applyMenuAIDocumentSourceField(item *AIDocumentItem, operation AIDocumentOperation) (bool, error) {
	unset := operation.Kind == AIDocumentUnsetItemField
	switch operation.Field {
	case "link_type":
		if unset || operation.Value.Kind != AIDocumentText {
			return false, errors.New("link_type requires text")
		}
		return assignString(&item.LinkType, operation.Value.Text), nil
	case "url", "target_id", "target_slug", "localization_mode", "fixed_locale", "visibility_mode":
		if !unset && operation.Value.Kind != AIDocumentText {
			return false, fmt.Errorf("%s requires text", operation.Field)
		}
		var next *string
		if !unset {
			value := operation.Value.Text
			next = &value
		}
		switch operation.Field {
		case "url":
			return assignStringPointer(&item.URL, next), nil
		case "target_id":
			return assignStringPointer(&item.TargetID, next), nil
		case "target_slug":
			return assignStringPointer(&item.TargetSlug, next), nil
		case "localization_mode":
			return assignStringPointer(&item.LocalizationMode, next), nil
		case "fixed_locale":
			return assignStringPointer(&item.FixedLocale, next), nil
		case "visibility_mode":
			return assignStringPointer(&item.VisibilityMode, next), nil
		}
	case "open_in_new_tab":
		if !unset && operation.Value.Kind != AIDocumentBoolean {
			return false, errors.New("open_in_new_tab requires boolean")
		}
		var next *bool
		if !unset {
			value := operation.Value.Boolean
			next = &value
		}
		return assignBoolPointer(&item.OpenInNewTab, next), nil
	case "visibility_roles":
		if !unset && operation.Value.Kind != AIDocumentTextList {
			return false, errors.New("visibility_roles requires a text list")
		}
		next := []string(nil)
		if !unset {
			next = append([]string(nil), operation.Value.Texts...)
		}
		if equalStrings(item.VisibilityRoles, next) {
			return false, nil
		}
		item.VisibilityRoles = next
		return true, nil
	default:
		return false, fmt.Errorf("unsupported Menu item field %q", operation.Field)
	}
	return false, nil
}

func itemOwnsLocaleLabel(item *AIDocumentItem, locale string) bool {
	modelItem := aiDocumentItemToModel(*item)
	return ShouldTranslateItemLabel(&modelItem, locale)
}

// OwnsLabel reports whether this item's requested-locale label is editable.
// Translated items own every non-source locale; fixed-locale items own only
// their declared locale.
func (item AIDocumentItem) OwnsLabel(locale string) bool {
	return itemOwnsLocaleLabel(&item, locale)
}

func validateAIDocumentItemConfiguration(items []AIDocumentItem) error {
	for _, item := range items {
		if item.LocalizationMode != nil {
			switch strings.TrimSpace(*item.LocalizationMode) {
			case model.MenuItemLocalizationModeTranslated:
				if item.FixedLocale != nil && strings.TrimSpace(*item.FixedLocale) != "" {
					return fmt.Errorf("menu item %q translated mode cannot carry fixed_locale", item.ID)
				}
			case model.MenuItemLocalizationModeFixedLocale:
				if NormalizeItemFixedLocale(item.FixedLocale) == nil {
					return fmt.Errorf("menu item %q fixed_locale mode requires a supported locale", item.ID)
				}
			default:
				return fmt.Errorf("menu item %q has an unsupported localization mode", item.ID)
			}
		} else if item.FixedLocale != nil && strings.TrimSpace(*item.FixedLocale) != "" && NormalizeItemFixedLocale(item.FixedLocale) == nil {
			return fmt.Errorf("menu item %q has an unsupported fixed locale", item.ID)
		}
		if item.VisibilityMode != nil {
			switch strings.TrimSpace(*item.VisibilityMode) {
			case "all", "authenticated", "guest", "roles":
			default:
				return fmt.Errorf("menu item %q has an unsupported visibility mode", item.ID)
			}
		}
		if err := validateAIDocumentItemConfiguration(item.Children); err != nil {
			return err
		}
	}
	return nil
}

func modelItemsToAIDocument(body []byte) ([]AIDocumentItem, error) {
	var items []model.MenuItem
	if len(body) != 0 {
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, err
		}
	}
	return modelMenuItemsToAIDocument(items), nil
}

func modelMenuItemsToAIDocument(items []model.MenuItem) []AIDocumentItem {
	result := make([]AIDocumentItem, len(items))
	for index := range items {
		item := items[index]
		result[index] = AIDocumentItem{
			ID: item.ID, Label: item.Label, LinkType: item.LinkType,
			URL: cloneStringPointer(item.URL), TargetID: cloneStringPointer(item.TargetID),
			TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
			LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
			Children: modelMenuItemsToAIDocument(item.Children),
		}
		if item.Visibility != nil {
			mode := item.Visibility.Mode
			result[index].VisibilityMode = &mode
			result[index].VisibilityRoles = append([]string(nil), item.Visibility.Roles...)
		}
	}
	return result
}

func aiDocumentItemsToModel(items []AIDocumentItem) []model.MenuItem {
	result := make([]model.MenuItem, len(items))
	for index := range items {
		result[index] = aiDocumentItemToModel(items[index])
	}
	return result
}

func aiDocumentItemToModel(item AIDocumentItem) model.MenuItem {
	result := model.MenuItem{
		ID: item.ID, Label: item.Label, LinkType: item.LinkType,
		URL: cloneStringPointer(item.URL), TargetID: cloneStringPointer(item.TargetID),
		TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
		LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
		Children: aiDocumentItemsToModel(item.Children),
	}
	if item.VisibilityMode != nil || len(item.VisibilityRoles) != 0 {
		mode := "all"
		if item.VisibilityMode != nil {
			mode = *item.VisibilityMode
		}
		result.Visibility = &model.MenuVisibility{Mode: mode, Roles: append([]string(nil), item.VisibilityRoles...)}
	}
	return result
}

func cloneMenuAIDocumentSnapshot(snapshot AIDocumentSnapshot) AIDocumentSnapshot {
	copy := snapshot
	copy.Items = cloneMenuAIDocumentItems(snapshot.Items)
	copy.Labels = cloneMenuLabels(snapshot.Labels)
	copy.sourceLabels = cloneMenuLabels(snapshot.sourceLabels)
	return copy
}

func cloneMenuAIDocumentItems(items []AIDocumentItem) []AIDocumentItem {
	cloned := make([]AIDocumentItem, len(items))
	for index := range items {
		cloned[index] = items[index]
		cloned[index].URL = cloneStringPointer(items[index].URL)
		cloned[index].TargetID = cloneStringPointer(items[index].TargetID)
		cloned[index].TargetSlug = cloneStringPointer(items[index].TargetSlug)
		cloned[index].OpenInNewTab = cloneBoolPointer(items[index].OpenInNewTab)
		cloned[index].VisibilityMode = cloneStringPointer(items[index].VisibilityMode)
		cloned[index].VisibilityRoles = append([]string(nil), items[index].VisibilityRoles...)
		cloned[index].LocalizationMode = cloneStringPointer(items[index].LocalizationMode)
		cloned[index].FixedLocale = cloneStringPointer(items[index].FixedLocale)
		cloned[index].Children = cloneMenuAIDocumentItems(items[index].Children)
	}
	return cloned
}

func equalAIDocumentItems(left, right []AIDocumentItem) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func insertionIndex(siblings []string, after string) (int, error) {
	if after == "" {
		return 0, nil
	}
	for index, sibling := range siblings {
		if sibling == after {
			return index + 1, nil
		}
	}
	return 0, fmt.Errorf("after item %q is not a sibling", after)
}

func insertString(values []string, index int, value string) []string {
	values = append(values, "")
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}

func removeString(values []string, value string) []string {
	result := make([]string, 0, len(values))
	for _, current := range values {
		if current != value {
			result = append(result, current)
		}
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func assignString(target *string, value string) bool {
	if *target == value {
		return false
	}
	*target = value
	return true
}

func assignStringPointer(target **string, value *string) bool {
	if target == nil {
		return false
	}
	if (*target == nil) == (value == nil) && (*target == nil || **target == *value) {
		return false
	}
	*target = cloneStringPointer(value)
	return true
}

func assignBoolPointer(target **bool, value *bool) bool {
	if (*target == nil) == (value == nil) && (*target == nil || **target == *value) {
		return false
	}
	*target = cloneBoolPointer(value)
	return true
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
