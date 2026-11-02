package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

func (s *Service) PromoteUserAvatarAsset(ctx context.Context, sourceFileID string) (*mediaasset.Reference, error) {
	return s.promoteSourceFileToPublicAsset(ctx, sourceFileID, "avatar")
}
