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

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
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
