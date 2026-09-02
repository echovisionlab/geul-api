package og

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// authorizeOgEntityTarget prevents the author-role OG endpoints from becoming
// a cross-resource read or mutation surface. Site and legal route targets have
// no resource-manager relation, so they remain admin-only.
func (s *AdminService) authorizeOgEntityTarget(
	ctx context.Context,
	entityType managev1.OgEntityType,
	entityID string,
	requireEdit bool,
) error {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return err
	}
	policy, ok := PolicyForEntityType(entityType)
	if !ok {
		return errs.InvalidEntityType(entityType.String())
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return errs.Required("entity_id")
	}
	return s.authorizer.AuthorizeEntity(ctx, policy.Name, entityID, requireEdit)
}

func normalizeOgAuthorizationEntityID(entityType managev1.OgEntityType, entityID string) string {
	entityID = strings.TrimSpace(entityID)
	policy, ok := PolicyForEntityType(entityType)
	if !ok {
		return entityID
	}
	switch policy.LocaleStrategy {
	case LocaleStrategyStatic:
		return policy.CanonicalEntityID
	default:
		if entityType == managev1.OgEntityType_OG_ENTITY_TYPE_SITE && entityID == "" {
			return "default"
		}
		return entityID
	}
}

func (s *AdminService) authorizeStoredOgTarget(ctx context.Context, target *model.OgGenerationTarget, requireEdit bool) error {
	if target == nil {
		return errs.Required("target")
	}
	policy, ok := PolicyForEntityName(target.EntityType)
	if !ok {
		return errs.InvalidArgument("entity_type", "unknown OG target entity type")
	}
	return s.authorizeOgEntityTarget(ctx, policy.EntityType, target.EntityID, requireEdit)
}
