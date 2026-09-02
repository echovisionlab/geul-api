package account

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
)

func (s *AccountEmailService) BackfillMemberEmailProjection(
	ctx context.Context,
	pageSize int,
) (*AccountEmailBackfillResult, error) {
	result := &AccountEmailBackfillResult{}
	if s.identityManager == nil {
		return result, fmt.Errorf("identity manager is required")
	}
	lister, ok := s.identityManager.(auth.IdentityLister)
	if !ok {
		return result, fmt.Errorf("identity manager does not support listing identities")
	}
	if pageSize <= 0 {
		pageSize = 100
	}

	for page := 0; ; page++ {
		identities, total, err := lister.ListIdentities(ctx, page, pageSize)
		if err != nil {
			return result, err
		}
		if len(identities) == 0 {
			break
		}
		for _, identity := range identities {
			if identity == nil || strings.TrimSpace(identity.ID) == "" {
				continue
			}
			result.Processed++
			if _, err := s.SyncMemberEmailProjection(ctx, identity.ID, nil, nil); err != nil {
				result.Failed++
				slog.Warn("failed to backfill Member email projection", "identity_id", identity.ID, "error", err)
				continue
			}
			result.Synced++
		}
		if len(identities) < pageSize {
			break
		}
		if total > 0 && int64((page+1)*pageSize) >= total {
			break
		}
	}

	return result, nil
}
