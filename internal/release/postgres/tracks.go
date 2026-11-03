package releasepostgres

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func trackFromRow(row model.Track) release.Track {
	return release.Track{
		ID: row.ID, ReleaseID: row.ReleaseID, TrackNumber: row.TrackNumber, Title: row.Title,
		DurationSeconds: row.DurationSeconds, ProcessingStatus: row.ProcessingStatus, Lyrics: row.Lyrics,
		Metadata: map[string]any(row.Metadata), CreatedAt: row.CreatedAt, AudioOriginalFileID: row.AudioOriginalFileID,
		DownloadAccess: row.DownloadAccess,
	}
}

func trackRow(track release.Track) model.Track {
	return model.Track{
		ID: track.ID, ReleaseID: track.ReleaseID, TrackNumber: track.TrackNumber, Title: track.Title,
		DurationSeconds: track.DurationSeconds, ProcessingStatus: track.ProcessingStatus, Lyrics: track.Lyrics,
		Metadata: model.TrackMetadata(track.Metadata), CreatedAt: track.CreatedAt, AudioOriginalFileID: track.AudioOriginalFileID,
		DownloadAccess: track.DownloadAccess,
	}
}

func (s *store) Tracks(ctx context.Context, releaseID string) ([]release.Track, error) {
	var rows []model.Track
	if err := s.tx.WithContext(ctx).
		Where("release_id = ?", releaseID).
		Order("track_number ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	tracks := make([]release.Track, len(rows))
	for i, row := range rows {
		tracks[i] = trackFromRow(row)
	}
	return tracks, nil
}

func (s *store) FindTrack(ctx context.Context, trackID string) (release.Track, error) {
	var row model.Track
	if err := s.tx.WithContext(ctx).Where("id = ?", trackID).First(&row).Error; err != nil {
		return release.Track{}, notFoundAs(err, release.ErrTrackNotFound)
	}
	return trackFromRow(row), nil
}

func (s *store) LockTrack(ctx context.Context, trackID string) (release.Track, error) {
	var row model.Track
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = ?", trackID).Error; err != nil {
		return release.Track{}, notFoundAs(err, release.ErrTrackNotFound)
	}
	return trackFromRow(row), nil
}

func (s *store) LockTrackID(ctx context.Context, trackID string) error {
	var row model.Track
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").Where("id = ?", trackID).Take(&row).Error
	return notFoundAs(err, release.ErrTrackNotFound)
}

func (s *store) LockTrackIDs(ctx context.Context, releaseID string) ([]string, error) {
	var trackIDs []string
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Model(&model.Track{}).
		Where("release_id = ?", releaseID).
		Pluck("id", &trackIDs).Error; err != nil {
		return nil, err
	}
	return trackIDs, nil
}

func (s *store) LockTrackDownloadCriteriaSet(ctx context.Context, trackID string) error {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	return s.tx.WithContext(ctx).
		Table("member_criteria AS criteria").
		Select("criteria.id").
		Joins("JOIN track_download_member_criteria AS association ON association.member_criteria_id = criteria.id").
		Where("association.track_id = ?", trackID).
		Order("criteria.id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "criteria"}}).
		Find(&rows).Error
}

func (s *store) NextTrackNumber(ctx context.Context, releaseID string) (int, error) {
	var maxTrackNumber int
	if err := s.tx.WithContext(ctx).Raw(`
				SELECT COALESCE(MAX(track_number), 0) FROM track
				WHERE release_id = $1`, releaseID).Scan(&maxTrackNumber).Error; err != nil {
		return 0, err
	}
	return maxTrackNumber + 1, nil
}

func (s *store) InsertTrack(ctx context.Context, track *release.Track) error {
	row := trackRow(*track)
	if err := s.tx.WithContext(ctx).Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return err
	}
	*track = trackFromRow(row)
	return nil
}

func (s *store) SaveTrack(ctx context.Context, track release.Track) error {
	row := trackRow(track)
	return s.tx.WithContext(ctx).Save(&row).Error
}

func (s *store) DisableTrackDownload(ctx context.Context, trackID string) error {
	tx := s.tx.WithContext(ctx)
	if err := tx.Model(&model.Track{}).Where("id = ?", trackID).
		Update("download_access", release.DownloadAccessDisabled).Error; err != nil {
		return err
	}
	return tx.Where("track_id = ?", trackID).Delete(&model.TrackDownloadMemberCriteria{}).Error
}

func (s *store) AttachTrackAudio(ctx context.Context, trackID string, audio release.TrackAudio) error {
	updates := structured.Fields{
		"audio_original_file_id": audio.FileID, "processing_status": string(release.TrackProcessingProcessing),
		"download_access": release.DownloadAccessDisabled,
	}
	if audio.Completed {
		updates["processing_status"] = string(release.TrackProcessingCompleted)
		updates["duration_seconds"] = audio.DurationSeconds
	}
	tx := s.tx.WithContext(ctx)
	if err := tx.Model(&model.Track{ID: trackID}).Updates(updates).Error; err != nil {
		return err
	}
	return tx.Where("track_id = ?", trackID).Delete(&model.TrackDownloadMemberCriteria{}).Error
}

func (s *store) DeleteTrack(ctx context.Context, trackID string) error {
	result := s.tx.WithContext(ctx).Where("id = ?", trackID).Delete(&model.Track{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return sentinelError{error: gorm.ErrRecordNotFound, sentinel: release.ErrTrackNotFound}
	}
	return nil
}

func (s *store) CountTracksOf(ctx context.Context, releaseID string, trackIDs []string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).
		Model(&model.Track{}).
		Where("id IN ? AND release_id = ?", trackIDs, releaseID).
		Count(&count).Error
	return count, err
}

func (s *store) CountReleaseTracks(ctx context.Context, releaseID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).
		Model(&model.Track{}).
		Where("release_id = ?", releaseID).
		Count(&count).Error
	return count, err
}

func (s *store) TrackOrder(ctx context.Context, releaseID string) ([]string, error) {
	var ids []string
	if err := s.tx.WithContext(ctx).Model(&model.Track{}).Where("release_id = ?", releaseID).Order("track_number ASC").Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// RenumberTracks renumbers the Tracks in two phases, negative and then
// positive, so the unique track number never collides midway.
func (s *store) RenumberTracks(ctx context.Context, releaseID string, trackIDs []string) error {
	tx := s.tx.WithContext(ctx)
	for i, trackID := range trackIDs {
		if err := tx.Exec("UPDATE track SET track_number = ? WHERE id = ? AND release_id = ?",
			-(i + 1), trackID, releaseID).Error; err != nil {
			return err
		}
	}
	return tx.Exec("UPDATE track SET track_number = -track_number WHERE track_number < 0 AND release_id = ?",
		releaseID).Error
}

func (s *store) HasTrackUploadSessions(ctx context.Context, trackIDs []string) (bool, error) {
	if len(trackIDs) == 0 {
		return false, nil
	}
	var count int64
	if err := s.tx.WithContext(ctx).Model(&model.UploadSession{}).
		Where("upload_type = ? AND entity_id IN ?", managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackIDs).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) RequireNoTrackUploadSessions(ctx context.Context, trackID string) error {
	return s.ports.TrackFiles.RequireNoTrackUploadSessionsWithDB(ctx, s.tx, trackID)
}

func trackCreditFromRow(row model.TrackCredit) release.TrackCredit {
	return release.TrackCredit{
		ID: row.ID, TrackID: row.TrackID, ArtistID: row.ArtistID, MemberID: row.MemberID,
		CreditedName: row.CreditedName, CreditRole: row.CreditRole, SortOrder: row.SortOrder, CreatedAt: row.CreatedAt,
	}
}

func trackCreditRow(credit release.TrackCredit) model.TrackCredit {
	return model.TrackCredit{
		ID: credit.ID, TrackID: credit.TrackID, ArtistID: credit.ArtistID, MemberID: credit.MemberID,
		CreditedName: credit.CreditedName, CreditRole: credit.CreditRole, SortOrder: credit.SortOrder, CreatedAt: credit.CreatedAt,
	}
}

func (s *store) TrackCredits(ctx context.Context, trackID string) ([]release.TrackCredit, error) {
	var rows []model.TrackCredit
	if err := s.tx.WithContext(ctx).Where("track_id = ?", trackID).Order("sort_order ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]release.TrackCredit, len(rows))
	for i, row := range rows {
		credits[i] = trackCreditFromRow(row)
	}
	return credits, nil
}

func (s *store) TrackCreditRows(ctx context.Context, trackID string) ([]release.CreditRow, error) {
	var rows []model.TrackCreditWithInfo
	query := `
		SELECT
			tc.id,
			CASE
				WHEN tc.artist_id IS NOT NULL THEN 'artist'
				WHEN tc.member_id IS NOT NULL THEN 'member'
				ELSE 'text'
			END as credit_type,
			tc.artist_id,
			tc.member_id,
			tc.credited_name,
			tc.credit_role,
			tc.sort_order
		FROM track_credit tc
		WHERE tc.track_id = ?
		ORDER BY tc.sort_order ASC, tc.created_at ASC
	`
	if err := s.tx.WithContext(ctx).Raw(query, trackID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]release.CreditRow, len(rows))
	for i, row := range rows {
		credits[i] = release.CreditRow{
			ID: row.ID, CreditType: row.CreditType, ArtistID: row.ArtistID, MemberID: row.MemberID,
			CreditedName: row.CreditedName, CreditRole: row.CreditRole, SortOrder: row.SortOrder,
		}
	}
	return credits, nil
}

// ReplaceTrackCredits stores next in place of current: removed credits
// first, then changed and added credits in their new order.
func (s *store) ReplaceTrackCredits(ctx context.Context, current, next []release.TrackCredit) ([]release.TrackCreditChange, error) {
	tx := s.tx.WithContext(ctx)
	var changes []release.TrackCreditChange
	currentByID := make(map[string]release.TrackCredit, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
	}
	nextByID := make(map[string]release.TrackCredit, len(next))
	for _, row := range next {
		if row.ID != "" {
			nextByID[row.ID] = row
		}
	}
	for _, row := range current {
		if _, keep := nextByID[row.ID]; keep {
			continue
		}
		if err := tx.Delete(&model.TrackCredit{}, "id = ?", row.ID).Error; err != nil {
			return nil, err
		}
		changes = append(changes, release.TrackCreditChange{CreditID: row.ID, Kind: release.ChangeDeleted})
	}
	for _, credit := range next {
		if previous, exists := currentByID[credit.ID]; exists && credit.ID != "" {
			if sameTrackCredit(previous, credit) {
				continue
			}
			updates := creditUpdateMap(
				creditValues{previous.ArtistID, previous.MemberID, previous.CreditedName, previous.CreditRole, previous.SortOrder},
				creditValues{credit.ArtistID, credit.MemberID, credit.CreditedName, credit.CreditRole, credit.SortOrder},
			)
			if err := tx.Model(&model.TrackCredit{}).Where("id = ?", credit.ID).Updates(updates).Error; err != nil {
				return nil, err
			}
			changes = append(changes, release.TrackCreditChange{CreditID: credit.ID, Kind: release.ChangeUpdated})
			continue
		}
		row := trackCreditRow(credit)
		if err := tx.Clauses(clause.Returning{}).Create(&row).Error; err != nil {
			return nil, err
		}
		changes = append(changes, release.TrackCreditChange{CreditID: row.ID, Kind: release.ChangeCreated})
	}
	return changes, nil
}

func sameTrackCredit(left, right release.TrackCredit) bool {
	return sameOptionalString(left.CreditRole, right.CreditRole) && left.SortOrder == right.SortOrder && sameOptionalString(left.ArtistID, right.ArtistID) && sameOptionalString(left.MemberID, right.MemberID) && sameOptionalString(left.CreditedName, right.CreditedName)
}
