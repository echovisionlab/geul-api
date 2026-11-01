package filemediapostgres

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
)

func fileFolder(row model.FileFolder) domain.FileFolder {
	return domain.FileFolder{
		ID:                row.ID,
		ParentID:          row.ParentID,
		Name:              row.Name,
		CreatedByMemberID: row.CreatedByMemberID,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}
func fileFolderRow(value domain.FileFolder) model.FileFolder {
	return model.FileFolder{
		ID:                value.ID,
		ParentID:          value.ParentID,
		Name:              value.Name,
		CreatedByMemberID: value.CreatedByMemberID,
		CreatedAt:         value.CreatedAt,
		UpdatedAt:         value.UpdatedAt,
	}
}
