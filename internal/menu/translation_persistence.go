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

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// ApplyProviderTranslationCandidateWithDB applies only the request-time Menu
// label units that still belong to the current Menu graph. A source-locale
// switch changes which locale row is source-owned, not whether the accepted
// provider response may update its surviving stable labels.
func ApplyProviderTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if tx == nil || job == nil || job.EntityType != "menu" || candidate == nil || !candidate.HasProviderUnitPatch() {
		return errs.Internal(errors.New("menu provider translation candidate is required"))
	}
	if auditWriter == nil {
		return errs.Internal(errors.New("menu provider translation Audit writer is required"))
	}
	memberID, err := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
	if err != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
		return errs.InternalMsg("Menu provider translation requires canonical requester Member")
	}
	now := input.Now.UTC()
	if now.IsZero() {
		return errs.InternalMsg("Menu provider translation time is required")
	}
	root, err := lockMenuForUpdate(ctx, tx, job.EntityID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFound("menu", job.EntityID)
	}
	if err != nil {
		return errs.Internal(err)
	}
	current, err := loadMenuAIDocumentSnapshotFromRoot(ctx, tx, *root, job.TargetLocale, true)
	if err != nil {
		return err
	}
	patch, _ := candidate.ProviderPatch()
	incoming := make(map[string]string, len(patch.Results))
	for _, unit := range patch.Units {
		result, ok := patch.Results[unit.UnitID]
		if !ok || unit.ContainerType != translation.ContainerTypeBlock || unit.FieldName != "label" ||
			unit.UnitID != "item:"+unit.ContainerID+":label" {
			continue
		}
		incoming[unit.ContainerID] = result.TranslatedText
	}
	incoming = canonicalMenuAIDocumentLabels(current.Items, job.TargetLocale, incoming)
	next := cloneMenuAIDocumentSnapshot(current)
	if next.Labels == nil {
		next.Labels = make(map[string]string)
	}
	maps.Copy(next.Labels, incoming)
	if maps.Equal(current.Labels, next.Labels) && current.LocaleExists {
		return nil
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if job.TargetLocale == current.SourceLocale {
		if !current.SourceValuesStored {
			return errs.FailedPrecondition("Menu source locale values are not initialized")
		}
		if maps.Equal(current.Labels, next.Labels) {
			return nil
		}
		if err := persistMenuAIDocumentSourceValues(ctx, tx, current, next, now); err != nil {
			return err
		}
		expected, err := parseMenuContentDocumentUUID(current.DocumentRevision, "content_document.revision")
		if err != nil {
			return err
		}
		if _, err := advanceMenuContentDocument(ctx, tx, current.ID, current.contentDocumentID, expected, now); err != nil {
			return err
		}
	} else {
		if !current.LocaleExists {
			next.LocaleExists = true
			operation = sharedtelemetry.AuditItemOperationCreated
		}
		if err := persistMenuAIDocumentTarget(ctx, tx, current, next, false, now); err != nil {
			return err
		}
	}
	return domainaudit.AppendMember(
		ctx, tx, auditWriter, memberID.String(), sharedtelemetry.AuditMenuUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMenuLocaleContentAuditRecord(metadata, current.ID, job.TargetLocale, operation)
		},
	)
}

func decodeSourceItems(body []byte) ([]model.MenuItem, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var items []model.MenuItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, errs.Internal(err)
	}
	return items, nil
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
	seen := map[string]struct{}{}
	if len(body) == 0 {
		return labels, nil
	}
	var items []translationLabelItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, errs.InvalidArgument("content_json", "must be a valid Menu label document")
	}
	var collect func([]translationLabelItem) error
	collect = func(current []translationLabelItem) error {
		for _, item := range current {
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
			if err := collect(item.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(items); err != nil {
		return nil, errs.InvalidArgument("content_json", err.Error())
	}
	return labels, nil
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

func initializeCurrentSourceLabelValues(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	sourceJSON []byte,
	rootUpdatedAt time.Time,
) error {
	var root struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).Table("menu").Select("source_locale").
		Where("id = ?", menuID).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("menu", menuID)
		}
		return errs.Internal(err)
	}
	root.SourceLocale = strings.TrimSpace(root.SourceLocale)
	if root.SourceLocale == "" {
		return errs.FailedPrecondition("Menu source locale is not initialized")
	}
	sourceItems, err := decodeSourceItems(sourceJSON)
	if err != nil {
		return err
	}
	labels := make(map[string]string)
	collectMenuLocaleLabelValues(sourceItems, root.SourceLocale, labels)
	body, err := EncodeTranslationLabelValues(labels)
	if err != nil {
		return errs.Internal(err)
	}
	now := rootUpdatedAt.UTC()
	if now.IsZero() {
		return errs.FailedPrecondition("Menu source timestamp is not initialized")
	}
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO menu_translation (entity_id, locale, items_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		menuID, root.SourceLocale, string(body), now, now,
	).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

// SyncCurrentSourceLabelValues writes the full current source-locale label set
// supplied by the direct Menu source editor. It requires the source version
// row to exist and never creates or mutates a non-source locale row.
func SyncCurrentSourceLabelValues(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	sourceItems []model.MenuItem,
	now time.Time,
) error {
	var root struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).Table("menu").Select("source_locale").Where("id = ?", menuID).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("menu", menuID)
		}
		return errs.Internal(err)
	}
	root.SourceLocale = strings.TrimSpace(root.SourceLocale)
	if root.SourceLocale == "" {
		return errs.FailedPrecondition("Menu source locale is not initialized")
	}
	type sourceRow struct {
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	var row sourceRow
	if err := db.WithContext(ctx).Table("menu_translation").Clauses(clause.Locking{Strength: "UPDATE"}).Select("items_json, updated_at").
		Where("entity_id = ? AND locale = ?", menuID, root.SourceLocale).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.FailedPrecondition("Menu source locale values are not initialized")
		}
		return errs.Internal(err)
	}
	current, err := DecodeTranslationLabelValues(row.ItemsJSON)
	if err != nil {
		return errs.Internal(err)
	}
	next := make(map[string]string)
	collectMenuLocaleLabelValues(sourceItems, root.SourceLocale, next)
	if maps.Equal(current, next) {
		return nil
	}
	body, err := EncodeTranslationLabelValues(next)
	if err != nil {
		return errs.Internal(err)
	}
	if err := db.WithContext(ctx).Exec(
		`UPDATE menu_translation SET items_json = ?, updated_at = ?
		 WHERE entity_id = ? AND locale = ?`,
		string(body), translation.NextTargetUpdatedAt(now, row.UpdatedAt), menuID, root.SourceLocale,
	).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}

func collectMenuLocaleLabelValues(items []model.MenuItem, locale string, labels map[string]string) {
	for i := range items {
		if id := strings.TrimSpace(items[i].ID); id != "" && ShouldTranslateItemLabel(&items[i], locale) {
			labels[id] = items[i].Label
		}
		collectMenuLocaleLabelValues(items[i].Children, locale, labels)
	}
}

// ReconcileTranslationRowsWithSourceJSON removes target values whose stable
// source item no longer exists. Other source mutations leave target rows and
// their revisions unchanged.
func ReconcileTranslationRowsWithSourceJSON(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	sourceJSON []byte,
	now time.Time,
) error {
	sourceItems, err := decodeSourceItems(sourceJSON)
	if err != nil {
		return err
	}
	return ReconcileTranslationRowsWithSourceItems(ctx, db, menuID, sourceItems, now)
}

// ReconcileTranslationRowsWithSourceItems prunes only labels belonging to
// deleted source item IDs. It never creates rows, seeds new labels, or rewrites
// an unchanged target row.
func ReconcileTranslationRowsWithSourceItems(
	ctx context.Context,
	db *gorm.DB,
	menuID string,
	sourceItems []model.MenuItem,
	now time.Time,
) error {
	liveIDs := make(map[string]struct{})
	collectMenuItemIDs(sourceItems, liveIDs)

	type translationRow struct {
		Locale    string    `gorm:"column:locale"`
		ItemsJSON []byte    `gorm:"column:items_json"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	var rows []translationRow
	if err := db.WithContext(ctx).
		Table("menu_translation").
		Select("locale, items_json, updated_at").
		Where("entity_id = ?", menuID).
		Find(&rows).Error; err != nil {
		return errs.Internal(err)
	}

	for _, row := range rows {
		labels, err := DecodeTranslationLabelValues(row.ItemsJSON)
		if err != nil {
			return errs.Internal(err)
		}
		pruned := false
		for id := range labels {
			if _, exists := liveIDs[id]; exists {
				continue
			}
			delete(labels, id)
			pruned = true
		}
		if !pruned {
			continue
		}
		nextJSON, err := EncodeTranslationLabelValues(labels)
		if err != nil {
			return errs.Internal(err)
		}
		if err := db.WithContext(ctx).Exec(
			`UPDATE menu_translation
			 SET items_json = ?, updated_at = ?
			 WHERE entity_id = ? AND locale = ?`,
			string(nextJSON), translation.NextTargetUpdatedAt(now, row.UpdatedAt), menuID, row.Locale,
		).Error; err != nil {
			return errs.Internal(err)
		}
	}
	return nil
}

func collectMenuItemIDs(items []model.MenuItem, ids map[string]struct{}) {
	for i := range items {
		if id := strings.TrimSpace(items[i].ID); id != "" {
			ids[id] = struct{}{}
		}
		collectMenuItemIDs(items[i].Children, ids)
	}
}
