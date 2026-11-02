package filemediapostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FileReferenceQuery is one owner's scoped attachment-authority relation.
type FileReferenceQuery struct {
	Name  string
	Query *gorm.DB
}

// AttachmentReferences supplies owner queries on the caller's transaction.
type AttachmentReferences interface {
	Queries(context.Context, *gorm.DB, any) []FileReferenceQuery
}

func attachmentQueries(ctx context.Context, db *gorm.DB, references AttachmentReferences, fileID any) ([]FileReferenceQuery, error) {
	if references == nil {
		return nil, errors.New("File attachment reference owners are required")
	}
	queries := references.Queries(ctx, db, fileID)
	if len(queries) == 0 {
		return nil, errors.New("File attachment reference owner queries are required")
	}
	for _, reference := range queries {
		if reference.Query == nil {
			return nil, fmt.Errorf("File reference query %s is required", reference.Name)
		}
	}
	return append(queries, FileReferenceQuery{Name: "mesh_optimization_candidate.output_file_id", Query: db.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("mesh_optimization_candidate AS ref").Where("ref.output_file_id = ?", fileID).Where("ref.status IN ?", domain.MeshOutputProtectionStatuses())}), nil
}

// ActiveFileReferenceNames runs the owners' deletion checks under the File row lock.
func ActiveFileReferenceNames(ctx context.Context, db *gorm.DB, references AttachmentReferences, fileID string) ([]string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, nil
	}
	queries, err := attachmentQueries(ctx, db, references, fileID)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, reference := range queries {
		var count int64
		if err := reference.Query.Limit(1).Count(&count).Error; err != nil {
			return nil, fmt.Errorf("check %s reference: %w", reference.Name, err)
		}
		if count > 0 {
			result = append(result, reference.Name)
		}
	}
	return result, nil
}

// ActiveFileReferenceIDs preserves one statement and snapshot for a bounded File set.
func ActiveFileReferenceIDs(ctx context.Context, db *gorm.DB, references AttachmentReferences, fileIDs []string) (map[string]struct{}, error) {
	ids := sliceutil.SortedUniqueTrimmed(fileIDs)
	result := map[string]struct{}{}
	if len(ids) == 0 {
		return result, nil
	}
	queries, err := attachmentQueries(ctx, db, references, clause.Column{Table: "file", Name: "id"})
	if err != nil {
		return nil, err
	}
	query := db.WithContext(ctx).Table("file AS file").Select("file.id").Where("file.id IN ?", ids)
	for _, reference := range queries {
		query = query.Where("NOT EXISTS (?)", reference.Query.Select("1"))
	}
	var unreferenced []string
	if err := query.Order("file.id ASC").Pluck("file.id", &unreferenced).Error; err != nil {
		return nil, err
	}
	unreferencedSet := map[string]bool{}
	for _, id := range unreferenced {
		unreferencedSet[id] = true
	}
	for _, id := range ids {
		if !unreferencedSet[id] {
			result[id] = struct{}{}
		}
	}
	return result, nil
}

func (s *store) ActiveFileReferenceNames(ctx context.Context, id string) ([]string, error) {
	return ActiveFileReferenceNames(ctx, s.tx, s.ports.AttachmentReferences, id)
}
func (s *store) ActiveFileReferenceIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	return ActiveFileReferenceIDs(ctx, s.tx, s.ports.AttachmentReferences, ids)
}
