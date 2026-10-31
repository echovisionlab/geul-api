package page

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// pageEntityName names the owner in shared content document messages.
const pageEntityName = "Page"

type pageLocaleMetadataRow struct {
	Locale        string    `gorm:"column:locale"`
	Title         *string   `gorm:"column:title"`
	Summary       *string   `gorm:"column:summary"`
	IncarnationID uuid.UUID `gorm:"column:incarnation_id"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

type pageTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata pageLocaleMetadataRow
	TargetMetadata *pageLocaleMetadataRow
	TargetRevision string
}

func (state pageTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: pageEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

type pageTargetMetadataPatch struct {
	EnsureLocale  bool
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
	DeleteLocale  bool
}

func normalizePageDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func pageSourceRoomFence(locale string, base contentblock.DomainFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		domain, err := base(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != locale {
			return contentblock.DomainContext{}, contentblock.LocaleOwnershipConflict(ctx, tx, documentID,
				"Page source locale changed since the room was loaded; reload before saving",
			)
		}
		return domain, nil
	}
}

func loadOptionalPageLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	locale string,
	forUpdate bool,
) (pageLocaleMetadataRow, bool, error) {
	query := tx.WithContext(ctx).Table("page_translation").
		Select("locale", "title", "summary", "incarnation_id", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", pageID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row pageLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pageLocaleMetadataRow{}, false, nil
		}
		return pageLocaleMetadataRow{}, false, err
	}
	return row, true, nil
}

func derivePageTargetRevision(documentRevision string, metadata pageLocaleMetadataRow) (string, error) {
	if metadata.IncarnationID == uuid.Nil {
		return "", errors.New("Page translation locale incarnation is missing")
	}
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision,
		LocaleIncarnation: metadata.IncarnationID.String(), LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
	}
	return revision, nil
}

func loadPageTargetLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	pageID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (pageTargetLocaleState, error) {
	locale, err := normalizePageDocumentLocale(locale)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	lock := clause.LockingStrengthShare
	if forUpdate {
		lock = clause.LockingStrengthUpdate
	}
	_, lockedDocumentID, err := loadPageAIDocumentRoot(ctx, tx, pageID, lock)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if lockedDocumentID != documentID {
		return pageTargetLocaleState{}, domainerr.Precondition("Page content document changed; reload before saving")
	}
	domain, err := loadPageContentDomainContext(ctx, tx, pageID)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return pageTargetLocaleState{}, normalizePageContentBlockError(err)
	}
	if snapshot.SourceLocale != domain.SourceLocale || snapshot.Document.Profile != "page" {
		return pageTargetLocaleState{}, domainerr.Precondition("Page content document source or profile is inconsistent")
	}
	sourceMetadata, sourceExists, err := loadOptionalPageLocaleMetadataRow(ctx, tx, pageID, domain.SourceLocale, forUpdate)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if !sourceExists {
		return pageTargetLocaleState{}, domainerr.Precondition("Page source locale metadata is missing")
	}
	state := pageTargetLocaleState{Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: sourceMetadata}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &sourceMetadata
		return state, nil
	}
	target, exists, err := loadOptionalPageLocaleMetadataRow(ctx, tx, pageID, locale, forUpdate)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if !exists {
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return pageTargetLocaleState{}, domainerr.Precondition("Page target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = derivePageTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	return state, nil
}

// pageTargetLocaleMutationInput is one write to a Page target locale.
type pageTargetLocaleMutationInput struct {
	PageID                 string
	DocumentID             uuid.UUID
	Locale                 string
	Batch                  contentblock.Batch
	ExpectedTargetRevision *string
	Patch                  pageTargetMetadataPatch
	Policy                 targetlocale.Policy
	Now                    time.Time
	Fence                  contentblock.DomainFence
}

func applyPageTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input pageTargetLocaleMutationInput,
) (contentblock.Result, *string, error) {
	pageID, documentID, locale, patch := input.PageID, input.DocumentID, input.Locale, input.Patch
	domain, err := input.Fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	state, err := loadPageTargetLocaleState(ctx, tx, store, pageID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, nil, domainerr.InvalidField("locale", "Page target locale must differ from source locale")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, nil, domainerr.Precondition("Page source locale changed; reload before saving")
	}
	if patch.DeleteLocale && (patch.EnsureLocale || patch.UpdateTitle || patch.UpdateSummary || len(input.Batch.LocaleGroups) != 0) {
		return contentblock.Result{}, nil, domainerr.InvalidField("operations", "Page translation deletion must be exclusive")
	}
	batch, err := input.Policy.Admit(state.locked(), locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if patch.DeleteLocale && state.TargetMetadata == nil {
		return contentblock.Result{}, nil, domainerr.Precondition("Page target locale must exist before deletion")
	}
	now := targetlocale.WriteTime(input.Now)
	if patch.DeleteLocale {
		batch.LocaleGroups = targetlocale.DeleteGroups(state.Snapshot, locale)
	}
	write := &pageTargetMetadataWrite{pageID: pageID, locale: locale, state: state, patch: patch, now: now}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, contentblock.LockedFence(pageEntityName, documentID, domain), write.apply,
	)
	if err != nil {
		return contentblock.Result{}, nil, normalizePageContentBlockError(err)
	}
	if patch.DeleteLocale {
		return result, nil, nil
	}
	targetRevision, err := derivePageTargetRevision(result.DocumentRevision.String(), write.final)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	return result, &targetRevision, nil
}

// pageTargetMetadataWrite stores the Page target locale row of one write and
// keeps the row it leaves behind.
type pageTargetMetadataWrite struct {
	pageID string
	locale string
	state  pageTargetLocaleState
	patch  pageTargetMetadataPatch
	now    time.Time
	final  pageLocaleMetadataRow
}

func (w *pageTargetMetadataWrite) apply(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
	switch {
	case w.patch.DeleteLocale:
		deleted := tx.WithContext(ctx).Exec("DELETE FROM page_translation WHERE entity_id = ?::uuid AND locale = ?", w.pageID, w.locale)
		if deleted.Error != nil {
			return contentblock.MetadataEffect{}, deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return contentblock.MetadataEffect{}, errors.New("Page target locale disappeared while deleting")
		}
	case w.state.TargetMetadata == nil:
		if err := w.create(ctx, tx); err != nil {
			return contentblock.MetadataEffect{}, err
		}
	default:
		changed, err := w.update(ctx, tx, contentChanged)
		if err != nil || !changed {
			return contentblock.MetadataEffect{}, err
		}
	}
	return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.locale}}, nil
}

func (w *pageTargetMetadataWrite) create(ctx context.Context, tx *gorm.DB) error {
	var inserted struct {
		IncarnationID uuid.UUID `gorm:"column:incarnation_id"`
	}
	created := tx.WithContext(ctx).Raw(
		"INSERT INTO page_translation (entity_id, locale, title, summary, created_at, updated_at) VALUES (?::uuid, ?, ?, ?, ?, ?) RETURNING incarnation_id",
		w.pageID, w.locale, w.patch.Title, w.patch.Summary, w.now, w.now,
	).Scan(&inserted)
	if created.Error != nil {
		return created.Error
	}
	if created.RowsAffected != 1 || inserted.IncarnationID == uuid.Nil {
		return errors.New("Page target locale could not be created")
	}
	w.final = pageLocaleMetadataRow{
		Locale: w.locale, Title: cloneOptionalString(w.patch.Title), Summary: cloneOptionalString(w.patch.Summary),
		IncarnationID: inserted.IncarnationID, UpdatedAt: w.now,
	}
	return nil
}

func (w *pageTargetMetadataWrite) update(ctx context.Context, tx *gorm.DB, contentChanged bool) (bool, error) {
	w.final = *w.state.TargetMetadata
	metadataChanged := false
	updates := map[string]any{}
	if w.patch.UpdateTitle && !nullableStringEqual(w.final.Title, w.patch.Title) {
		w.final.Title, updates["title"], metadataChanged = cloneOptionalString(w.patch.Title), w.patch.Title, true
	}
	if w.patch.UpdateSummary && !nullableStringEqual(w.final.Summary, w.patch.Summary) {
		w.final.Summary, updates["summary"], metadataChanged = cloneOptionalString(w.patch.Summary), w.patch.Summary, true
	}
	if !contentChanged && !metadataChanged {
		return false, nil
	}
	w.final.UpdatedAt = translation.NextTargetUpdatedAt(w.now, w.final.UpdatedAt)
	updates["updated_at"] = w.final.UpdatedAt
	updated := tx.WithContext(ctx).Table("page_translation").
		Where("entity_id = ?::uuid AND locale = ?", w.pageID, w.locale).Updates(updates)
	if updated.Error != nil {
		return false, updated.Error
	}
	if updated.RowsAffected != 1 {
		return false, errors.New("Page target locale disappeared while saving")
	}
	return true, nil
}

func pageContributorUUIDs(values []string) ([]uuid.UUID, error) {
	contributors := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		contributor, err := parsePageContentUUID("contributor_member_ids", value)
		if err != nil {
			return nil, err
		}
		contributors = append(contributors, contributor)
	}
	return contributors, nil
}
