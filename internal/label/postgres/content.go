package postgres

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/label"
)

// domainFence runs the Label's fence over the transaction the content store
// hands it.
func (s *store) domainFence(fence label.ContentFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return fence(ctx, s.with(tx), documentID)
	}
}

func (s *store) CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	return s.deps.ContentBlocks.CreateDocument(ctx, s.tx, input)
}

func (s *store) LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.deps.ContentBlocks.LoadSnapshotInTransaction(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.deps.ContentBlocks.LoadSnapshot(ctx, s.tx, documentID, sourceLocale)
}

func (s *store) LockContentDocument(ctx context.Context, documentID uuid.UUID) error {
	return contentblock.LockDocumentRow(ctx, s.tx, documentID)
}

func (s *store) CurrentContentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error) {
	return contentblock.CurrentRevision(ctx, s.tx, documentID)
}

func (s *store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence label.ContentFence) (contentblock.Result, error) {
	return s.deps.ContentBlocks.ApplyBatch(ctx, s.tx, batch, s.domainFence(fence))
}

func (s *store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence label.ContentFence, effect label.TargetMetadataEffect) (contentblock.Result, error) {
	return s.deps.ContentBlocks.ApplyTargetLocaleBatchWithMetadata(ctx, s.tx, batch, locale, s.domainFence(fence),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			return effect(ctx, s.with(tx), contentChanged)
		})
}

func (s *store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence label.ContentFence) (contentblock.Result, error) {
	return s.deps.ContentBlocks.ReplaceSnapshot(ctx, s.tx, replace, s.domainFence(fence))
}

func (s *store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence label.ContentFence, effect label.MetadataEffect) (contentblock.AdvanceResult, error) {
	return s.deps.ContentBlocks.AdvanceRevision(ctx, s.tx, input, s.domainFence(fence),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return effect(ctx, s.with(tx))
		})
}

func (s *store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence label.ContentFence) error {
	return s.deps.ContentBlocks.DeleteDocument(ctx, s.tx, documentID, s.domainFence(fence))
}
