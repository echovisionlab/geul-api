package label

import (
	"context"
	"fmt"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func loadReadyManageOgAssetRefs(ctx context.Context, runtime Runtime, db *gorm.DB, candidates ...*string) (map[string]*commonv1.AssetRef, error) {
	ids := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		id := strings.TrimSpace(*candidate)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return runtime.ResolveReadyAssetRefs(ctx, db, ids)
}

func manageOgAssetFromReadyMap(ready map[string]*commonv1.AssetRef, candidates ...*string) *commonv1.AssetRef {
	for _, candidate := range candidates {
		if candidate != nil {
			if asset := ready[strings.TrimSpace(*candidate)]; asset != nil {
				return asset
			}
		}
	}
	return nil
}

func readyManageOgAssetRef(ctx context.Context, runtime Runtime, db *gorm.DB, candidates ...*string) (*commonv1.AssetRef, error) {
	ready, err := loadReadyManageOgAssetRefs(ctx, runtime, db, candidates...)
	if err != nil {
		return nil, err
	}
	return manageOgAssetFromReadyMap(ready, candidates...), nil
}

func validateResourceDeletionAuthorizationBatchSize(resourceName string, deleted, restored []policyv1.RelationshipMutation) error {
	const maxMutations = 1000
	if len(deleted) <= maxMutations && len(restored) <= maxMutations {
		return nil
	}
	return errs.FailedPrecondition(fmt.Sprintf("%s has too many authorization relationships to delete atomically; remove participant relationships or reparent dependent resources first", resourceName))
}

func timestampProtoPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}
