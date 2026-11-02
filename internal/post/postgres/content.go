package postpostgres

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/post"
)

// domainFence runs the Post's fence over the transaction the content store
// hands it.
func (s *store) domainFence(fence post.ContentFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return fence(ctx, s.with(tx), documentID)
	}
}

func (s *store) metadataEffect(effect post.MetadataEffect) contentblock.MetadataMutation {
	return func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
		return effect(ctx, s.with(tx))
	}
}

func (s *store) CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.CreateDocument(ctx, s.tx, input)
}

func (s *store) LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.LoadSnapshotInTransaction(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.ports.ContentBlocks.LoadSnapshot(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) LockContentDocument(ctx context.Context, documentID uuid.UUID) error {
	var document contentblock.Document
	return s.tx.WithContext(ctx).Table("content_document").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", documentID).
		Take(&document).Error
}

func (s *store) ContentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error) {
	var document contentblock.Document
	if err := s.tx.WithContext(ctx).Table("content_document").
		Select("revision").
		Where("id = ?", documentID).
		Take(&document).Error; err != nil {
		return uuid.Nil, err
	}
	return document.Revision, nil
}

func (s *store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence post.ContentFence) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatch(ctx, s.tx, batch, s.domainFence(fence))
}

func (s *store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence post.ContentFence, effect post.MetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatchWithMetadata(ctx, s.tx, batch, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence post.ContentFence, effect post.TargetMetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyTargetLocaleBatchWithMetadata(ctx, s.tx, batch, locale, s.domainFence(fence),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			return effect(ctx, s.with(tx), contentChanged)
		})
}

func (s *store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence post.ContentFence) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ReplaceSnapshot(ctx, s.tx, replace, s.domainFence(fence))
}

func (s *store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence post.ContentFence, effect post.MetadataEffect) (contentblock.AdvanceResult, error) {
	return s.ports.ContentBlocks.AdvanceRevision(ctx, s.tx, input, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence post.ContentFence) error {
	return s.ports.ContentBlocks.DeleteDocument(ctx, s.tx, documentID, s.domainFence(fence))
}
