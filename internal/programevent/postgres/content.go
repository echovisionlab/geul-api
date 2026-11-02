package programeventpostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// domainFence runs the event's fence over the transaction the content store
// hands it.
func (s *store) domainFence(fence programevent.ContentFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return fence(ctx, s.with(tx), documentID)
	}
}

func (s *store) metadataEffect(effect programevent.MetadataEffect) contentblock.MetadataMutation {
	return func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
		return effect(ctx, s.with(tx))
	}
}

func (s *store) CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.CreateDocument(ctx, s.tx, input)
}

func (s *store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.LoadSnapshotInTransaction(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error) {
	return contentblock.LoadMediaReferences(ctx, s.tx, documentID)
}

func (s *store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence programevent.ContentFence) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatch(ctx, s.tx, batch, s.domainFence(fence))
}

func (s *store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence programevent.ContentFence, effect programevent.MetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatchWithMetadata(ctx, s.tx, batch, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence programevent.ContentFence, effect programevent.TargetMetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyTargetLocaleBatchWithMetadata(ctx, s.tx, batch, locale, s.domainFence(fence),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			return effect(ctx, s.with(tx), contentChanged)
		})
}

func (s *store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence programevent.ContentFence, effect programevent.MetadataEffect) (contentblock.AdvanceResult, error) {
	return s.ports.ContentBlocks.AdvanceRevision(ctx, s.tx, input, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence programevent.ContentFence) error {
	return s.ports.ContentBlocks.DeleteDocument(ctx, s.tx, documentID, s.domainFence(fence))
}

func (s *store) ContentFileMIMEType(ctx context.Context, blockID uuid.UUID, referencePath string, fileID uuid.UUID) (string, bool, error) {
	var file struct {
		MIMEType string `gorm:"column:mime_type"`
	}
	result := s.tx.WithContext(ctx).Raw(`
		SELECT file.mime_type
		FROM content_block_attachment AS attachment
		JOIN file ON file.id = attachment.file_id
		WHERE attachment.block_id = ? AND attachment.reference_path = ?
		  AND attachment.selector_kind = 'active' AND attachment.file_id = ?
		  AND file.delete_requested_at IS NULL
	`, blockID, referencePath, fileID).Scan(&file)
	if result.Error != nil {
		return "", false, result.Error
	}
	return file.MIMEType, result.RowsAffected == 1, nil
}

func (s *store) SiteVariables(ctx context.Context) (sitevariable.Context, error) {
	return sitevariable.LoadRuntime(ctx, s.tx)
}
