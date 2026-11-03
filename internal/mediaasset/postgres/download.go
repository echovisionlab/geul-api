package mediaassetpostgres

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CriteriaConfigLoader interface {
	LoadCriteriaConfigs(context.Context, *gorm.DB, []*model.MemberCriteria) error
}
type downloadStore struct {
	db     *gorm.DB
	loader CriteriaConfigLoader
}

func NewDownloadStore(tx *gorm.DB, loader CriteriaConfigLoader) mediaasset.DownloadStore {
	if tx == nil {
		panic("media asset download store: database is required")
	}
	return &downloadStore{db: tx, loader: loader}
}

type downloadSourceRow struct {
	PolicyKey     string
	PolicyKind    mediaasset.FileDownloadPolicyKind
	BlockID       string                        `gorm:"column:block_id"`
	ReferencePath string                        `gorm:"column:reference_path"`
	TrackID       string                        `gorm:"column:track_id"`
	FileID        string                        `gorm:"column:id"`
	Extension     string                        `gorm:"column:extension"`
	MimeType      string                        `gorm:"column:mime_type"`
	FileSize      int64                         `gorm:"column:file_size"`
	FileName      *string                       `gorm:"column:file_name"`
	Access        mediaasset.FileDownloadAccess `gorm:"column:download_access"`
}

func downloadSource(row downloadSourceRow) mediaasset.FileDownloadSource {
	return mediaasset.FileDownloadSource{PolicyKey: row.PolicyKey, PolicyKind: row.PolicyKind, BlockID: row.BlockID, ReferencePath: row.ReferencePath, TrackID: row.TrackID, FileID: row.FileID, Extension: row.Extension, MimeType: row.MimeType, FileSize: row.FileSize, FileName: row.FileName, Access: row.Access}
}
func (s *downloadStore) ContentSources(ctx context.Context, blockIDs []string) ([]mediaasset.FileDownloadSource, error) {
	var rows []downloadSourceRow
	err := s.db.WithContext(ctx).Raw(`
		SELECT attachment.block_id, attachment.reference_path,
		       file.id, file.extension, file.mime_type, file.file_size, file.file_name,
		       attachment.download_access
		FROM content_block_attachment AS attachment
		JOIN content_block AS block ON block.id = attachment.block_id
		JOIN file ON file.id = attachment.file_id
		WHERE attachment.block_id IN ?
		  AND block.kind = 'file'
		  AND attachment.reference_path = 'file'
		  AND attachment.selector_kind = 'active'
		  AND file.delete_requested_at IS NULL
	`, blockIDs).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]mediaasset.FileDownloadSource, len(rows))
	for i, row := range rows {
		result[i] = downloadSource(row)
	}
	return result, nil
}
func (s *downloadStore) TrackSources(ctx context.Context, trackIDs []string) ([]mediaasset.FileDownloadSource, error) {
	var rows []downloadSourceRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT track.id AS track_id, file.id, file.extension, file.mime_type,
		       file.file_size, file.file_name, track.download_access
		FROM track
		JOIN file ON file.id = track.audio_original_file_id
		WHERE track.id IN ? AND file.delete_requested_at IS NULL
	`, trackIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]mediaasset.FileDownloadSource, len(rows))
	for i, row := range rows {
		result[i] = downloadSource(row)
	}
	return result, nil
}

func (s *downloadStore) Associations(
	ctx context.Context,
	sources map[string]mediaasset.FileDownloadSource,
	activeOnly bool,
) ([]mediaasset.DownloadAssociation, error) {
	contentSources := make(map[string]mediaasset.FileDownloadSource)
	trackIDs := make([]string, 0)
	blockIDs := make([]string, 0)
	for key, source := range sources {
		switch source.PolicyKind {
		case mediaasset.FileDownloadPolicyContentBlock:
			contentSources[key] = source
			blockIDs = append(blockIDs, source.BlockID)
		case mediaasset.FileDownloadPolicyTrack:
			trackIDs = append(trackIDs, source.TrackID)
		}
	}
	associations := make([]mediaasset.DownloadAssociation, 0)
	if blockIDs = sliceutil.SortedUniqueTrimmed(blockIDs); len(blockIDs) > 0 {
		var rows []struct {
			BlockID          string `gorm:"column:block_id"`
			ReferencePath    string `gorm:"column:reference_path"`
			MemberCriteriaID string `gorm:"column:member_criteria_id"`
		}
		query := s.db.WithContext(ctx).
			Table("content_block_attachment_download_member_criteria AS association").
			Select("association.block_id, association.reference_path, association.member_criteria_id").
			Where("association.block_id IN ?", blockIDs)
		if activeOnly {
			query = query.Joins("JOIN member_criteria AS criteria ON criteria.id = association.member_criteria_id").
				Where("criteria.archived_at IS NULL")
		}
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := mediaasset.ContentBlockDownloadPolicyKey(row.BlockID, row.ReferencePath)
			if _, ok := contentSources[key]; ok {
				associations = append(associations, mediaasset.DownloadAssociation{PolicyKey: key, MemberCriteriaID: row.MemberCriteriaID})
			}
		}
	}
	if trackIDs = sliceutil.SortedUniqueTrimmed(trackIDs); len(trackIDs) > 0 {
		var rows []model.TrackDownloadMemberCriteria
		query := s.db.WithContext(ctx).
			Table("track_download_member_criteria AS association").
			Select("association.track_id, association.member_criteria_id").
			Where("association.track_id IN ?", trackIDs)
		if activeOnly {
			query = query.Joins("JOIN member_criteria AS criteria ON criteria.id = association.member_criteria_id").
				Where("criteria.archived_at IS NULL")
		}
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			associations = append(associations, mediaasset.DownloadAssociation{PolicyKey: row.TrackID, MemberCriteriaID: row.MemberCriteriaID})
		}
	}
	return associations, nil
}

func (s *downloadStore) CriteriaSet(ctx context.Context, ids []string, activeOnly bool) ([]mediaasset.DownloadCriteria, error) {
	var rows []model.MemberCriteria
	q := s.db.WithContext(ctx).Where("id IN ?", ids)
	if activeOnly {
		q = q.Where("archived_at IS NULL")
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	if s.loader == nil {
		return nil, domainerr.DependencyUnavailable("Access criteria configuration")
	}
	pointers := make([]*model.MemberCriteria, 0, len(rows))
	for i := range rows {
		pointers = append(pointers, &rows[i])
	}
	if err := s.loader.LoadCriteriaConfigs(ctx, s.db, pointers); err != nil {
		return nil, err
	}
	result := make([]mediaasset.DownloadCriteria, len(rows))
	for i, row := range rows {
		result[i] = mediaasset.DownloadCriteria{ID: row.ID, Kind: mediaasset.DownloadKind(row.Kind), ArchivedAt: row.ArchivedAt, UpdatedAt: row.UpdatedAt, Config: mediaasset.DownloadCriteriaConfig{MemberTagIDs: row.Config.MemberTagIDs, AccountRoles: row.Config.AccountRoles, CreatedAfter: row.Config.CreatedAfter, CreatedBefore: row.Config.CreatedBefore, ExcludeMemberIDs: row.Config.ExcludeMemberIDs}}
	}
	return result, nil
}

func (s *downloadStore) MemberFacts(
	ctx context.Context,
	memberID string,
	identityID string,
) (mediaasset.DownloadMemberFacts, bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return mediaasset.DownloadMemberFacts{}, false, nil //nolint:nilerr // a malformed member ID matches no row; report not found (deny), not a failure
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return mediaasset.DownloadMemberFacts{}, false, nil //nolint:nilerr // a malformed identity ID matches no row; report not found (deny), not a failure
	}

	query := s.db.WithContext(ctx).
		Table("member AS member").
		Joins(identityread.JoinLinked("member", "identity")).
		Where("member.id = ?", memberID).
		Where("member.account_identity_id = ?", identityID).
		Where("identity.id = ?", identityID).
		Where("member.deleted_at IS NULL").
		Where("member.onboarded = ?", true).
		Where("identity.state = ?", auth.KratosStateActive)
	if s.db.Dialector.Name() == "sqlite" {
		query = query.Select(`
			member.id AS member_id,
			identity.id AS identity_id,
			member.onboarded AS onboarded,
			member.created_at AS member_created_at`)
		query = query.Where(
			"(json_extract(identity.metadata_admin, '$.banned') IS NULL OR json_extract(identity.metadata_admin, '$.banned') = 0)",
		)
	} else {
		query = query.Select(`
			member.id AS member_id,
			identity.id::text AS identity_id,
			member.onboarded AS onboarded,
			member.created_at AS member_created_at`)
		query = query.Where(
			"(identity.metadata_admin->>'banned' IS NULL OR identity.metadata_admin->>'banned' = 'false')",
		)
	}
	var facts struct {
		MemberID   string    `gorm:"column:member_id"`
		IdentityID string    `gorm:"column:identity_id"`
		Onboarded  bool      `gorm:"column:onboarded"`
		CreatedAt  time.Time `gorm:"column:member_created_at"`
	}
	if err := query.Take(&facts).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return mediaasset.DownloadMemberFacts{}, false, nil
		}
		return mediaasset.DownloadMemberFacts{}, false, err
	}
	return mediaasset.DownloadMemberFacts{MemberID: facts.MemberID, IdentityID: facts.IdentityID, Onboarded: facts.Onboarded, CreatedAt: facts.CreatedAt}, true, nil
}

func (s *downloadStore) TagFacts(ctx context.Context, memberID string, referencedTagIDs []string) ([]mediaasset.DownloadTagFact, error) {
	var rows []struct {
		TagID          string  `gorm:"column:tag_id"`
		MappedMemberID *string `gorm:"column:mapped_member_id"`
	}
	if err := s.db.WithContext(ctx).
		Table("user_tag AS tag").
		Select(`
			tag.id AS tag_id,
			mapping.member_id AS mapped_member_id`).
		Joins(`LEFT JOIN user_tag_mapping AS mapping
			ON mapping.tag_id = tag.id AND mapping.member_id = ?`, memberID).
		Where("tag.id IN ?", referencedTagIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]mediaasset.DownloadTagFact, len(rows))
	for i, row := range rows {
		result[i] = mediaasset.DownloadTagFact{TagID: row.TagID, MappedMemberID: row.MappedMemberID}
	}
	return result, nil
}
func (s *downloadStore) ViewerTagIDs(ctx context.Context, id string) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Table("user_tag_mapping AS mapping").Joins("JOIN user_tag AS tag ON tag.id = mapping.tag_id").Where("mapping.member_id = ?", id).Pluck("mapping.tag_id", &ids).Error
	return ids, err
}
func (s *downloadStore) LockCriteriaSet(ctx context.Context, criteriaIDs []string) error {
	var locked []struct {
		ID string `gorm:"column:id"`
	}
	return s.db.WithContext(ctx).
		Table("member_criteria").
		Select("id").
		Where("id IN ?", criteriaIDs).
		Order("id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Find(&locked).Error
}

// LockFileDownloadViewerFacts locks the database-owned authenticated viewer
// witness after Criteria locks and before the exact relation lock. The boolean
// reports whether the exact Member and Identity pair remains active.
func (s *downloadStore) LockViewer(ctx context.Context, user *auth.UserInfo, restricted bool) (bool, error) {
	var active bool
	if s.db.Dialector.Name() == "sqlite" {
		_, found, err := s.MemberFacts(
			ctx, user.MemberID.String(), user.IdentityID.String(),
		)
		if err != nil {
			return false, err
		}
		active = found
	} else {
		var err error
		active, err = identitystate.LockActivePrincipal(ctx, s.db, user)
		if err != nil {
			return false, err
		}
	}
	if !active || !restricted {
		return active, nil
	}
	var rows []struct {
		MemberID string `gorm:"column:member_id"`
	}
	err := s.db.WithContext(ctx).
		Table("user_tag_mapping AS mapping").
		Select("mapping.member_id").
		Joins("JOIN user_tag AS tag ON tag.id = mapping.tag_id").
		Where("mapping.member_id = ?", user.MemberID.String()).
		Order("mapping.tag_id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Find(&rows).Error
	return err == nil, err
}

func ContentDownloadShareLinkWitnessFromModel(link *model.ShareLink) *mediaasset.ContentDownloadShareLinkWitness {
	if link == nil {
		return nil
	}
	return &mediaasset.ContentDownloadShareLinkWitness{
		ID: link.ID, EntityType: link.EntityType, EntityID: link.EntityID,
		ExpiresAt: link.ExpiresAt,
	}
}
