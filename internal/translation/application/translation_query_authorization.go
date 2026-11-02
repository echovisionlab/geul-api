package application

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
)

// authorizeTranslationJobList keeps the global job browser admin-only while
// allowing an authenticated resource manager to read only the jobs for the
// exact entity they can edit. The latter is the narrow projection Web uses for
// translation status visibility and explicit cancel/regenerate actions.
func (s *Service) authorizeTranslationJobList(
	ctx context.Context,
	filters []*commonv1.FilterSpec,
) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if isAdmin, err := checkSpiceDBAdmin(ctx, auth.GetUser(ctx), s.spiceDB); err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	} else if isAdmin {
		return nil
	}
	if strings.TrimSpace(user.MemberID.String()) == "" {
		return domainerr.AuthenticationRequired()
	}

	entityType, entityID, exactScope := exactTranslationJobEntityScope(filters)
	if _, err := uuid.Parse(entityID); !exactScope || err != nil {
		return domainerr.AdminRequired()
	}
	if entityType == "work" {
		return domainerr.AdminRequired()
	}
	if _, ok := translation.DefinitionForKind(entityType); !ok {
		return domainerr.InvalidField("filters.entity_type", "unsupported translation entity type")
	}
	return s.requireJobRead(ctx, entityType, entityID)
}

// requireJobRead asks the owning domain whether the principal may read the
// entity's jobs. A domain may lock the entity and the principal to decide.
func (s *Service) requireJobRead(ctx context.Context, entityType, entityID string) error {
	return s.units.ReadCurrent(ctx, func(store Store) error {
		return store.Domains().RequireJobRead(ctx, entityType, entityID)
	})
}

func exactTranslationJobEntityScope(filters []*commonv1.FilterSpec) (string, string, bool) {
	var entityType string
	var entityID string
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "entity_type":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_EQ || entityType != "" {
				return "", "", false
			}
			entityType = strings.TrimSpace(filter.GetValue())
		case "entity_id":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_EQ || entityID != "" {
				return "", "", false
			}
			entityID = strings.TrimSpace(filter.GetValue())
		}
	}
	return entityType, entityID, entityType != "" && entityID != ""
}

func (s *Service) authorizeTranslationRead(
	ctx context.Context,
	entityType string,
	entityID string,
) error {
	if isAdmin, err := checkSpiceDBAdmin(ctx, auth.GetUser(ctx), s.spiceDB); err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	} else if isAdmin {
		return nil
	}
	if entityType != "series" {
		return domainerr.AdminRequired()
	}
	return s.requireJobRead(ctx, entityType, entityID)
}
