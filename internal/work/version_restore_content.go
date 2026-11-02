package work

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	"github.com/google/uuid"
)

type versionRestoreContent struct {
	store Store
	fence ContentFence
}

var _ versionrestore.Store[Store] = versionRestoreContent{}

func (s versionRestoreContent) LoadSnapshot(ctx context.Context, documentID uuid.UUID, locale string) (contentblock.Snapshot, error) {
	return s.store.LockedContentSnapshot(ctx, documentID, locale)
}

func (s versionRestoreContent) ReplaceSnapshot(ctx context.Context, input contentblock.ReplaceInput) (contentblock.Result, error) {
	return s.store.ReplaceContentSnapshot(ctx, input, s.fence)
}

func (s versionRestoreContent) AdvanceRevision(ctx context.Context, input contentblock.AdvanceInput, effect func(context.Context, Store) (contentblock.MetadataEffect, error)) (contentblock.AdvanceResult, error) {
	return s.store.AdvanceContentRevision(ctx, input, s.fence, effect)
}
