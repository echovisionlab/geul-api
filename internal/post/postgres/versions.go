package postpostgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/post"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func versionFromRow(row model.PostVersion) post.StoredVersion {
	return post.StoredVersion{
		ID: row.ID, PostID: row.PostID, Version: row.Version, ContentSnapshot: row.ContentSnapshot,
		ContributorMemberIDs: []string(row.ContributorMemberIDs), CreatedAt: row.CreatedAt,
	}
}

func (s *store) ListVersions(ctx context.Context, postID string, limit, offset int32) ([]post.StoredVersion, int64, error) {
	var total int64
	if err := s.tx.WithContext(ctx).Model(&model.PostVersion{}).Where("post_id = ?", postID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.PostVersion
	if err := s.tx.WithContext(ctx).Where("post_id = ?", postID).Order("version DESC").
		Limit(int(limit)).Offset(int(offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	versions := make([]post.StoredVersion, len(rows))
	for index, row := range rows {
		versions[index] = versionFromRow(row)
	}
	return versions, total, nil
}

func (s *store) FindVersion(ctx context.Context, versionID string) (post.StoredVersion, error) {
	var row model.PostVersion
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", versionID).Error; err != nil {
		return post.StoredVersion{}, notFound(err, post.ErrVersionNotFound)
	}
	return versionFromRow(row), nil
}

func (s *store) LockLatestVersion(ctx context.Context, postID string) (post.StoredVersion, bool, error) {
	var last model.PostVersion
	err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("post_id = ?", postID).
		Order("version DESC").
		First(&last).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return post.StoredVersion{}, false, nil
	}
	if err != nil {
		return post.StoredVersion{}, false, err
	}
	return versionFromRow(last), true, nil
}

func (s *store) InsertVersion(ctx context.Context, version *post.StoredVersion) error {
	row := model.PostVersion{
		PostID: version.PostID, Version: version.Version, ContentSnapshot: version.ContentSnapshot,
		ContributorMemberIDs: pq.StringArray(version.ContributorMemberIDs),
	}
	if err := s.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	*version = versionFromRow(row)
	return nil
}

func (s *store) UnavailableVersionAttachments(ctx context.Context, document *contentv1.LocalizedRichTextDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return s.ports.VersionRestore.LoadUnavailableVersionAttachmentKinds(ctx, s.tx, document)
}
