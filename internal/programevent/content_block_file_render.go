package programevent

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type contentBlockFileRenderResolver struct {
	db     *gorm.DB
	assets MediaAssets
}

func newContentBlockFileRenderResolver(db *gorm.DB, assets MediaAssets) contentblock.FileRenderResolver {
	return &contentBlockFileRenderResolver{db: db, assets: assets}
}

func (r *contentBlockFileRenderResolver) ResolveContentBlockFile(
	ctx context.Context,
	selector contentblock.FileRenderSelector,
) (contentblock.FileRenderTarget, error) {
	if r == nil || r.db == nil || r.assets == nil || selector.BlockID == uuid.Nil || selector.ReferencePath == "" || selector.FileID == uuid.Nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("invalid Program Event Content Block File render selector")
	}
	var file struct {
		MIMEType string `gorm:"column:mime_type"`
	}
	result := r.db.WithContext(ctx).Raw(`
		SELECT file.mime_type
		FROM content_block_attachment AS attachment
		JOIN file ON file.id = attachment.file_id
		WHERE attachment.block_id = ? AND attachment.reference_path = ?
		  AND attachment.selector_kind = 'active' AND attachment.file_id = ?
		  AND file.delete_requested_at IS NULL
	`, selector.BlockID, selector.ReferencePath, selector.FileID).Scan(&file)
	if result.Error != nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("load exact Program Event Content Block File: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return contentblock.FileRenderTarget{}, fmt.Errorf("exact Program Event Content Block File reference does not exist")
	}
	if !strings.HasPrefix(file.MIMEType, "image/") {
		return contentblock.FileRenderTarget{MIMEType: file.MIMEType}, nil
	}
	ref, err := r.assets.ResolveSingleReadyInlineAssetForSourceFile(ctx, r.db, selector.FileID.String(), "image")
	if err != nil {
		return contentblock.FileRenderTarget{}, err
	}
	return contentblock.FileRenderTarget{URL: ref.GetUrl(), MIMEType: ref.GetMimeType()}, nil
}
