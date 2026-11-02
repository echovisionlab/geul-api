package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// ProviderTranslation is one accepted provider response to a Menu
// translation job.
type ProviderTranslation struct {
	MenuID              string
	TargetLocale        string
	RequestedByMemberID string
	Candidate           *translation.Candidate
	Now                 time.Time
}

// ApplyProviderTranslation applies, in the translation pipeline's joined
// transaction, only the request-time Menu label units that still belong to
// the current Menu graph. A source-locale switch changes which locale row is
// source-owned, not whether the accepted provider response may update its
// surviving stable labels.
func ApplyProviderTranslation(ctx context.Context, joined Joined, applied ProviderTranslation) error {
	if joined.Store == nil || applied.Candidate == nil || !applied.Candidate.HasProviderUnitPatch() {
		return errors.New("menu provider translation candidate is required")
	}
	if joined.Audit == nil {
		return errors.New("menu provider translation Audit writer is required")
	}
	memberID, err := uuid.Parse(strings.TrimSpace(applied.RequestedByMemberID))
	if err != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(applied.RequestedByMemberID) {
		return fmt.Errorf("menu provider translation requires canonical requester Member, got %q", applied.RequestedByMemberID)
	}
	now := applied.Now.UTC()
	if now.IsZero() {
		return errors.New("Menu provider translation time is required")
	}
	store := joined.Store
	root, err := store.LockMenu(ctx, applied.MenuID)
	if errors.Is(err, ErrMenuNotFound) {
		return domainerr.ResourceNotFound("menu", applied.MenuID)
	}
	if err != nil {
		return err
	}
	current, err := loadMenuAIDocumentSnapshotFromRoot(ctx, store, root, applied.TargetLocale, true)
	if err != nil {
		return err
	}
	incoming := canonicalMenuAIDocumentLabels(current.Items, applied.TargetLocale, providerMenuLabels(applied.Candidate))
	next := cloneMenuAIDocumentSnapshot(current)
	maps.Copy(next.Labels, incoming)
	if maps.Equal(current.Labels, next.Labels) && current.LocaleExists {
		return nil
	}
	operation, changed, err := persistProviderTranslation(ctx, store, current, next, now)
	if err != nil || !changed {
		return err
	}
	return domainaudit.AppendMember(
		ctx, nil, joined.Audit, memberID.String(), sharedtelemetry.AuditMenuUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMenuLocaleContentAuditRecord(metadata, current.ID, applied.TargetLocale, operation)
		},
	)
}

// persistProviderTranslation writes the provider labels of next to the
// source values row, advancing the content document, or to the target
// locale row, and names the audited operation.
func persistProviderTranslation(ctx context.Context, store Store, current, next AIDocumentSnapshot, now time.Time) (sharedtelemetry.AuditItemOperation, bool, error) {
	operation := sharedtelemetry.AuditItemOperationUpdated
	if current.Locale != current.SourceLocale {
		if !current.LocaleExists {
			next.LocaleExists = true
			operation = sharedtelemetry.AuditItemOperationCreated
		}
		return operation, true, persistMenuAIDocumentTarget(ctx, store, current, next, false, now)
	}
	if !current.SourceValuesStored {
		return "", false, domainerr.Precondition("Menu source locale values are not initialized")
	}
	if maps.Equal(current.Labels, next.Labels) {
		return "", false, nil
	}
	if err := persistMenuAIDocumentSourceValues(ctx, store, current, next, now); err != nil {
		return "", false, err
	}
	expected, err := parseMenuContentDocumentUUID(current.DocumentRevision, "content_document.revision")
	if err != nil {
		return "", false, err
	}
	return operation, true, advanceMenuContentDocument(ctx, store, current.ID, current.contentDocumentID, expected, now)
}

// providerMenuLabels returns the translated item labels of a provider
// response, keyed by item ID.
func providerMenuLabels(candidate *translation.Candidate) map[string]string {
	patch, _ := candidate.ProviderPatch()
	labels := make(map[string]string, len(patch.Results))
	for _, unit := range patch.Units {
		result, ok := patch.Results[unit.UnitID]
		if !ok || unit.ContainerType != translation.ContainerTypeBlock || unit.FieldName != "label" ||
			unit.UnitID != "item:"+unit.ContainerID+":label" {
			continue
		}
		labels[unit.ContainerID] = result.TranslatedText
	}
	return labels
}

type translationLabelItem struct {
	ID       string                 `json:"id"`
	Label    json.RawMessage        `json:"label"`
	Children []translationLabelItem `json:"children"`
}

// DecodeTranslationLabelValues decodes only present label values. An omitted
// label is missing while a present empty string remains an explicit value.
// Source-owned fields in legacy/full-tree payloads are ignored.
func DecodeTranslationLabelValues(body []byte) (map[string]string, error) {
	labels := map[string]string{}
	if len(body) == 0 {
		return labels, nil
	}
	var items []translationLabelItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, domainerr.InvalidField("content_json", "must be a valid Menu label document").WithCause(err)
	}
	if err := collectTranslationLabels(items, labels, map[string]struct{}{}); err != nil {
		return nil, domainerr.InvalidField("content_json", err.Error())
	}
	return labels, nil
}

func collectTranslationLabels(items []translationLabelItem, labels map[string]string, seen map[string]struct{}) error {
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return errors.New("menu translation item ID is required")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("menu translation item ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
		if len(item.Label) != 0 {
			var label string
			if err := json.Unmarshal(item.Label, &label); err != nil {
				return fmt.Errorf("menu translation item %q label must be text: %w", id, err)
			}
			labels[id] = label
		}
		if err := collectTranslationLabels(item.Children, labels, seen); err != nil {
			return err
		}
	}
	return nil
}

// EncodeTranslationLabelValues emits deterministic flat values-only rows.
func EncodeTranslationLabelValues(labels map[string]string) ([]byte, error) {
	type storedItem struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	ids := make([]string, 0, len(labels))
	for id := range labels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]storedItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, storedItem{ID: id, Label: labels[id]})
	}
	return json.Marshal(items)
}

// initializeSourceLabelValues creates the source locale row of a new root
// with the labels of its items.
func initializeSourceLabelValues(ctx context.Context, store Store, root Root) error {
	sourceLocale := strings.TrimSpace(root.SourceLocale)
	if sourceLocale == "" {
		return domainerr.Precondition("Menu source locale is not initialized")
	}
	sourceItems, err := decodeSourceItems(root.ItemsJSON)
	if err != nil {
		return err
	}
	labels := make(map[string]string)
	collectMenuLocaleLabelValues(sourceItems, sourceLocale, labels)
	now := root.UpdatedAt.UTC()
	if now.IsZero() {
		return domainerr.Precondition("Menu source timestamp is not initialized")
	}
	return store.InsertLocale(ctx, LocaleValues{MenuID: root.ID, Locale: sourceLocale, Labels: labels, UpdatedAt: now})
}

// syncCurrentSourceLabelValues writes the full current source-locale label
// set supplied by the direct Menu source editor. It requires the source
// version row to exist and never creates or mutates a non-source locale row.
func syncCurrentSourceLabelValues(ctx context.Context, store Store, root Root, sourceItems []Item, now time.Time) error {
	sourceLocale := strings.TrimSpace(root.SourceLocale)
	if sourceLocale == "" {
		return domainerr.Precondition("Menu source locale is not initialized")
	}
	current, err := store.LockLocale(ctx, root.ID, sourceLocale)
	if err != nil {
		if errors.Is(err, ErrLocaleNotFound) {
			return domainerr.Precondition("Menu source locale values are not initialized")
		}
		return err
	}
	next := make(map[string]string)
	collectMenuLocaleLabelValues(sourceItems, sourceLocale, next)
	if maps.Equal(current.Labels, next) {
		return nil
	}
	return store.UpdateLocale(ctx, LocaleValues{
		MenuID: root.ID, Locale: sourceLocale, Labels: next,
		UpdatedAt: translation.NextTargetUpdatedAt(now, current.UpdatedAt),
	})
}

func collectMenuLocaleLabelValues(items []Item, locale string, labels map[string]string) {
	for i := range items {
		if id := strings.TrimSpace(items[i].ID); id != "" && items[i].ownsLabel(locale) {
			labels[id] = items[i].Label
		}
		collectMenuLocaleLabelValues(items[i].Children, locale, labels)
	}
}

// reconcileTranslationRowsWithSourceItems prunes only labels belonging to
// deleted source item IDs. It never creates rows, seeds new labels, or
// rewrites an unchanged target row.
func reconcileTranslationRowsWithSourceItems(ctx context.Context, store Store, menuID string, sourceItems []Item, now time.Time) error {
	liveIDs := make(map[string]struct{})
	collectMenuItemIDs(sourceItems, liveIDs)

	rows, err := store.Locales(ctx, menuID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		pruned := false
		for id := range row.Labels {
			if _, exists := liveIDs[id]; exists {
				continue
			}
			delete(row.Labels, id)
			pruned = true
		}
		if !pruned {
			continue
		}
		row.UpdatedAt = translation.NextTargetUpdatedAt(now, row.UpdatedAt)
		// A row deleted since it was read has nothing left to prune.
		if err := store.UpdateLocale(ctx, row); err != nil && !errors.Is(err, ErrLocaleNotFound) {
			return err
		}
	}
	return nil
}

func collectMenuItemIDs(items []Item, ids map[string]struct{}) {
	for i := range items {
		if id := strings.TrimSpace(items[i].ID); id != "" {
			ids[id] = struct{}{}
		}
		collectMenuItemIDs(items[i].Children, ids)
	}
}
