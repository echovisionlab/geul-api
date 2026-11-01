// Package postgres is the PostgreSQL Store of the Post Series domain: the
// SQL, row locks and constraint mapping of the series, series_translation,
// series_manager and content_document rows, and the bindings of the other
// domains that act in a Series transaction.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Media is the media and OG runtime a Series transaction binds
// (seriesadapter.Runtime).
type Media interface {
	RequireAttachableFile(ctx context.Context, tx *gorm.DB, fileID string) error
	BindFeaturedImage(ctx context.Context, tx *gorm.DB, fileID, seriesID string) (*series.Asset, error)
	ReleaseFeaturedImage(ctx context.Context, tx *gorm.DB, seriesID string) error
	CancelAndReleaseOG(ctx context.Context, tx *gorm.DB, seriesID string) error
	RequestCurrent(ctx context.Context, tx *gorm.DB, seriesID, locale string, allLocales bool, reason string) (*string, error)
}

// MenuTargets keeps Menu-owned Series links synchronized in the caller
// transaction (menu.TargetLifecycle).
type MenuTargets interface {
	UpdateSlug(ctx context.Context, tx *gorm.DB, linkType, targetID, oldSlug, newSlug string) error
	Remove(ctx context.Context, tx *gorm.DB, linkType, targetID, slug string) error
}

// PostMembership reads and writes the Post rows of a membership change
// (postadapter.SeriesMembership). ReadSeriesRelation, LockSeriesRelation and
// AssignSeries report gorm.ErrRecordNotFound for a missing Post; ClearSeries
// and OrderSeries report series.ErrPostRelationChanged.
type PostMembership interface {
	ListSeriesPosts(ctx context.Context, db *gorm.DB, seriesID string) ([]series.SeriesPost, error)
	SeriesPostIDs(ctx context.Context, tx *gorm.DB, seriesID string) ([]string, error)
	ReadSeriesRelation(ctx context.Context, db *gorm.DB, postID string) (*string, error)
	LockSeriesRelation(ctx context.Context, tx *gorm.DB, postID string) (*string, error)
	LockSeriesPosts(ctx context.Context, tx *gorm.DB, postIDs []string) (int, error)
	NextSeriesOrder(ctx context.Context, tx *gorm.DB, seriesID string) (int, error)
	AssignSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, order int, now time.Time) error
	ClearSeries(ctx context.Context, tx *gorm.DB, postID, seriesID string, now time.Time) error
	ClearAllSeries(ctx context.Context, tx *gorm.DB, seriesID string, now time.Time) error
	OrderSeries(ctx context.Context, tx *gorm.DB, seriesID string, postIDs []string, now time.Time) error
}

// PostAccess owns the Post edit decision a membership change requires
// (seriesadapter.PostAccess).
type PostAccess interface {
	RequireLockedEdit(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, postID string) error
}

// Checkpoints authorizes the contributors of a collaboration save inside
// its transaction (collaboration.CheckpointFence).
type Checkpoints interface {
	RequireCurrentContributors(ctx context.Context, tx *gorm.DB, resourceType intrav1.CollaborationResourceType, resourceID string, memberIDs []string) error
}

// Collaborators are the other domains a Series transaction reaches. Every
// field is required.
type Collaborators struct {
	Media       Media
	MenuTargets MenuTargets
	Posts       PostMembership
	PostAccess  PostAccess
	SpiceDB     *auth.SpiceDBClient
	Checkpoints Checkpoints
}

func (c Collaborators) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Media": c.Media == nil, "MenuTargets": c.MenuTargets == nil, "Posts": c.Posts == nil,
		"PostAccess": c.PostAccess == nil, "SpiceDB": c.SpiceDB == nil, "Checkpoints": c.Checkpoints == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("series store collaborators are required: %s", strings.Join(sortedStrings(missing), ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Series use cases over
// db.
func NewUnits(db *gorm.DB, collaborators Collaborators) (mutation.UnitOfWork[series.Store], error) {
	if db == nil {
		return nil, errors.New("series store database is required")
	}
	if err := collaborators.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) series.Store {
		return store{translationStore: translationStore{tx: tx}, with: collaborators}
	}), nil
}

// NewTranslationStore binds the Series translation store to tx, a
// transaction of the translation application.
func NewTranslationStore(tx *gorm.DB) series.TranslationStore {
	return translationStore{tx: tx}
}

type translationStore struct {
	tx *gorm.DB
}

type store struct {
	translationStore
	with Collaborators
}

var _ series.Store = store{}

// slugTakenError is a unique violation of the Series slug; the driver error
// stays readable and in the chain.
type slugTakenError struct{ error }

func (err slugTakenError) Is(target error) bool { return target == series.ErrSlugTaken }
func (err slugTakenError) Unwrap() error        { return err.error }

func slugError(err error) error {
	if dberrors.IsUniqueViolation(err) {
		return slugTakenError{err}
	}
	return err
}

func (s translationStore) LockSeries(ctx context.Context, seriesID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	err := s.tx.WithContext(ctx).Table("series").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		Where("id = ?", seriesID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return series.ErrSeriesNotFound
	}
	return err
}

type sourceCopyRow struct {
	EntityID string         `gorm:"column:entity_id"`
	Title    sql.NullString `gorm:"column:title"`
	Summary  sql.NullString `gorm:"column:summary"`
	OgAsset  sql.NullString `gorm:"column:og_asset_id"`
}

func (s translationStore) SourceCopies(ctx context.Context, seriesIDs []string) (map[string]series.SourceCopy, error) {
	var rows []sourceCopyRow
	if err := s.tx.WithContext(ctx).Raw(
		`SELECT translation.entity_id::text AS entity_id, translation.title,
		        translation.summary, translation.og_asset_id
		 FROM series_translation AS translation
		 JOIN series AS root ON root.id = translation.entity_id
		  AND root.source_locale = translation.locale
		 WHERE translation.entity_id::text IN ?`,
		seriesIDs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	copies := make(map[string]series.SourceCopy, len(rows))
	for _, row := range rows {
		var source series.SourceCopy
		if row.Title.Valid {
			source.Title = stringPointer(row.Title.String)
		}
		if row.Summary.Valid {
			source.Summary = stringPointer(row.Summary.String)
			source.ContentText = stringPointer(row.Summary.String)
		}
		if row.OgAsset.Valid {
			source.OgAssetID = stringPointer(row.OgAsset.String)
		}
		copies[row.EntityID] = source
	}
	return copies, nil
}

func (s translationStore) Translation(ctx context.Context, seriesID, locale string, lock bool) (series.Translation, error) {
	query := s.tx.WithContext(ctx).Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, locale)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row model.SeriesTranslation
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return series.Translation{}, series.ErrTranslationNotFound
		}
		return series.Translation{}, err
	}
	return translationFromRow(row), nil
}

func (s translationStore) UpsertTranslation(ctx context.Context, seriesID, locale string, write series.TranslationWrite) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO series_translation (
			entity_id, locale,
			title, summary, content_json, content_html, content_text,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			content_json = EXCLUDED.content_json,
			content_html = EXCLUDED.content_html,
			content_text = EXCLUDED.content_text,
			updated_at = EXCLUDED.updated_at`,
		seriesID,
		locale,
		write.Title,
		write.Summary,
		jsonValueOrNil(write.ContentJSON),
		write.ContentHTML,
		write.ContentText,
		write.Now,
		write.Now,
	).Error
}

func (s translationStore) InsertTranslation(ctx context.Context, translation series.Translation) error {
	row := translationRow(translation)
	return s.tx.WithContext(ctx).Table("series_translation").Create(&row).Error
}

func (s translationStore) UpdateTranslation(ctx context.Context, seriesID, locale string, title, summary, contentText *string, updatedAt time.Time) error {
	result := s.tx.WithContext(ctx).Table("series_translation").
		Where("entity_id = ? AND locale = ?", seriesID, locale).
		Updates(structured.Fields{
			"title": title, "summary": summary, "content_text": contentText, "updated_at": updatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return series.ErrTranslationNotFound
	}
	return nil
}

func (s translationStore) DeleteTranslation(ctx context.Context, seriesID, locale string) error {
	result := s.tx.WithContext(ctx).Table("series_translation").Where("entity_id = ? AND locale = ?", seriesID, locale).
		Delete(&model.SeriesTranslation{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return series.ErrTranslationNotFound
	}
	return nil
}

func (s translationStore) SetSourceLocale(ctx context.Context, seriesID, locale string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.Series{}).Where("id = ?", seriesID).
		Updates(structured.Fields{"source_locale": locale, "updated_at": now}).Error
}

func (s translationStore) ContentDocumentRoot(ctx context.Context, seriesID string, lock bool) (series.ContentDocumentRoot, error) {
	query := s.tx.WithContext(ctx).Table("series").
		Select("id, source_locale, content_document_id").
		Where("id = ?", seriesID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var root struct {
		ID                string         `gorm:"column:id"`
		SourceLocale      string         `gorm:"column:source_locale"`
		ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
	}
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return series.ContentDocumentRoot{}, series.ErrSeriesNotFound
		}
		return series.ContentDocumentRoot{}, err
	}
	return series.ContentDocumentRoot{SourceLocale: root.SourceLocale, ContentDocumentID: nullString(root.ContentDocumentID)}, nil
}

func (s translationStore) ContentDocument(ctx context.Context, documentID string, lock bool) (series.ContentDocumentRow, error) {
	query := s.tx.WithContext(ctx).Table("content_document").
		Select("id, profile, revision").Where("id = ?", documentID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var document struct {
		ID       sql.NullString `gorm:"column:id"`
		Profile  string         `gorm:"column:profile"`
		Revision sql.NullString `gorm:"column:revision"`
	}
	if err := query.Take(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return series.ContentDocumentRow{}, series.ErrContentDocumentNotFound
		}
		return series.ContentDocumentRow{}, err
	}
	return series.ContentDocumentRow{Profile: document.Profile, Revision: nullString(document.Revision)}, nil
}

func (s translationStore) AdvanceContentDocument(ctx context.Context, seriesID, documentID, expected, next string, now time.Time) error {
	updated := s.tx.WithContext(ctx).Exec(
		`UPDATE content_document
		 SET revision = ?, updated_at = ?
		 WHERE id = ? AND revision = ?
		   AND EXISTS (
			 SELECT 1 FROM series
			 WHERE id = ? AND content_document_id = ?
		   )`,
		next, now.UTC(), documentID, expected, seriesID, documentID,
	)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return series.ErrContentDocumentRevisionChanged
	}
	return nil
}

func (s translationStore) AppendRequestAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendRequest(ctx, s.tx, appender, action, build)
}

func (s translationStore) AppendMemberAudit(ctx context.Context, appender domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendMember(ctx, s.tx, appender, memberID, action, build)
}

func (s store) Series(ctx context.Context, seriesID string) (series.Series, error) {
	var row model.Series
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", seriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return series.Series{}, series.ErrSeriesNotFound
		}
		return series.Series{}, err
	}
	return seriesFromRow(row), nil
}

func (s store) SeriesPage(ctx context.Context, seriesIDs []string, limit, offset int32) ([]series.Series, int64, error) {
	stored := s.tx.WithContext(ctx).Model(&model.Series{}).Where("id IN ?", seriesIDs)
	var total int64
	if err := stored.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	stored = stored.Order("created_at DESC, id ASC")
	var rows []model.Series
	if err := stored.Limit(int(limit)).Offset(int(offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return seriesFromRows(rows), total, nil
}

func (s store) AdminSeriesPage(ctx context.Context, query series.ListSeriesAdminQuery, limit, offset int32) ([]series.Series, int64, error) {
	stored, err := seriesFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.Series{}), query.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := stored.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if stored, err = seriesSortConfig.ApplySort(stored, query.Sorts); err != nil {
		return nil, 0, err
	}
	stored = stored.Order("id ASC")
	var rows []model.Series
	if err := stored.Limit(int(limit)).Offset(int(offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return seriesFromRows(rows), total, nil
}

func (s store) SeriesByCreation(ctx context.Context, seriesIDs []string) ([]series.Series, error) {
	var rows []model.Series
	if err := s.tx.WithContext(ctx).Model(&model.Series{}).
		Where("id IN ?", seriesIDs).
		Order("created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return seriesFromRows(rows), nil
}

func (s store) SlugTaken(ctx context.Context, slug, excludeID string) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&model.Series{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s store) InsertSeries(ctx context.Context, created series.Series) (series.Series, error) {
	row := seriesRow(created)
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return series.Series{}, slugError(err)
	}
	return seriesFromRow(row), nil
}

func (s store) UpdateSeriesRoot(ctx context.Context, seriesID string, slug *string, status *series.Status, now time.Time) error {
	updates := structured.Fields{}
	if slug != nil {
		updates["slug"] = *slug
	}
	if status != nil {
		updates["status"] = string(*status)
	}
	updates["updated_at"] = now
	return slugError(s.tx.WithContext(ctx).Model(&model.Series{ID: seriesID}).Updates(updates).Error)
}

func (s store) SetFeaturedImage(ctx context.Context, seriesID string, fileID *string, now time.Time) error {
	var value any
	if fileID != nil {
		value = *fileID
	}
	return s.tx.WithContext(ctx).Model(&model.Series{ID: seriesID}).Updates(structured.Fields{
		"featured_image_file_id": value, "updated_at": now,
	}).Error
}

func (s store) DeleteSeries(ctx context.Context, seriesID string) error {
	return s.tx.WithContext(ctx).Delete(&model.Series{ID: seriesID}).Error
}

func (s store) CreateContentDocument(ctx context.Context, documentID, revision string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		documentID, contentDocumentProfile, revision, now.UTC(), now.UTC(),
	).Error
}

func (s store) DeleteContentDocument(ctx context.Context, documentID string) error {
	result := s.tx.WithContext(ctx).Exec("DELETE FROM content_document WHERE id = ?", documentID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return series.ErrContentDocumentNotFound
	}
	return nil
}

func (s store) ManagerAttributions(ctx context.Context, seriesID string) ([]series.ManagerAttribution, error) {
	var rows []struct {
		MemberID  string
		CreatedAt time.Time
	}
	if err := s.tx.WithContext(ctx).Table("series_manager").Select("member_id", "created_at").
		Where("series_id = ?", seriesID).Order("created_at ASC, member_id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	attributions := make([]series.ManagerAttribution, len(rows))
	for i, row := range rows {
		attributions[i] = series.ManagerAttribution{MemberID: row.MemberID, CreatedAt: row.CreatedAt}
	}
	return attributions, nil
}

func (s store) ManagerAttributed(ctx context.Context, seriesID, memberID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).Table("series_manager").
		Where("series_id = ? AND member_id = ?", seriesID, memberID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

// UpsertManagerAttribution records a durable product attribution used for
// participant display and history.
func (s store) UpsertManagerAttribution(ctx context.Context, seriesID, memberID string, updatedAt time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
INSERT INTO series_manager (series_id, member_id, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (series_id, member_id)
DO UPDATE SET updated_at = EXCLUDED.updated_at
`, seriesID, memberID, updatedAt, updatedAt).Error
}

func (s store) DeleteManagerAttribution(ctx context.Context, seriesID, memberID string) error {
	return s.tx.WithContext(ctx).
		Exec("DELETE FROM series_manager WHERE series_id = ? AND member_id = ?", seriesID, memberID).
		Error
}

func (s store) LinkedMember(ctx context.Context, memberID string) error {
	_, err := authorizationtarget.RequireLinkedMember(ctx, s.tx, memberID, false)
	return err
}

func (s store) LockManagerTarget(ctx context.Context, memberID string) (string, error) {
	target, err := authorizationtarget.RequireLocked(ctx, s.tx, memberID)
	return target.IdentityID, err
}

func (s store) LockLinkedManagerTarget(ctx context.Context, memberID string) (string, error) {
	target, err := authorizationtarget.RequireLockedLinked(ctx, s.tx, memberID)
	return target.IdentityID, err
}

func (s store) EnsureRouteAvailable(ctx context.Context, slug string, lock bool) error {
	if lock {
		return routeregistry.EnsureResourceRouteAvailableInTx(ctx, s.tx, "series", "series", slug)
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.tx, "series", "series", slug)
}

func (s store) RouteAvailable(ctx context.Context, slug string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, s.tx, "series", slug)
}

func (s store) Posts() series.Posts {
	return posts{tx: s.tx, with: s.with}
}

func (s store) RequireAttachableFile(ctx context.Context, fileID string) error {
	err := s.with.Media.RequireAttachableFile(ctx, s.tx, fileID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return series.ErrFileNotFound
	}
	return err
}

func (s store) BindFeaturedImage(ctx context.Context, seriesID, fileID string) (*series.Asset, error) {
	return s.with.Media.BindFeaturedImage(ctx, s.tx, fileID, seriesID)
}

func (s store) ReleaseFeaturedImage(ctx context.Context, seriesID string) error {
	return s.with.Media.ReleaseFeaturedImage(ctx, s.tx, seriesID)
}

func (s store) CancelAndReleaseOG(ctx context.Context, seriesID string) error {
	return s.with.Media.CancelAndReleaseOG(ctx, s.tx, seriesID)
}

func (s store) RequestOG(ctx context.Context, seriesID, locale string, allLocales bool, reason string) (*string, error) {
	return s.with.Media.RequestCurrent(ctx, s.tx, seriesID, locale, allLocales, reason)
}

func (s store) RenameMenuTargets(ctx context.Context, seriesID, oldSlug, newSlug string) error {
	return s.with.MenuTargets.UpdateSlug(ctx, s.tx, "series", seriesID, oldSlug, newSlug)
}

func (s store) RemoveMenuTargets(ctx context.Context, seriesID, slug string) error {
	return s.with.MenuTargets.Remove(ctx, s.tx, "series", seriesID, slug)
}

func (s store) RequireCurrentContributors(ctx context.Context, seriesID string, memberIDs []string) error {
	return s.with.Checkpoints.RequireCurrentContributors(
		ctx, s.tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST_SERIES, seriesID, memberIDs,
	)
}

func (s store) PublishContentSignal(ctx context.Context, signal series.ContentSignal) error {
	return contentsignal.PublishInteractive(
		ctx, s.tx, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST_SERIES, signal.SeriesID, signal.Locale,
		signal.DocumentRevision, signal.TargetRevision, signal.Changed, signal.Deleted, signal.MemberID,
	)
}

// posts binds the Post port to a Series transaction.
type posts struct {
	tx   *gorm.DB
	with Collaborators
}

func postError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return series.ErrPostNotFound
	}
	return err
}

func (p posts) SeriesPosts(ctx context.Context, seriesID string) ([]series.SeriesPost, error) {
	return p.with.Posts.ListSeriesPosts(ctx, p.tx, seriesID)
}

func (p posts) SeriesPostIDs(ctx context.Context, seriesID string) ([]string, error) {
	return p.with.Posts.SeriesPostIDs(ctx, p.tx, seriesID)
}

func (p posts) SeriesOf(ctx context.Context, postID string) (*string, error) {
	seriesID, err := p.with.Posts.ReadSeriesRelation(ctx, p.tx, postID)
	return seriesID, postError(err)
}

func (p posts) LockSeriesOf(ctx context.Context, postID string) (*string, error) {
	seriesID, err := p.with.Posts.LockSeriesRelation(ctx, p.tx, postID)
	return seriesID, postError(err)
}

func (p posts) LockPosts(ctx context.Context, postIDs []string) (int, error) {
	return p.with.Posts.LockSeriesPosts(ctx, p.tx, postIDs)
}

func (p posts) NextOrder(ctx context.Context, seriesID string) (int, error) {
	return p.with.Posts.NextSeriesOrder(ctx, p.tx, seriesID)
}

func (p posts) Assign(ctx context.Context, postID, seriesID string, order int, now time.Time) error {
	return postError(p.with.Posts.AssignSeries(ctx, p.tx, postID, seriesID, order, now))
}

func (p posts) Clear(ctx context.Context, postID, seriesID string, now time.Time) error {
	return p.with.Posts.ClearSeries(ctx, p.tx, postID, seriesID, now)
}

func (p posts) ClearAll(ctx context.Context, seriesID string, now time.Time) error {
	return p.with.Posts.ClearAllSeries(ctx, p.tx, seriesID, now)
}

func (p posts) Order(ctx context.Context, seriesID string, postIDs []string, now time.Time) error {
	return p.with.Posts.OrderSeries(ctx, p.tx, seriesID, postIDs, now)
}

func (p posts) RequireLockedEdit(ctx context.Context, postID string) error {
	return p.with.PostAccess.RequireLockedEdit(ctx, p.tx, p.with.SpiceDB, postID)
}
