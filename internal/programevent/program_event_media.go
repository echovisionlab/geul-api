package programevent

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AddProgramEventMediaCommand adds FileID to the event under Role, or
// updates the alt text, caption and primary flag of the existing relation;
// omitted alt and caption values clear them.
type AddProgramEventMediaCommand struct {
	EventID     string
	FileID      string
	Role        string
	Alt         *string
	Caption     *string
	MakePrimary bool
}

// MediaChange is the outcome of a media relation edit.
type MediaChange struct {
	EventID string
	Changed bool
	// Media is the added or updated relation.
	Media     *managev1.ProgramEventMedia
	UpdatedAt time.Time
}

// AddProgramEventMedia adds or updates one media relation of the event.
func (s *Service) AddProgramEventMedia(ctx context.Context, command AddProgramEventMediaCommand) (MediaChange, error) {
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	var audit *mutation.Audit
	return mutation.Run(ctx, s.mutations, mutation.Spec[MediaChange]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (MediaChange, error) {
			change, record, err := s.addMedia(ctx, tx, command)
			audit = record
			return change, err
		},
		Audit: func(MediaChange) mutation.Audit {
			if audit == nil {
				return mutation.Unchanged()
			}
			return *audit
		},
	})
}

func (s *Service) addMedia(ctx context.Context, tx *gorm.DB, command AddProgramEventMediaCommand) (MediaChange, *mutation.Audit, error) {
	role, err := normalizeProgramEventMediaRole(command.Role)
	if err != nil {
		return MediaChange{}, nil, err
	}
	var before model.ProgramEventMedia
	beforeResult := tx.Where("event_id = ? AND role = ? AND file_id = ?", command.EventID, role, command.FileID).First(&before)
	if beforeResult.Error != nil && !errors.Is(beforeResult.Error, gorm.ErrRecordNotFound) {
		return MediaChange{}, nil, beforeResult.Error
	}
	created := errors.Is(beforeResult.Error, gorm.ErrRecordNotFound)
	primaryBefore, err := loadProgramEventPrimaryMediaFileID(ctx, tx, command.EventID, role)
	if err != nil {
		return MediaChange{}, nil, err
	}
	if err := addProgramEventMedia(
		ctx, tx, s.media, command.EventID, command.FileID, command.Role, command.Alt, command.Caption, command.MakePrimary,
	); err != nil {
		return MediaChange{}, nil, err
	}
	var after model.ProgramEventMedia
	if err := tx.Where("event_id = ? AND role = ? AND file_id = ?", command.EventID, role, command.FileID).First(&after).Error; err != nil {
		return MediaChange{}, nil, err
	}
	primaryAfter, err := loadProgramEventPrimaryMediaFileID(ctx, tx, command.EventID, role)
	if err != nil {
		return MediaChange{}, nil, err
	}
	changed := created || !sameProgramEventMedia(before, after)
	updatedAt, err := programEventRelationMutationUpdatedAt(ctx, tx, command.EventID, changed)
	if err != nil {
		return MediaChange{}, nil, err
	}
	change := MediaChange{EventID: command.EventID, Changed: changed, Media: programEventMediaProto(after), UpdatedAt: updatedAt}
	switch {
	case role == "poster" && primaryBefore != primaryAfter:
		return change, posterAudit(command.EventID, primaryBefore, primaryAfter), nil
	case created:
		audit := childAudit(command.EventID, "media", after.ID, sharedtelemetry.AuditItemOperationCreated)
		return change, &audit, nil
	case changed:
		audit := childAudit(command.EventID, "media", after.ID, sharedtelemetry.AuditItemOperationUpdated)
		return change, &audit, nil
	}
	return change, nil, nil
}

// DeleteProgramEventMediaCommand names the media relation to delete.
type DeleteProgramEventMediaCommand struct {
	EventID string
	MediaID string
}

// DeleteProgramEventMedia deletes one media relation of the event and
// promotes the next relation of its role when it was primary.
func (s *Service) DeleteProgramEventMedia(ctx context.Context, command DeleteProgramEventMediaCommand) (MediaChange, error) {
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	var audit mutation.Audit
	return mutation.Run(ctx, s.mutations, mutation.Spec[MediaChange]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (MediaChange, error) {
			var before model.ProgramEventMedia
			if err := tx.First(&before, "event_id = ? AND id = ?", command.EventID, command.MediaID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return MediaChange{}, domainerr.ResourceNotFound("program event media", command.MediaID)
				}
				return MediaChange{}, err
			}
			primaryBefore, err := loadProgramEventPrimaryMediaFileID(ctx, tx, command.EventID, before.Role)
			if err != nil {
				return MediaChange{}, err
			}
			updatedAt, err := programEventRelationMutationUpdatedAt(ctx, tx, command.EventID, true)
			if err != nil {
				return MediaChange{}, err
			}
			if _, err := deleteProgramEventMedia(ctx, tx, s.media, command.EventID, command.MediaID); err != nil {
				return MediaChange{}, err
			}
			primaryAfter, err := loadProgramEventPrimaryMediaFileID(ctx, tx, command.EventID, before.Role)
			if err != nil {
				return MediaChange{}, err
			}
			audit = childAudit(command.EventID, "media", before.ID, sharedtelemetry.AuditItemOperationDeleted)
			if poster := posterAudit(command.EventID, primaryBefore, primaryAfter); before.Role == "poster" && poster != nil {
				audit = *poster
			}
			return MediaChange{EventID: command.EventID, Changed: true, UpdatedAt: updatedAt}, nil
		},
		Audit: func(MediaChange) mutation.Audit { return audit },
	})
}

// ReorderProgramEventMediaCommand orders every media relation of Role; the
// first becomes primary.
type ReorderProgramEventMediaCommand struct {
	EventID  string
	Role     string
	MediaIDs []string
}

// MediaOrder is the outcome of a media reorder.
type MediaOrder struct {
	Changed   bool
	Role      string
	UpdatedAt time.Time
}

// ReorderProgramEventMedia orders the media relations of one role.
func (s *Service) ReorderProgramEventMedia(ctx context.Context, command ReorderProgramEventMediaCommand) (MediaOrder, error) {
	lock, access := s.lockedEventEdit(command.EventID, policyv1.ProgramEvent.Edit)
	return mutation.Run(ctx, s.mutations, mutation.Spec[MediaOrder]{
		Lock:   lock,
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (MediaOrder, error) {
			role, err := normalizeProgramEventMediaRole(command.Role)
			if err != nil {
				return MediaOrder{}, err
			}
			before, err := loadProgramEventMediaIDs(ctx, tx, command.EventID, role)
			if err != nil {
				return MediaOrder{}, err
			}
			if err := reorderProgramEventMedia(ctx, tx, command.EventID, role, command.MediaIDs); err != nil {
				return MediaOrder{}, err
			}
			changed := !sameStringSlice(before, command.MediaIDs)
			updatedAt, err := programEventRelationMutationUpdatedAt(ctx, tx, command.EventID, changed)
			return MediaOrder{Changed: changed, Role: role, UpdatedAt: updatedAt}, err
		},
		Audit: func(order MediaOrder) mutation.Audit {
			if !order.Changed {
				return mutation.Unchanged()
			}
			return childOrderAudit(command.EventID, "media", command.MediaIDs)
		},
	})
}

func programEventRelationMutationUpdatedAt(ctx context.Context, db *gorm.DB, eventID string, changed bool) (time.Time, error) {
	if changed {
		now := dbtime.Now()
		if err := db.WithContext(ctx).Model(&model.ProgramEvent{}).Where("id = ?", eventID).Update("updated_at", now).Error; err != nil {
			return time.Time{}, err
		}
		return now, nil
	}
	var event struct {
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := db.WithContext(ctx).Table("program_event").Select("updated_at").Where("id = ?", eventID).Take(&event).Error; err != nil {
		return time.Time{}, err
	}
	return event.UpdatedAt, nil
}

func programEventMediaProto(media model.ProgramEventMedia) *managev1.ProgramEventMedia {
	return &managev1.ProgramEventMedia{
		Id:        media.ID,
		FileId:    media.FileID,
		Role:      media.Role,
		SortOrder: media.SortOrder,
		IsPrimary: media.IsPrimary,
		Alt:       media.Alt,
		Caption:   media.Caption,
		CreatedAt: timestamppb.New(media.CreatedAt),
		UpdatedAt: timestamppb.New(media.UpdatedAt),
	}
}

func addProgramEventMedia(
	ctx context.Context,
	db *gorm.DB,
	mediaAssets MediaAssets,
	eventID string,
	fileID string,
	role string,
	alt *string,
	caption *string,
	makePrimary bool,
) error {
	eventID = strings.TrimSpace(eventID)
	fileID = strings.TrimSpace(fileID)
	if eventID == "" {
		return domainerr.Required("event_id")
	}
	if fileID == "" {
		return domainerr.Required("file_id")
	}
	normalizedRole, err := normalizeProgramEventMediaRole(role)
	if err != nil {
		return err
	}
	if err := lockProgramEventForMutation(ctx, db, eventID); err != nil {
		return err
	}
	if err := mediaAssets.LockAttachableFilesForUpdate(ctx, db, []string{fileID}); err != nil {
		return err
	}

	var existing model.ProgramEventMedia
	result := db.WithContext(ctx).
		Where("event_id = ? AND role = ? AND file_id = ?", eventID, normalizedRole, fileID).
		First(&existing)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}

	shouldBePrimary := makePrimary
	if !shouldBePrimary {
		hasPrimary, err := hasProgramEventMediaPrimary(ctx, db, eventID, normalizedRole)
		if err != nil {
			return err
		}
		shouldBePrimary = !hasPrimary
	}
	if result.Error == nil && existing.IsPrimary == shouldBePrimary && sameNullableString(existing.Alt, nullableString(alt)) && sameNullableString(existing.Caption, nullableString(caption)) {
		return nil
	}
	if shouldBePrimary {
		if err := clearProgramEventMediaPrimary(ctx, db, eventID, normalizedRole); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	if result.Error == nil {
		updates := structured.Fields{
			"alt":        nullableString(alt),
			"caption":    nullableString(caption),
			"updated_at": now,
		}
		if shouldBePrimary {
			updates["is_primary"] = true
		}
		if err := db.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
		return bindProgramEventMediaPublicAsset(ctx, db, mediaAssets, existing)
	}

	var next struct {
		SortOrder int32 `gorm:"column:sort_order"`
	}
	if err := db.WithContext(ctx).
		Table("program_event_media").
		Select("COALESCE(MAX(sort_order), -1) + 1 AS sort_order").
		Where("event_id = ? AND role = ?", eventID, normalizedRole).
		Scan(&next).Error; err != nil {
		return err
	}

	row := &model.ProgramEventMedia{
		EventID:   eventID,
		FileID:    fileID,
		Role:      normalizedRole,
		SortOrder: next.SortOrder,
		IsPrimary: shouldBePrimary,
		Alt:       nullableString(alt),
		Caption:   nullableString(caption),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(row).Error; err != nil {
		return err
	}
	return bindProgramEventMediaPublicAsset(ctx, db, mediaAssets, *row)
}

func bindProgramEventMediaPublicAsset(ctx context.Context, db *gorm.DB, mediaAssets MediaAssets, media model.ProgramEventMedia) error {
	assetKind, ok := programEventMediaPublicAssetKind(media.Role)
	if !ok {
		return nil
	}
	_, err := mediaAssets.BindReadyAssetForSourceFile(
		ctx, db, media.FileID, "program_event", media.EventID, "media_"+media.ID, assetKind,
	)
	return err
}

// Program Event's inventory grants public-asset ownership only to poster
// media: that one asset is the shared Open Graph image. Other role relations
// preserve their File references without guessing a derivative kind.
func programEventMediaPublicAssetKind(role string) (string, bool) {
	if role == "poster" {
		return "poster", true
	}
	return "", false
}

func deleteProgramEventMedia(ctx context.Context, db *gorm.DB, mediaAssets MediaAssets, eventID string, mediaID string) (string, error) {
	eventID = strings.TrimSpace(eventID)
	mediaID = strings.TrimSpace(mediaID)
	if eventID == "" {
		return "", domainerr.Required("event_id")
	}
	if mediaID == "" {
		return "", domainerr.Required("media_id")
	}

	var media model.ProgramEventMedia
	if err := db.WithContext(ctx).First(&media, "id = ? AND event_id = ?", mediaID, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domainerr.ResourceNotFound("program event media", mediaID)
		}
		return "", err
	}
	result := db.WithContext(ctx).Delete(&model.ProgramEventMedia{}, "id = ? AND event_id = ?", mediaID, eventID)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", domainerr.ResourceNotFound("program event media", mediaID)
	}
	if media.IsPrimary {
		if err := promoteProgramEventMediaPrimary(ctx, db, media.EventID, media.Role); err != nil {
			return "", err
		}
	}
	if err := mediaAssets.ReleasePublicAssetBindings(ctx, db, "program_event", eventID, "media_"+media.ID); err != nil {
		return "", err
	}
	return media.FileID, nil
}

func reorderProgramEventMedia(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	role string,
	mediaIDs []string,
) error {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domainerr.Required("event_id")
	}
	normalizedRole, err := normalizeProgramEventMediaRole(role)
	if err != nil {
		return err
	}

	orderedIDs := make([]string, 0, len(mediaIDs))
	seen := make(map[string]struct{}, len(mediaIDs))
	for _, mediaID := range mediaIDs {
		mediaID = strings.TrimSpace(mediaID)
		if mediaID == "" {
			return domainerr.Required("media_id")
		}
		if _, ok := seen[mediaID]; ok {
			return domainerr.InvalidField("media_ids", "duplicate program event media id")
		}
		seen[mediaID] = struct{}{}
		orderedIDs = append(orderedIDs, mediaID)
	}

	var existing []model.ProgramEventMedia
	if err := db.WithContext(ctx).
		Where("event_id = ? AND role = ?", eventID, normalizedRole).
		Order("sort_order ASC, created_at ASC").
		Find(&existing).Error; err != nil {
		return err
	}
	if len(existing) != len(orderedIDs) {
		return domainerr.InvalidField("media_ids", "program event media order must include every item")
	}
	existingIDs := make(map[string]struct{}, len(existing))
	for _, media := range existing {
		existingIDs[media.ID] = struct{}{}
	}
	for _, mediaID := range orderedIDs {
		if _, ok := existingIDs[mediaID]; !ok {
			return domainerr.InvalidField("media_ids", "program event media id does not belong to event")
		}
	}
	if len(orderedIDs) == 0 {
		return nil
	}
	currentIDs := make([]string, len(existing))
	for index := range existing {
		currentIDs[index] = existing[index].ID
	}
	if sameStringSlice(currentIDs, orderedIDs) {
		return nil
	}

	now := time.Now().UTC()
	if err := db.WithContext(ctx).Model(&model.ProgramEventMedia{}).
		Where("event_id = ? AND role = ?", eventID, normalizedRole).
		Updates(structured.Fields{
			"is_primary": false,
			"updated_at": now,
		}).Error; err != nil {
		return err
	}
	for index, mediaID := range orderedIDs {
		result := db.WithContext(ctx).Model(&model.ProgramEventMedia{}).
			Where("id = ? AND event_id = ? AND role = ?", mediaID, eventID, normalizedRole).
			Updates(structured.Fields{
				"sort_order": int32(index),
				"is_primary": index == 0,
				"updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return domainerr.InvalidField("media_ids", "program event media id does not belong to event")
		}
	}
	return nil
}

func loadProgramEventPrimaryMediaFileID(ctx context.Context, db *gorm.DB, eventID, role string) (string, error) {
	var row struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).Raw(`SELECT file_id::text FROM program_event_media WHERE event_id = ? AND role = ? ORDER BY is_primary DESC, sort_order ASC, created_at ASC LIMIT 1`, eventID, role).Scan(&row).Error; err != nil {
		return "", err
	}
	return row.FileID, nil
}

func loadProgramEventMediaIDs(ctx context.Context, db *gorm.DB, eventID, role string) ([]string, error) {
	var rows []model.ProgramEventMedia
	if err := db.WithContext(ctx).Where("event_id = ? AND role = ?", eventID, role).Order("sort_order ASC, created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for index := range rows {
		ids[index] = rows[index].ID
	}
	return ids, nil
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameProgramEventMedia(left, right model.ProgramEventMedia) bool {
	return left.FileID == right.FileID && left.Role == right.Role && left.IsPrimary == right.IsPrimary && left.SortOrder == right.SortOrder && sameNullableString(left.Alt, right.Alt) && sameNullableString(left.Caption, right.Caption)
}

func deleteDefaultProgramEventPosterMedia(ctx context.Context, db *gorm.DB, mediaAssets MediaAssets, eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return domainerr.Required("event_id")
	}
	var media []model.ProgramEventMedia
	if err := db.WithContext(ctx).
		Where("event_id = ? AND role = ?", eventID, "poster").
		Find(&media).Error; err != nil {
		return err
	}
	if err := db.WithContext(ctx).
		Where("event_id = ? AND role = ?", eventID, "poster").
		Delete(&model.ProgramEventMedia{}).Error; err != nil {
		return err
	}
	for _, row := range media {
		if err := mediaAssets.ReleasePublicAssetBindings(ctx, db, "program_event", eventID, "media_"+row.ID); err != nil {
			return err
		}
	}
	return nil
}

func loadProgramEventMedia(ctx context.Context, db *gorm.DB, eventID string) ([]*managev1.ProgramEventMedia, error) {
	var rows []model.ProgramEventMedia
	if err := db.WithContext(ctx).
		Where("event_id = ?", eventID).
		Order("role ASC, is_primary DESC, sort_order ASC, created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*managev1.ProgramEventMedia, 0, len(rows))
	for i := range rows {
		result = append(result, &managev1.ProgramEventMedia{
			Id:        rows[i].ID,
			FileId:    rows[i].FileID,
			Role:      rows[i].Role,
			SortOrder: rows[i].SortOrder,
			IsPrimary: rows[i].IsPrimary,
			Alt:       rows[i].Alt,
			Caption:   rows[i].Caption,
			CreatedAt: timestamppb.New(rows[i].CreatedAt),
			UpdatedAt: timestamppb.New(rows[i].UpdatedAt),
		})
	}
	return result, nil
}

func loadProgramEventPrimaryPosterFileID(ctx context.Context, db *gorm.DB, eventID string) (*string, error) {
	var row struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).Raw(
		`SELECT file_id::text
		 FROM program_event_media
		 WHERE event_id = ? AND role = 'poster'
		 ORDER BY is_primary DESC, sort_order ASC, created_at ASC
		 LIMIT 1`,
		eventID,
	).Scan(&row).Error; err != nil {
		return nil, err
	}
	if row.FileID == "" {
		return nil, nil
	}
	return &row.FileID, nil
}

func loadProgramEventDefaultPosterFileIDs(ctx context.Context, db *gorm.DB, eventIDs []string) (map[string]*string, error) {
	result := make(map[string]*string, len(eventIDs))
	if len(eventIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EventID string `gorm:"column:event_id"`
		FileID  string `gorm:"column:file_id"`
	}
	if err := db.WithContext(ctx).Raw(
		`SELECT DISTINCT ON (event_id)
		     event_id::text,
		     file_id::text
		 FROM program_event_media
		 WHERE event_id::text IN ? AND role = 'poster'
		 ORDER BY event_id, is_primary DESC, sort_order ASC, created_at ASC`,
		eventIDs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		fileID := rows[i].FileID
		result[rows[i].EventID] = &fileID
	}
	return result, nil
}

func normalizeProgramEventMediaRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return "poster", nil
	}
	switch role {
	case "poster", "gallery", "lineup", "sponsor", "social", "venue":
		return role, nil
	default:
		return "", domainerr.InvalidField("role", "unsupported program event media role")
	}
}

func hasProgramEventMediaPrimary(ctx context.Context, db *gorm.DB, eventID string, role string) (bool, error) {
	query := db.WithContext(ctx).Model(&model.ProgramEventMedia{}).
		Where("event_id = ? AND role = ? AND is_primary = TRUE", eventID, role)
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func clearProgramEventMediaPrimary(ctx context.Context, db *gorm.DB, eventID string, role string) error {
	query := db.WithContext(ctx).Model(&model.ProgramEventMedia{}).
		Where("event_id = ? AND role = ?", eventID, role)
	if err := query.Updates(structured.Fields{
		"is_primary": false,
		"updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	return nil
}

func promoteProgramEventMediaPrimary(ctx context.Context, db *gorm.DB, eventID string, role string) error {
	query := db.WithContext(ctx).Where("event_id = ? AND role = ?", eventID, role)
	var replacement model.ProgramEventMedia
	if err := query.Order("sort_order ASC, created_at ASC").First(&replacement).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if err := db.WithContext(ctx).Model(&replacement).Updates(structured.Fields{
		"is_primary": true,
		"updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	return nil
}
