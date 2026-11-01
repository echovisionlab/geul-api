package formpostgres

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
)

func (s *store) CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (uuid.UUID, error) {
	document, err := s.ports.ContentBlocks.CreateDocument(ctx, s.tx, input)
	if err != nil {
		return uuid.Nil, err
	}
	return document.Document.ID, nil
}

func (s *store) DocumentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error) {
	revision, err := contentblock.CurrentRevision(ctx, s.tx, documentID)
	return revision, notFound(err, form.ErrContentDocumentNotFound)
}

func (s *store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence form.ContentFence, effect form.MetadataEffect) (contentblock.AdvanceResult, error) {
	return s.ports.ContentBlocks.AdvanceRevision(ctx, s.tx, input,
		func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
			return fence(ctx, s.with(tx), documentID)
		},
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return effect(ctx, s.with(tx))
		},
	)
}

func (s *store) LockAttachableFiles(ctx context.Context, fileIDs []string) error {
	return s.ports.Assets.LockAttachableFiles(ctx, s.tx, fileIDs)
}

func (s *store) FileID(ctx context.Context, fileID string) (string, error) {
	var file model.File
	if err := s.tx.WithContext(ctx).First(&file, "id = ?", fileID).Error; err != nil {
		return "", notFound(err, form.ErrFileNotFound)
	}
	return file.ID, nil
}

func (s *store) BindFeaturedImage(ctx context.Context, formID, fileID string) (*form.Asset, error) {
	asset, err := s.ports.Assets.BindFeaturedImage(ctx, s.tx, fileID, formID)
	return AssetOf(asset), err
}

func (s *store) ReleaseFeaturedImage(ctx context.Context, formID string) error {
	return s.ports.Assets.ReleaseFeaturedImage(ctx, s.tx, formID)
}

func (s *store) FeaturedImage(ctx context.Context, formID string) *form.Asset {
	return AssetOf(s.ports.Assets.FeaturedImage(ctx, s.tx, formID))
}

func (s *store) ReadyOGAsset(ctx context.Context, localizedID, sourceID *string) (*form.Asset, error) {
	asset, err := s.ports.OG.ReadyAsset(ctx, s.tx, localizedID, sourceID)
	return AssetOf(asset), err
}

func (s *store) RequestOG(ctx context.Context, formID, locale string, allLocales bool, reason string) (string, error) {
	return s.ports.OG.Request(ctx, s.tx, formID, locale, allLocales, reason)
}

func (s *store) CancelOG(ctx context.Context, formID string) error {
	return s.ports.OG.CancelAndRelease(ctx, s.tx, formID)
}
