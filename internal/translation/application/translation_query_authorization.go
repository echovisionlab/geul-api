package application

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
)

func applyTranslationJobFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		value := strings.TrimSpace(filter.GetValue())
		switch filter.GetField() {
		case "entity_type", "entity_id", "target_locale", "source_locale":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_EQ {
				return nil, invalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
			query = query.Where(fmt.Sprintf("%s = ?", filter.GetField()), value)
		case "status":
			switch filter.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				query = query.Where(fmt.Sprintf("%s = ?", filter.GetField()), value)
			case commonv1.FilterOp_FILTER_OP_IN:
				values := make([]string, 0, len(filter.GetValues()))
				for _, item := range filter.GetValues() {
					if normalized := strings.TrimSpace(item); normalized != "" {
						values = append(values, normalized)
					}
				}
				if len(values) == 0 {
					return nil, domainerr.InvalidField(filter.GetField(), "IN filter requires at least one value")
				}
				query = query.Where(fmt.Sprintf("%s IN ?", filter.GetField()), values)
			default:
				return nil, invalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
		default:
			return nil, domainerr.New(domainerr.Invalid, filter.GetField(), "invalid filter field: "+filter.GetField())
		}
	}
	return query, nil
}

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
	return s.domains.RequireJobRead(ctx, s.db, s.spiceDB, entityType, entityID)
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
	return s.domains.RequireJobRead(ctx, s.db, s.spiceDB, entityType, entityID)
}

func invalidFilterOp(field, op string) error {
	return domainerr.New(domainerr.Invalid, field, fmt.Sprintf("invalid filter operation '%s' for field '%s'", op, field))
}
