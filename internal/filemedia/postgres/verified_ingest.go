package filemediapostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) EnsureIngestDateFolder(ctx context.Context) (*string, error) {
	if s.tx.Dialector.Name() != "postgres" {
		return nil, nil
	}
	var date struct{ Year, Month, Day string }
	if err := s.tx.WithContext(ctx).Raw(`SELECT to_char(CURRENT_DATE, 'YYYY') AS year, to_char(CURRENT_DATE, 'MM') AS month, to_char(CURRENT_DATE, 'DD') AS day`).Scan(&date).Error; err != nil {
		return nil, err
	}
	var parent *string
	for _, name := range []string{date.Year, date.Month, date.Day} {
		var id string
		if err := s.tx.WithContext(ctx).Raw(`INSERT INTO file_folder (id, parent_id, name, created_by_member_id, created_at, updated_at) VALUES (gen_random_uuid(), ?, ?, NULL, now(), now()) ON CONFLICT (parent_id, name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, parent, name).Scan(&id).Error; err != nil {
			return nil, err
		}
		parent = &id
	}
	return parent, nil
}

func (s *store) InsertIngestFile(ctx context.Context, file domain.File) error {
	fields := structured.Fields{"id": file.ID, "file_name": file.FileName, "mime_type": file.MimeType, "file_size": file.FileSize, "extension": file.Extension}
	if file.SHA256 != nil {
		fields["sha256"] = file.SHA256
	}
	if file.IngestSlotID != nil {
		fields["ingest_slot_id"] = *file.IngestSlotID
	}
	if file.IngestAttemptID != nil {
		fields["ingest_attempt_id"] = *file.IngestAttemptID
	}
	if file.FolderID != nil {
		fields["folder_id"] = *file.FolderID
	}
	if file.UploadedByMemberID != nil {
		fields["uploaded_by_member_id"] = *file.UploadedByMemberID
	}
	return s.tx.WithContext(ctx).Table("file").Create(fields).Error
}

func (s *store) InsertIngestBinding(ctx context.Context, input domain.IngestBinding, independent bool) error {
	row := model.FileIngestBinding(input)
	q := s.tx.WithContext(ctx)
	if independent {
		q = q.Omit("EntityID")
	}
	return q.Create(&row).Error
}

func (s *store) LockIngestOwner(ctx context.Context, kind, id string) error {
	table, ok := ingestOwnerTable(kind)
	if !ok || strings.TrimSpace(id) == "" {
		return nil
	}
	var row struct {
		ID string `gorm:"column:id"`
	}
	err := s.tx.WithContext(ctx).Table(table).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainerr.ResourceNotFound(kind, id)
	}
	if err != nil {
		return fmt.Errorf("lock File ingest %s target: %w", kind, err)
	}
	return nil
}

func ingestOwnerTable(kind string) (string, bool) {
	switch kind {
	case "artist", "form", "label", "page", "post", "program_event", "release", "series", "work":
		return kind, true
	default:
		return "", false
	}
}
