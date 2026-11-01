package legalpostgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// policyRow is a row of terms_history or privacy_history; the kinds share
// the columns.
type policyRow struct {
	ID                string     `gorm:"column:id;primaryKey"`
	Version           int        `gorm:"column:version"`
	Title             string     `gorm:"column:title"`
	Status            string     `gorm:"column:status"`
	ContentDocumentID *string    `gorm:"column:content_document_id"`
	EffectiveFrom     *time.Time `gorm:"column:effective_from"`
	EffectiveUntil    *time.Time `gorm:"column:effective_until"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (row policyRow) version() legal.Version {
	return legal.Version{
		ID: row.ID, Version: row.Version, Title: row.Title, Status: legal.Status(row.Status),
		ContentDocumentID: row.ContentDocumentID,
		EffectiveFrom:     row.EffectiveFrom, EffectiveUntil: row.EffectiveUntil,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

type rootRow struct {
	ID                string     `gorm:"column:id"`
	Title             string     `gorm:"column:title"`
	Status            string     `gorm:"column:status"`
	Version           int        `gorm:"column:version"`
	SourceLocale      string     `gorm:"column:source_locale"`
	ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
}

func (row rootRow) root() legal.Root {
	return legal.Root{
		ID: row.ID, Title: row.Title, Status: legal.Status(row.Status), Version: row.Version,
		SourceLocale: row.SourceLocale, ContentDocumentID: row.ContentDocumentID,
	}
}

func (s *store) LockVersionAssignment(ctx context.Context, kind legal.PolicyKind) error {
	return s.tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(hashtext(?))", historyTable(kind)+"_version").Error
}

func (s *store) Version(ctx context.Context, kind legal.PolicyKind, id string, lock legal.RowLock) (legal.Version, error) {
	var row policyRow
	if err := s.tx.WithContext(ctx).Table(historyTable(kind)).Clauses(locking(lock)...).
		First(&row, "id = ?", id).Error; err != nil {
		return legal.Version{}, notFound(err, legal.ErrPolicyNotFound)
	}
	return row.version(), nil
}

func (s *store) Root(ctx context.Context, kind legal.PolicyKind, id string, lock legal.RowLock) (legal.Root, error) {
	var row rootRow
	if err := s.tx.WithContext(ctx).Table(historyTable(kind)).
		Select("id", "title", "status", "version", "source_locale", "content_document_id").
		Where("id = ?", id).Clauses(locking(lock)...).
		Take(&row).Error; err != nil {
		return legal.Root{}, notFound(err, legal.ErrPolicyNotFound)
	}
	return row.root(), nil
}

func (s *store) LockStatus(ctx context.Context, kind legal.PolicyKind, id string) (legal.Status, error) {
	var row struct {
		Status string `gorm:"column:status"`
	}
	if err := s.tx.WithContext(ctx).Table(historyTable(kind)).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("status").Where("id = ?", id).Take(&row).Error; err != nil {
		return "", notFound(err, legal.ErrPolicyNotFound)
	}
	return legal.Status(row.Status), nil
}

func (s *store) ScheduleState(ctx context.Context, kind legal.PolicyKind, id string) (legal.Lifecycle, error) {
	var row struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
	}
	if err := s.tx.WithContext(ctx).Table(historyTable(kind)).
		Select("status", "effective_from").
		Where("id = ?", id).
		Take(&row).Error; err != nil {
		return legal.Lifecycle{}, notFound(err, legal.ErrPolicyNotFound)
	}
	return legal.Lifecycle{Status: legal.Status(row.Status), EffectiveFrom: row.EffectiveFrom}, nil
}

func (s *store) ListVersions(ctx context.Context, kind legal.PolicyKind, status *legal.Status, limit, offset int32) ([]legal.Version, int64, error) {
	list := s.tx.WithContext(ctx).Table(historyTable(kind))
	if status != nil {
		list = list.Where("status = ?", *status)
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []policyRow
	if err := list.Order("version DESC").Limit(int(limit)).Offset(int(offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	versions := make([]legal.Version, len(rows))
	for i, row := range rows {
		versions[i] = row.version()
	}
	return versions, total, nil
}

func (s *store) InsertDraft(ctx context.Context, kind legal.PolicyKind, draft legal.Draft) (legal.Version, error) {
	table := historyTable(kind)
	var row policyRow
	if err := s.tx.WithContext(ctx).Raw(`
		INSERT INTO `+table+` (title, content, status, version, created_at, updated_at, content_document_id, source_locale)
		VALUES (?, '', ?, GREATEST(COALESCE((SELECT MAX(version) FROM `+table+`), 0), ?::integer) + 1, ?, ?, ?, ?)
		RETURNING id, version, title, content, status, created_at, updated_at, content_document_id
	`, draft.Title, status(kind, "draft"), draft.SealedVersion, draft.CreatedAt, draft.CreatedAt, draft.ContentDocumentID, draft.SourceLocale).Scan(&row).Error; err != nil {
		return legal.Version{}, err
	}
	return row.version(), nil
}

func (s *store) Schedule(ctx context.Context, kind legal.PolicyKind, id string, effectiveFrom, at time.Time) error {
	return s.tx.WithContext(ctx).Table(historyTable(kind)).Where("id = ?", id).Updates(structured.Fields{
		"status": status(kind, "scheduled"), "effective_from": effectiveFrom, "updated_at": at,
	}).Error
}

func (s *store) Unschedule(ctx context.Context, kind legal.PolicyKind, id string, at time.Time) error {
	return s.tx.WithContext(ctx).Table(historyTable(kind)).Where("id = ?", id).Updates(structured.Fields{
		"status": status(kind, "draft"), "effective_from": nil, "updated_at": at,
	}).Error
}

func (s *store) LockScheduledIDs(ctx context.Context, kind legal.PolicyKind, exceptID string) ([]string, error) {
	var ids []string
	err := s.tx.WithContext(ctx).Raw(
		"SELECT id FROM "+historyTable(kind)+" WHERE id <> ? AND status = ? ORDER BY id ASC FOR UPDATE",
		exceptID, status(kind, "scheduled"),
	).Scan(&ids).Error
	return ids, err
}

func (s *store) LockLifecycle(ctx context.Context, kind legal.PolicyKind, id string) (legal.Lifecycle, bool, error) {
	var row struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
		Version       int        `gorm:"column:version"`
	}
	result := s.tx.WithContext(ctx).Raw(
		"SELECT status, effective_from, version FROM "+historyTable(kind)+" WHERE id = ? FOR UPDATE", id,
	).Scan(&row)
	if result.Error != nil {
		return legal.Lifecycle{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return legal.Lifecycle{}, false, nil
	}
	return legal.Lifecycle{Status: legal.Status(row.Status), EffectiveFrom: row.EffectiveFrom, Version: row.Version}, true, nil
}

func (s *store) LockActiveVersions(ctx context.Context, kind legal.PolicyKind, exceptID string) ([]legal.VersionRef, error) {
	var rows []struct {
		ID      string `gorm:"column:id"`
		Version int    `gorm:"column:version"`
	}
	if err := s.tx.WithContext(ctx).Raw(
		"SELECT id, version FROM "+historyTable(kind)+" WHERE id <> ? AND status = ? ORDER BY id ASC FOR UPDATE",
		exceptID, status(kind, "active"),
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	refs := make([]legal.VersionRef, len(rows))
	for i, row := range rows {
		refs[i] = legal.VersionRef{ID: row.ID, Version: row.Version}
	}
	return refs, nil
}

func (s *store) Archive(ctx context.Context, kind legal.PolicyKind, id string, at time.Time) error {
	return s.tx.WithContext(ctx).Table(historyTable(kind)).Where("id = ?", id).Updates(structured.Fields{
		"status": status(kind, "archived"), "effective_until": at, "updated_at": at,
	}).Error
}

func (s *store) Activate(ctx context.Context, kind legal.PolicyKind, id string, from []legal.Status, effectiveFrom *time.Time, at time.Time) error {
	updates := structured.Fields{"status": status(kind, "active"), "updated_at": at}
	if effectiveFrom != nil {
		updates["effective_from"] = *effectiveFrom
	}
	activation := s.tx.WithContext(ctx).Table(historyTable(kind)).
		Where("id = ? AND status IN ?", id, from).
		Updates(updates)
	if activation.Error != nil {
		return activation.Error
	}
	if activation.RowsAffected != 1 {
		return notFound(gorm.ErrRecordNotFound, legal.ErrPolicyNotFound)
	}
	return nil
}

func (s *store) DeleteVersion(ctx context.Context, kind legal.PolicyKind, id string) error {
	return s.tx.WithContext(ctx).Table(historyTable(kind)).Where("id = ?", id).Delete(&policyRow{}).Error
}

func (s *store) SetTitle(ctx context.Context, kind legal.PolicyKind, id, title string, at time.Time) error {
	return s.tx.WithContext(ctx).Table(historyTable(kind)).Where("id = ?", id).
		Updates(structured.Fields{"title": title, "updated_at": at}).Error
}

func (s *store) ReplaceTitle(ctx context.Context, kind legal.PolicyKind, id, title string, at time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Table(historyTable(kind)).
		Where("id = ? AND title IS DISTINCT FROM ?", id, title).
		Updates(structured.Fields{"title": title, "updated_at": at})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected != 0, nil
}

func (s *store) StoreSourceProjection(ctx context.Context, kind legal.PolicyKind, id string, projection legal.Projection, at time.Time) error {
	return s.tx.WithContext(ctx).Table(historyTable(kind)).Where("id = ?", id).
		Updates(structured.Fields{
			"content":      projection.HTML,
			"content_text": projection.Text,
			"content_hash": projection.Digest,
			"view_hash":    projection.Digest,
			"updated_at":   at,
		}).Error
}
