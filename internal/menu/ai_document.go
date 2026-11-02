package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
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

func (s *Service) compileAIDocument(
	snapshot AIDocumentSnapshot,
	operations []AIDocumentOperation,
) (compiledMenuAIDocument, []AIDocumentIssue) {
	working := cloneMenuAIDocumentSnapshot(snapshot)
	result := compiledMenuAIDocument{snapshot: working}
	graph, err := newMenuAIDocumentGraph(working.ID, working.Items)
	if err != nil {
		return result, []AIDocumentIssue{{Operation: -1, Code: AIDocumentIssueInvalidOperation, Handle: menuHandle(working.ID, ""), Message: err.Error()}}
	}
	compiler := &menuCompiler{
		working:        working,
		graph:          graph,
		result:         result,
		isSource:       working.Locale == working.SourceLocale,
		operationCount: len(operations),
	}
	bootstrapTarget := !compiler.isSource && !working.LocaleExists && menuAIDocumentCreatesTarget(operations)
	if bootstrapTarget {
		compiler.working.Labels = seedMenuTargetLabels(compiler.working)
	}
	for index, operation := range operations {
		if issue := compiler.apply(operation); issue != nil {
			issue.Operation = index
			return compiler.result, []AIDocumentIssue{*issue}
		}
	}
	if bootstrapTarget && !compiler.working.LocaleExists {
		compiler.working.LocaleExists = true
		compiler.result.changed, compiler.result.targetChanged = true, true
	}

	compiler.working.Items, err = graph.build()
	if err != nil {
		return compiler.result, []AIDocumentIssue{{Operation: -1, Code: AIDocumentIssueInvalidOperation, Handle: menuHandle(working.ID, ""), Message: err.Error()}}
	}
	compiler.result.snapshot = compiler.working
	issue := s.validateCompiledSource(compiler.result)
	if issue == nil {
		issue = validateCompiledLabels(compiler.result)
	}
	if issue != nil {
		issue.Operation = lastMenuAIDocumentMutationIndex(operations)
		return compiler.result, []AIDocumentIssue{*issue}
	}
	return compiler.result, nil
}

// menuCompiler applies AI document operations to a working copy of one Menu
// snapshot and records in result which parts of the aggregate changed.
type menuCompiler struct {
	working        AIDocumentSnapshot
	graph          *menuAIDocumentGraph
	result         compiledMenuAIDocument
	isSource       bool
	operationCount int
}

func menuHandle(menuID, field string) string {
	if field == "" {
		return "menu:" + menuID
	}
	return "menu:" + menuID + "/" + field
}

func itemHandle(itemID, field string) string {
	if field == "" {
		return "item:" + itemID
	}
	return "item:" + itemID + "/" + field
}

func translationHandle(locale string) string {
	return "translation:" + locale
}

// invalidOperation and forbiddenTarget build the issue for the operation
// being applied; compileAIDocument fills in its index.
func invalidOperation(handle, message string) *AIDocumentIssue {
	return &AIDocumentIssue{Code: AIDocumentIssueInvalidOperation, Handle: handle, Message: message}
}

func forbiddenTarget(handle, message string) *AIDocumentIssue {
	return &AIDocumentIssue{Code: AIDocumentIssueTargetForbidden, Handle: handle, Message: message}
}

func (c *menuCompiler) apply(operation AIDocumentOperation) *AIDocumentIssue {
	switch operation.Kind {
	case AIDocumentSetName:
		return c.applySetName(operation)
	case AIDocumentNoop:
		// The Menu catalog has one item kind. A shared kind replacement
		// normalized to the same menu_item kind is intentionally a no-op.
		return nil
	case AIDocumentSetItemField, AIDocumentUnsetItemField:
		item, ok := c.graph.items[operation.ItemID]
		if !ok {
			return invalidOperation(itemHandle(operation.ItemID, ""), "Menu item does not exist")
		}
		if operation.Field == "label" {
			return c.applyItemLabel(item, operation)
		}
		return c.applyItemSourceField(item, operation)
	case AIDocumentInsertItem, AIDocumentDeleteItem, AIDocumentMoveItem:
		return c.applyStructural(operation)
	case AIDocumentCreateTranslation, AIDocumentDeleteTranslation:
		return c.applyTranslationLifecycle(operation)
	default:
		return invalidOperation(menuHandle(c.working.ID, ""), "unsupported Menu AI document operation")
	}
}

func (c *menuCompiler) markSourceChanged() {
	c.result.changed, c.result.sourceChanged = true, true
}

// markLabelsChanged records a label change in the locale being edited: the
// source locale's own values row or the target translation.
func (c *menuCompiler) markLabelsChanged() {
	c.result.changed = true
	if c.isSource {
		c.result.sourceValuesChanged = true
	} else {
		c.result.targetChanged = true
	}
}

func (c *menuCompiler) applySetName(operation AIDocumentOperation) *AIDocumentIssue {
	if !c.isSource {
		return forbiddenTarget(menuHandle(c.working.ID, ""), "only the source locale may change the Menu name")
	}
	if operation.Value.Kind != AIDocumentText {
		return invalidOperation(menuHandle(c.working.ID, "name"), "Menu name requires text")
	}
	if c.working.Name != operation.Value.Text {
		c.working.Name = operation.Value.Text
		c.markSourceChanged()
	}
	return nil
}

func (c *menuCompiler) applyItemLabel(item *AIDocumentItem, operation AIDocumentOperation) *AIDocumentIssue {
	handle := itemHandle(operation.ItemID, "label")
	set := operation.Kind == AIDocumentSetItemField
	if set && operation.Value.Kind != AIDocumentText {
		return invalidOperation(handle, "Menu item label requires text")
	}
	if set && len(operation.Value.Text) > menuItemLabelMaxLength {
		return invalidOperation(handle, fmt.Sprintf("Menu item label must be at most %d characters", menuItemLabelMaxLength))
	}
	if !itemOwnsLocaleLabel(item, c.working.Locale) {
		return forbiddenTarget(handle, "fixed-locale Menu item labels may be edited only in their fixed locale")
	}
	if c.isSource && !c.working.SourceValuesStored {
		return invalidOperation(translationHandle(c.working.Locale), "Menu source locale values are not initialized")
	}
	current, exists := c.working.Labels[operation.ItemID]
	switch {
	case !set:
		if exists {
			delete(c.working.Labels, operation.ItemID)
			c.markLabelsChanged()
		}
	case !exists || current != operation.Value.Text:
		c.working.Labels[operation.ItemID] = operation.Value.Text
		c.markLabelsChanged()
	}
	return nil
}

func (c *menuCompiler) applyItemSourceField(item *AIDocumentItem, operation AIDocumentOperation) *AIDocumentIssue {
	handle := itemHandle(operation.ItemID, operation.Field)
	if !c.isSource {
		return forbiddenTarget(handle, "non-source locales may change only translated labels")
	}
	changed, err := applyMenuAIDocumentSourceField(item, operation)
	if err != nil {
		return invalidOperation(handle, err.Error())
	}
	if changed {
		c.markSourceChanged()
	}
	return nil
}

func (c *menuCompiler) applyStructural(operation AIDocumentOperation) *AIDocumentIssue {
	handle := itemHandle(operation.ItemID, "")
	changed := true
	var err error
	switch operation.Kind {
	case AIDocumentInsertItem:
		if !c.isSource {
			return forbiddenTarget(handle, "only the source locale may insert Menu items")
		}
		err = c.graph.insert(operation.ItemID, operation.ParentID, operation.AfterID)
	case AIDocumentDeleteItem:
		if !c.isSource {
			return forbiddenTarget(handle, "only the source locale may delete Menu items")
		}
		err = c.graph.delete(operation.ItemID)
	default:
		if !c.isSource {
			return forbiddenTarget(handle, "only the source locale may move Menu items")
		}
		changed, err = c.graph.move(operation.ItemID, operation.ParentID, operation.AfterID)
	}
	if err != nil {
		return invalidOperation(handle, err.Error())
	}
	if changed {
		c.markSourceChanged()
	}
	return nil
}

// applyTranslationLifecycle creates or deletes the edited target locale. Each
// must be the only operation in its command.
func (c *menuCompiler) applyTranslationLifecycle(operation AIDocumentOperation) *AIDocumentIssue {
	handle := translationHandle(c.working.Locale)
	if operation.Kind == AIDocumentCreateTranslation {
		if c.isSource || c.working.LocaleExists || c.operationCount != 1 {
			return invalidOperation(handle, "translation create requires one absent non-source locale")
		}
		c.working.LocaleExists = true
		c.result.changed, c.result.targetChanged = true, true
		return nil
	}
	if c.isSource || !c.working.LocaleExists || c.operationCount != 1 {
		return invalidOperation(handle, "translation delete requires one existing non-source locale")
	}
	c.working.LocaleExists = false
	c.working.Labels = map[string]string{}
	c.result.changed, c.result.targetChanged, c.result.deleteTranslation = true, true, true
	return nil
}

// validateCompiledSource checks the whole compiled source aggregate, which no
// single operation can, when the source changed.
func (s *Service) validateCompiledSource(result compiledMenuAIDocument) *AIDocumentIssue {
	if !result.sourceChanged {
		return nil
	}
	working := result.snapshot
	if err := validateMenuName(working.Name); err != nil {
		return invalidOperation(menuHandle(working.ID, "name"), err.Error())
	}
	if err := validateAIDocumentItemConfiguration(working.Items); err != nil {
		return invalidOperation(menuHandle(working.ID, "items"), err.Error())
	}
	if err := validateMenuItems(structuralValidationItems(working.Items), 0); err != nil {
		return invalidOperation(menuHandle(working.ID, "items"), err.Error())
	}
	return nil
}

func validateCompiledLabels(result compiledMenuAIDocument) *AIDocumentIssue {
	if !result.sourceValuesChanged && !result.targetChanged {
		return nil
	}
	if err := validateAIDocumentLocaleLabels(result.snapshot.Labels); err != nil {
		return invalidOperation(menuHandle(result.snapshot.ID, "labels"), err.Error())
	}
	return nil
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

// structuralValidationItems is the requested form of an AI document or
// collaboration tree for validateMenuItems. Its labels live in the locale
// values, so a missing one is filled for the check.
func structuralValidationItems(items []AIDocumentItem) []Item {
	validationItems := requestedItems(aiDocumentItemsToModel(items))
	fillMissingMenuLabelsForStructuralValidation(validationItems)
	return validationItems
}

func fillMissingMenuLabelsForStructuralValidation(items []Item) {
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

func persistAIDocumentSource(
	ctx context.Context,
	store Store,
	previous AIDocumentSnapshot,
	next AIDocumentSnapshot,
	now time.Time,
) error {
	change := menuUpdate{now: translation.NextTargetUpdatedAt(now, previous.rootUpdatedAt)}
	if previous.Name != next.Name {
		name := next.Name
		if err := ensureUpdatedMenuNameAvailable(ctx, store, previous.ID, &name); err != nil {
			return err
		}
		change.name = &name
	}
	itemsChanged := !equalAIDocumentItems(previous.Items, next.Items)
	if itemsChanged {
		itemsJSON, err := json.Marshal(aiDocumentItemsToModel(next.Items))
		if err != nil {
			return err
		}
		change.items = itemsJSON
	}
	if change.name == nil && change.items == nil {
		return nil
	}
	if err := updateMenuRow(ctx, store, previous.ID, change.name, change); err != nil {
		return err
	}
	if itemsChanged {
		return reconcileTranslationRowsWithSourceItems(ctx, store, previous.ID, aiDocumentItemsToModel(next.Items), now)
	}
	return nil
}

func persistMenuAIDocumentSourceValues(
	ctx context.Context,
	store Store,
	previous AIDocumentSnapshot,
	next AIDocumentSnapshot,
	now time.Time,
) error {
	if !previous.SourceValuesStored || previous.Locale != previous.SourceLocale {
		return domainerr.Precondition("Menu source locale values are not initialized")
	}
	err := store.UpdateLocale(ctx, LocaleValues{
		MenuID: previous.ID, Locale: previous.Locale, Labels: cloneMenuLabels(next.Labels),
		UpdatedAt: translation.NextTargetUpdatedAt(now, previous.LocaleUpdatedAt),
	})
	if errors.Is(err, ErrLocaleNotFound) {
		return domainerr.Precondition("Menu source locale values are not initialized")
	}
	return err
}

// persistMenuAIDocumentTarget deletes the target locale row, or writes the
// labels next owns there. previous is the snapshot locked in the same
// transaction, so its LocaleUpdatedAt is the row's current timestamp.
func persistMenuAIDocumentTarget(
	ctx context.Context,
	store Store,
	previous AIDocumentSnapshot,
	next AIDocumentSnapshot,
	deleteTranslation bool,
	now time.Time,
) error {
	if deleteTranslation {
		err := store.DeleteLocale(ctx, previous.ID, previous.Locale)
		if errors.Is(err, ErrLocaleNotFound) {
			return domainerr.Precondition("Menu translation no longer exists")
		}
		return err
	}
	nextUpdatedAt := now.UTC().Truncate(time.Microsecond)
	if previous.LocaleExists {
		nextUpdatedAt = translation.NextTargetUpdatedAt(nextUpdatedAt, previous.LocaleUpdatedAt)
	}
	return store.UpsertLocale(ctx, LocaleValues{
		MenuID: previous.ID, Locale: previous.Locale,
		Labels:    canonicalMenuAIDocumentLabels(next.Items, previous.Locale, next.Labels),
		UpdatedAt: nextUpdatedAt,
	})
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

// menuAIDocumentAudit names the record of a changed AI document: the
// target locale's lifecycle, or the changed source fields.
func menuAIDocumentAudit(previous AIDocumentSnapshot, compiled compiledMenuAIDocument) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditMenuUpdated, menuDocumentAuditRecord(previous, compiled, true))
}

// menuDocumentAuditRecord builds the record of a changed Menu document.
// A collaboration save records a target locale update without its
// lifecycle (trackLifecycle false), as rooms never create or delete one.
func menuDocumentAuditRecord(previous AIDocumentSnapshot, compiled compiledMenuAIDocument, trackLifecycle bool) domainaudit.Builder {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		if compiled.targetChanged {
			operation := sharedtelemetry.AuditItemOperationUpdated
			switch {
			case !trackLifecycle:
			case !previous.LocaleExists && compiled.snapshot.LocaleExists:
				operation = sharedtelemetry.AuditItemOperationCreated
			case previous.LocaleExists && !compiled.snapshot.LocaleExists:
				operation = sharedtelemetry.AuditItemOperationDeleted
			}
			return sharedtelemetry.NewMenuLocaleContentAuditRecord(metadata, previous.ID, previous.Locale, operation)
		}
		fields := make([]string, 0, 2)
		if previous.Name != compiled.snapshot.Name {
			fields = append(fields, "name")
		}
		if !equalAIDocumentItems(previous.Items, compiled.snapshot.Items) || compiled.sourceValuesChanged {
			fields = append(fields, "items")
		}
		sort.Strings(fields)
		return sharedtelemetry.NewMenuSourceUpdatedAuditRecord(metadata, previous.ID, fields)
	}
}

func loadMenuAIDocumentSnapshot(
	ctx context.Context,
	store Store,
	menuID string,
	locale string,
	lock bool,
) (AIDocumentSnapshot, error) {
	menuID, locale = strings.TrimSpace(menuID), strings.TrimSpace(locale)
	if menuID == "" || locale == "" {
		return AIDocumentSnapshot{}, domainerr.InvalidField("document", "Menu ID and locale are required")
	}
	read := store.Menu
	if lock {
		read = store.LockMenu
	}
	root, err := read(ctx, menuID)
	if err != nil {
		if errors.Is(err, ErrMenuNotFound) {
			return AIDocumentSnapshot{}, domainerr.ResourceNotFound("menu", menuID)
		}
		return AIDocumentSnapshot{}, err
	}
	return loadMenuAIDocumentSnapshotFromRoot(ctx, store, root, locale, lock)
}

func loadMenuAIDocumentSnapshotFromRoot(
	ctx context.Context,
	store Store,
	root Root,
	locale string,
	lock bool,
) (AIDocumentSnapshot, error) {
	menuID, locale := strings.TrimSpace(root.ID), strings.TrimSpace(locale)
	if menuID == "" || locale == "" {
		return AIDocumentSnapshot{}, domainerr.InvalidField("document", "Menu ID and locale are required")
	}

	document, err := loadMenuContentDocumentState(ctx, store, root, lock)
	if err != nil {
		return AIDocumentSnapshot{}, err
	}

	items, err := modelItemsToAIDocument(root.ItemsJSON)
	if err != nil {
		return AIDocumentSnapshot{}, err
	}
	snapshot := AIDocumentSnapshot{
		ID: root.ID, Name: root.Name, SourceLocale: document.SourceLocale, Locale: locale,
		DocumentRevision: document.Revision.String(), contentDocumentID: document.ID,
		rootUpdatedAt: root.UpdatedAt, Items: items, Labels: map[string]string{},
	}
	source, sourceExists, err := loadMenuAIDocumentLocaleRow(ctx, store, menuID, document.SourceLocale, lock)
	if err != nil {
		return AIDocumentSnapshot{}, err
	}
	if !sourceExists {
		return AIDocumentSnapshot{}, domainerr.Precondition("Menu source locale values are not initialized")
	}
	snapshot.sourceLabels = cloneMenuLabels(source.Labels)
	if locale == document.SourceLocale {
		snapshot.LocaleExists = true
		snapshot.SourceValuesStored = true
		snapshot.LocaleUpdatedAt = source.UpdatedAt
		snapshot.Labels = cloneMenuLabels(source.Labels)
		return snapshot, nil
	}
	target, targetExists, err := loadMenuAIDocumentLocaleRow(ctx, store, menuID, locale, lock)
	if err != nil || !targetExists {
		return snapshot, err
	}
	snapshot.LocaleExists = true
	snapshot.LocaleUpdatedAt = target.UpdatedAt
	snapshot.Labels = cloneMenuLabels(target.Labels)
	targetRevision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: snapshot.DocumentRevision,
		LocaleUpdatedAt: &snapshot.LocaleUpdatedAt,
	})
	if err != nil {
		return AIDocumentSnapshot{}, err
	}
	snapshot.TargetRevision = &targetRevision
	return snapshot, nil
}

func loadMenuAIDocumentLocaleRow(
	ctx context.Context,
	store Store,
	menuID string,
	locale string,
	lock bool,
) (LocaleValues, bool, error) {
	read := store.Locale
	if lock {
		read = store.LockLocale
	}
	values, err := read(ctx, menuID, locale)
	if errors.Is(err, ErrLocaleNotFound) {
		return LocaleValues{}, false, nil
	}
	if err != nil {
		return LocaleValues{}, false, err
	}
	return values, true, nil
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
	return ShouldTranslateItemLabel(item.LocalizationMode, item.FixedLocale, locale)
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
			case LocalizationTranslated:
				if item.FixedLocale != nil && strings.TrimSpace(*item.FixedLocale) != "" {
					return fmt.Errorf("menu item %q translated mode cannot carry fixed_locale", item.ID)
				}
			case LocalizationFixedLocale:
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
	var items []Item
	if len(body) != 0 {
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, err
		}
	}
	return modelMenuItemsToAIDocument(items), nil
}

func modelMenuItemsToAIDocument(items []Item) []AIDocumentItem {
	result := make([]AIDocumentItem, len(items))
	for index := range items {
		item := items[index]
		result[index] = AIDocumentItem{
			ID: item.ID, Label: item.Label, LinkType: string(item.LinkType),
			URL: cloneStringPointer(item.URL), TargetID: cloneStringPointer(item.TargetID),
			TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
			LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
			Children: modelMenuItemsToAIDocument(item.Children),
		}
		if item.Visibility != nil {
			mode := string(item.Visibility.Mode)
			result[index].VisibilityMode = &mode
			result[index].VisibilityRoles = append([]string(nil), item.Visibility.Roles...)
		}
	}
	return result
}

func aiDocumentItemsToModel(items []AIDocumentItem) []Item {
	result := make([]Item, len(items))
	for index := range items {
		result[index] = aiDocumentItemToModel(items[index])
	}
	return result
}

func aiDocumentItemToModel(item AIDocumentItem) Item {
	result := Item{
		ID: item.ID, Label: item.Label, LinkType: LinkType(item.LinkType),
		URL: cloneStringPointer(item.URL), TargetID: cloneStringPointer(item.TargetID),
		TargetSlug: cloneStringPointer(item.TargetSlug), OpenInNewTab: cloneBoolPointer(item.OpenInNewTab),
		LocalizationMode: cloneStringPointer(item.LocalizationMode), FixedLocale: cloneStringPointer(item.FixedLocale),
		Children: aiDocumentItemsToModel(item.Children),
	}
	if item.VisibilityMode != nil || len(item.VisibilityRoles) != 0 {
		mode := VisibilityAll
		if item.VisibilityMode != nil {
			mode = VisibilityMode(*item.VisibilityMode)
		}
		result.Visibility = &Visibility{Mode: mode, Roles: append([]string(nil), item.VisibilityRoles...)}
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
