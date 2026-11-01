package filemediapostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) LockDeliveryFiles(ctx context.Context, fileIDs []string, scope domain.DeliveryFileScope) (map[string]domain.File, error) {
	var fields []string
	switch scope {
	case domain.CatalogDelivery:
		fields = []string{"id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "folder_id", "uploaded_by_member_id", "delete_requested_at", "created_at", "updated_at"}
	case domain.StrongDelivery:
		fields = []string{"id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "ingest_slot_id", "ingest_attempt_id", "uploaded_by_member_id", "delete_requested_at", "client_media_bundle_id"}
	default:
		return nil, fmt.Errorf("unsupported delivery File scope %d", scope)
	}

	var locked []model.File
	query := s.tx.WithContext(ctx).
		Select(fields).
		Where("id IN ?", fileIDs).
		Order("id ASC")
	if s.tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	if err := query.Find(&locked).Error; err != nil {
		return nil, err
	}
	lockedByID := make(map[string]domain.File, len(locked))
	for _, file := range locked {
		lockedByID[file.ID] = domain.File(file)
	}
	return lockedByID, nil
}
func (s *store) LockMeshDeliveryWitnesses(
	ctx context.Context,
	witnesses []domain.MeshDeliveryWitness,
) (map[string]bool, error) {
	current := make(map[string]bool, len(witnesses))
	for _, witness := range witnesses {
		var row struct{ ID string }
		query := s.tx.WithContext(ctx).Table("mesh_optimization_candidate").Select("id").Where(
			"id = ? AND source_file_id = ? AND status = ? AND output_file_id = ?",
			witness.CandidateID, witness.SourceFileID, model.MeshOptimizationCandidateStatusReady, witness.OutputFileID,
		)
		if s.tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
		}
		result := query.Take(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			continue
		}
		if result.Error != nil {
			return nil, result.Error
		}
		current[witness.CandidateID] = true
	}
	return current, nil
}
func (s *store) LockExactDeliveryUsage(
	ctx context.Context,
	witness domain.DeliveryUsageWitness,
) (bool, error) {
	query := s.tx.WithContext(ctx)
	if s.tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	var row struct{ ID string }
	switch witness.Kind {
	case "content_attachment":
		result := query.Table("content_block_attachment").Select("block_id AS id").
			Where("block_id = ? AND reference_path = ? AND selector_kind = 'active' AND file_id = ?", witness.RelationID, witness.ReferencePath, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "featured_image":
		table, ok := deliveryOwnerTable(witness.ResourceType)
		if !ok {
			return false, fmt.Errorf("unsupported featured image owner %q", witness.ResourceType)
		}
		result := query.Table(table).Select("id").Where("id = ? AND featured_image_file_id = ?", witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "artist_file":
		result := query.Table("artist_file").Select("artist_id AS id").Where("artist_id = ? AND file_id = ?", witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "release_file":
		result := query.Table("release_file").Select("release_id AS id").Where("release_id = ? AND file_id = ?", witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "program_event_media":
		result := query.Table("program_event_media").Select("id").Where("id = ? AND event_id = ? AND file_id = ?", witness.RelationID, witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "track":
		result := query.Table("track").Select("id").Where("id = ? AND release_id = ? AND audio_original_file_id = ?", witness.RelationID, witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "label_logo":
		column := "logo_light_file_id"
		if witness.ReferencePath == "logo_dark" {
			column = "logo_dark_file_id"
		}
		result := query.Table("label").Select("id").Where("id = ? AND "+column+" = ?", witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "series_featured_image":
		result := query.Table("series").Select("id").Where("id = ? AND featured_image_file_id = ?", witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "form_featured_image":
		result := query.Table("form").Select("id").Where("id = ? AND featured_image_file_id = ?", witness.ResourceID, witness.FileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	default:
		return false, fmt.Errorf("unsupported manage delivery usage witness %q", witness.Kind)
	}
}
func manageDeliveryUsageLockResult(result *gorm.DB) (bool, error) {
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if result.Error != nil {
		return false, result.Error
	}
	return true, nil
}

func deliveryOwnerTable(kind string) (string, bool) {
	switch kind {
	case "artist", "form", "label", "page", "post", "program_event", "release", "series", "work":
		return kind, true
	default:
		return "", false
	}
}
func (s *store) LockDeliveryPrincipal(ctx context.Context, user *auth.UserInfo) (bool, error) {
	return identitystate.LockActivePrincipal(ctx, s.tx, user)
}
func (s *store) LockAvatarFileBinding(ctx context.Context, fileID, identityID string) (bool, error) {
	var row model.FileIngestBinding
	q := s.tx.WithContext(ctx).Where("file_id = ? AND upload_type = ? AND entity_id = ?", fileID, "UPLOAD_TYPE_USER_AVATAR", identityID)
	if s.tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	return manageDeliveryUsageLockResult(q.Take(&row))
}
func (s *store) LockUsageDeliveryFile(ctx context.Context, id string) (domain.File, bool, error) {
	var row model.File
	q := s.tx.WithContext(ctx).Select("id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "ingest_slot_id", "ingest_attempt_id", "delete_requested_at", "client_media_bundle_id").Where("id = ?", id)
	if s.tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.File{}, false, nil
	}
	return domain.File(row), err == nil, err
}
func (s *store) LockDeliveryOwner(ctx context.Context, kind, id string) (domain.DeliveryOwner, bool, error) {
	table, ok := deliveryOwnerTable(kind)
	if !ok {
		return domain.DeliveryOwner{}, false, fmt.Errorf("unsupported manage delivery usage owner %q", kind)
	}
	var row domain.DeliveryOwner
	fields := "id"
	if kind == "page" || kind == "post" || kind == "program_event" || kind == "work" {
		fields = "id, content_document_id"
		if kind == "post" || kind == "program_event" {
			fields += ", status"
		}
	}
	q := s.tx.WithContext(ctx).Table(table).Select(fields).Where("id = ?", id)
	if s.tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.DeliveryOwner{}, false, nil
	}
	return row, err == nil, err
}
