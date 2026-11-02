package emailauthoringpostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	emailcontentpostgres "github.com/echovisionlab/geul-api/internal/emailcontent/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// emailLayoutSortConfig defines allowed sort fields for email layouts
var emailLayoutSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"key":        "key",
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "name ASC, id ASC",
}

// emailLayoutFilterConfig defines filter fields for ListEmailLayouts.
var emailLayoutFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name", "key"},
		},
	},
}

// emailLayoutBaseRow inserts a layout with its content document.
type emailLayoutBaseRow struct {
	ID                string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	ContentDocumentID uuid.UUID `gorm:"column:content_document_id;type:uuid;not null"`
	SourceLocale      string    `gorm:"column:source_locale;type:text;not null"`
	Name              string    `gorm:"column:name;type:varchar(255);not null"`
	Key               string    `gorm:"column:key;type:varchar(100);unique;not null"`
	CreatedAt         time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (emailLayoutBaseRow) TableName() string {
	return "email_layout"
}

func layoutOf(row model.EmailLayout) emailauthoring.Layout {
	return emailauthoring.Layout{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale,
		Name: row.Name, Key: row.Key, HTMLContent: row.HTMLContent,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s *store) ListLayouts(ctx context.Context, query emailauthoring.ListQuery) ([]emailauthoring.Layout, int64, error) {
	list, err := emailLayoutFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.EmailLayout{}), query.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list, err = emailLayoutSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.EmailLayout
	if err := query.Page.Apply(list).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	layouts := make([]emailauthoring.Layout, len(rows))
	for index := range rows {
		layouts[index] = layoutOf(rows[index])
	}
	return layouts, total, nil
}

func (s *store) FindLayout(ctx context.Context, layoutID string) (emailauthoring.Layout, error) {
	var row model.EmailLayout
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", layoutID).Error; err != nil {
		return emailauthoring.Layout{}, notFound(err, emailauthoring.ErrLayoutNotFound)
	}
	return layoutOf(row), nil
}

func (s *store) LockLayout(ctx context.Context, layoutID string) (emailauthoring.Layout, error) {
	var row model.EmailLayout
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&row, "id = ?", layoutID).Error; err != nil {
		return emailauthoring.Layout{}, notFound(err, emailauthoring.ErrLayoutNotFound)
	}
	return layoutOf(row), nil
}

func (s *store) LockLayoutRoot(ctx context.Context, layoutID string, lock emailcontent.RowLock) error {
	var root emailLayoutBaseRow
	query := s.tx.WithContext(ctx).Table("email_layout").Where("id = ?", layoutID)
	if strength := emailcontentpostgres.Lock(lock); strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	return notFound(query.Take(&root).Error, emailauthoring.ErrLayoutNotFound)
}

func (s *store) LockLayouts(ctx context.Context, layoutIDs []string) ([]string, error) {
	var layouts []model.EmailLayout
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		Where("id IN ?", layoutIDs).
		Order("id ASC").
		Find(&layouts).Error; err != nil {
		return nil, err
	}
	locked := make([]string, 0, len(layouts))
	for _, layout := range layouts {
		locked = append(locked, layout.ID)
	}
	return locked, nil
}

func (s *store) LayoutAuthority(ctx context.Context, layoutID string, lock emailcontent.RowLock) (emailauthoring.LayoutAuthority, error) {
	query := s.tx.WithContext(ctx).
		Table("email_layout AS root").
		Select("root.id AS layout_id, root.source_locale, root.content_document_id, document.revision AS document_revision").
		Joins("JOIN content_document AS document ON document.id = root.content_document_id").
		Where("root.id = ?", layoutID)
	if strength := emailcontentpostgres.Lock(lock); strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength, Table: clause.Table{Name: "root"}})
	}
	var authority struct {
		LayoutID         string    `gorm:"column:layout_id"`
		SourceLocale     string    `gorm:"column:source_locale"`
		DocumentID       uuid.UUID `gorm:"column:content_document_id"`
		DocumentRevision uuid.UUID `gorm:"column:document_revision"`
	}
	if err := query.Take(&authority).Error; err != nil {
		return emailauthoring.LayoutAuthority{}, notFound(err, emailauthoring.ErrLayoutNotFound)
	}
	return emailauthoring.LayoutAuthority{
		LayoutID: authority.LayoutID, SourceLocale: authority.SourceLocale,
		DocumentID: authority.DocumentID, DocumentRevision: authority.DocumentRevision,
	}, nil
}

func (s *store) InsertLayout(ctx context.Context, layout *emailauthoring.Layout) error {
	baseRow := &emailLayoutBaseRow{
		ContentDocumentID: uuid.MustParse(layout.ContentDocumentID),
		SourceLocale:      layout.SourceLocale,
		Name:              layout.Name,
		Key:               layout.Key,
		CreatedAt:         layout.CreatedAt,
		UpdatedAt:         layout.UpdatedAt,
	}
	if err := s.tx.WithContext(ctx).
		Omit("ID").
		Clauses(clause.Returning{}).
		Create(baseRow).Error; err != nil {
		return keyTaken(err, emailauthoring.ErrLayoutKeyTaken)
	}
	layout.ID = baseRow.ID
	layout.CreatedAt = baseRow.CreatedAt
	layout.UpdatedAt = baseRow.UpdatedAt
	return nil
}

func (s *store) UpdateLayout(ctx context.Context, layoutID string, change emailauthoring.LayoutChange) error {
	updates := structured.Fields{"updated_at": change.UpdatedAt}
	if change.Name != nil {
		updates["name"] = *change.Name
	}
	if change.Key != nil {
		updates["key"] = *change.Key
	}
	return keyTaken(
		s.tx.WithContext(ctx).Model(&model.EmailLayout{ID: layoutID}).Updates(updates).Error,
		emailauthoring.ErrLayoutKeyTaken,
	)
}

func (s *store) DeleteLayout(ctx context.Context, layoutID string) error {
	return s.tx.WithContext(ctx).Delete(&model.EmailLayout{ID: layoutID}).Error
}

func (s *store) TemplateCounts(ctx context.Context, layoutIDs []string) (map[string]int32, error) {
	var rows []struct {
		ID    string
		Count int64
	}
	if err := s.tx.WithContext(ctx).Table("email_template").
		Select("layout_id AS id, COUNT(*) AS count").
		Where("layout_id IN ?", layoutIDs).
		Group("layout_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int32, len(rows))
	for _, row := range rows {
		counts[row.ID] = int32(row.Count)
	}
	return counts, nil
}

func (s *store) LayoutSourceLocale(ctx context.Context, layoutID string) (string, bool, error) {
	return email.ResolveLayoutTranslationSourceLocale(ctx, emailpostgres.NewStore(s.tx), layoutID)
}

func (s *store) CanonicalLayoutSource(ctx context.Context, layoutID, sourceLocale string) (email.LayoutTranslationDocument, error) {
	document, err := email.LoadCanonicalLayoutTranslationDocument(ctx, emailpostgres.NewStore(s.tx), layoutID, sourceLocale)
	if err != nil || document == nil {
		return email.LayoutTranslationDocument{}, err
	}
	return *document, nil
}

func (s *store) CanonicalLayoutSources(ctx context.Context, layoutIDs []string) (map[string]string, error) {
	projections, err := email.LoadCanonicalLayoutProjections(ctx, emailpostgres.NewStore(s.tx), layoutIDs)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]string, len(projections))
	for id, projection := range projections {
		sources[id] = projection.ContentHTML
	}
	return sources, nil
}

func (s *store) LayoutTranslationEntry(ctx context.Context, layoutID, locale string) (*email.LayoutTranslationEntry, error) {
	return email.LoadLayoutTranslationEntry(ctx, emailpostgres.NewStore(s.tx), layoutID, locale)
}

func (s *store) LockLayoutTranslationEntry(ctx context.Context, layoutID, locale string, lock emailcontent.RowLock) (*email.LayoutTranslationEntry, error) {
	var row struct {
		ContentHTML sql.NullString `gorm:"column:html_content"`
		ContentText sql.NullString `gorm:"column:content_text"`
		UpdatedAt   time.Time      `gorm:"column:updated_at"`
	}
	query := s.tx.WithContext(ctx).Table("email_layout_translation").
		Select("html_content, content_text, updated_at").
		Where("entity_id = ? AND locale = ?", layoutID, locale)
	if strength := emailcontentpostgres.Lock(lock); strength != "" {
		query = query.Clauses(clause.Locking{Strength: strength})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if result.Error != nil {
		return nil, result.Error
	}
	entry := &email.LayoutTranslationEntry{LayoutTranslationDocument: email.LayoutTranslationDocument{
		UpdatedAt: row.UpdatedAt,
	}}
	if row.ContentHTML.Valid {
		value := email.NormalizeTemplatePlaceholders(row.ContentHTML.String)
		entry.ContentHTML = &value
	}
	if row.ContentText.Valid {
		value := row.ContentText.String
		entry.ContentText = &value
	}
	return entry, nil
}

func (s *store) SaveLayoutSource(ctx context.Context, layoutID, locale string, document email.LayoutTranslationDocument, now time.Time) error {
	return email.SaveLayoutSourceLocaleDocument(ctx, emailpostgres.NewStore(s.tx), layoutID, locale, document, now)
}

func (s *store) UpsertLayoutTranslation(ctx context.Context, layoutID, locale string, input translation.EntryWrite) error {
	return email.UpsertLayoutTranslationEntry(ctx, emailpostgres.NewStore(s.tx), layoutID, locale, input)
}

func (s *store) SeedLayoutTranslation(ctx context.Context, layoutID, locale, sourceLocale string, now time.Time) error {
	return email.SeedLayoutTranslationEntryFromSource(ctx, emailpostgres.NewStore(s.tx), layoutID, locale, sourceLocale, now)
}

func (s *store) DeleteLayoutTranslation(ctx context.Context, layoutID, locale string) (bool, error) {
	result := s.tx.WithContext(ctx).Exec(
		"DELETE FROM email_layout_translation WHERE entity_id = ? AND locale = ?",
		layoutID, locale,
	)
	return result.RowsAffected == 1, result.Error
}

func (s *store) LocalizedLayoutHTML(ctx context.Context, layoutID, locale string) (string, error) {
	html, _, err := email.ResolveLocalizedEmailLayoutHTML(ctx, emailpostgres.NewStore(s.tx), layoutID, locale)
	return html, err
}
