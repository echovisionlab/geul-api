package workpostgres

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	"github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// domainFence runs the Work's fence over the transaction the content store
// hands it.
func (s *store) domainFence(fence work.ContentFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return fence(ctx, s.with(tx), documentID)
	}
}

func (s *store) metadataEffect(effect work.MetadataEffect) func(context.Context, *gorm.DB) (contentblock.MetadataEffect, error) {
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

func (s *store) MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
	return contentblock.LoadMediaReferences(ctx, s.tx, documentID)
}

func (s *store) LocaleOwnershipConflict(ctx context.Context, documentID uuid.UUID, message string) error {
	return contentblock.LocaleOwnershipConflict(ctx, s.tx, documentID, message)
}

func (s *store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence work.ContentFence) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatch(ctx, s.tx, batch, s.domainFence(fence))
}

func (s *store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence work.ContentFence, effect work.MetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyBatchWithMetadata(ctx, s.tx, batch, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence work.ContentFence, effect work.TargetMetadataEffect) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ApplyTargetLocaleBatchWithMetadata(ctx, s.tx, batch, locale, s.domainFence(fence),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			return effect(ctx, s.with(tx), contentChanged)
		})
}

func (s *store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence work.ContentFence) (contentblock.Result, error) {
	return s.ports.ContentBlocks.ReplaceSnapshot(ctx, s.tx, replace, s.domainFence(fence))
}

func (s *store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence work.ContentFence, effect work.MetadataEffect) (contentblock.AdvanceResult, error) {
	return s.ports.ContentBlocks.AdvanceRevision(ctx, s.tx, input, s.domainFence(fence), s.metadataEffect(effect))
}

func (s *store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence work.ContentFence) error {
	return s.ports.ContentBlocks.DeleteDocument(ctx, s.tx, documentID, s.domainFence(fence))
}

func (s *store) RestoreVersion(ctx context.Context, restore work.VersionRestore) (versionrestore.Outcome, error) {
	return versionrestore.Restore{
		Store: s.ports.ContentBlocks, DocumentID: restore.DocumentID, Version: restore.Version,
		Fence: s.domainFence(restore.Fence),
		LoadSource: func(ctx context.Context, tx *gorm.DB) (versionrestore.Source, error) {
			return restore.LoadSource(ctx, s.with(tx))
		},
		SwitchSourceLocale: func(ctx context.Context, tx *gorm.DB, from string, revision uuid.UUID) error {
			return restore.SwitchSourceLocale(ctx, s.with(tx), from, revision)
		},
		Replacement: func(ctx context.Context, tx *gorm.DB, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			return restore.Replacement(ctx, s.with(tx), revision)
		},
		UpdateMetadata: func(ctx context.Context, tx *gorm.DB) error {
			return restore.UpdateMetadata(ctx, s.with(tx))
		},
	}.Apply(ctx, s.tx)
}
