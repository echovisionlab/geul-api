package public

import (
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var programEventPublicFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{
				"program_event.title",
				"COALESCE((SELECT pet.summary FROM program_event_translation AS pet WHERE pet.entity_id = program_event.id AND pet.locale = program_event.source_locale LIMIT 1), '')",
			},
		},
		"type_id": {
			Column:     "type_id",
			Type:       queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN},
			IsFK:       true,
		},
		"location_mode": {
			Column:     "location_mode",
			Type:       queryutil.TypeEnum,
			AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN},
			EnumValues: []string{
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_MAP_PLACE.String(),
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE.String(),
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_HYBRID.String(),
				managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_TBA.String(),
			},
		},
		"series_id": {
			Column:     "series_id",
			Type:       queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{commonv1.FilterOp_FILTER_OP_EQ, commonv1.FilterOp_FILTER_OP_IN},
			IsFK:       true,
		},
		"map_place_id": {
			Column: "map_place_id",
			Type:   queryutil.TypeID,
			AllowedOps: []commonv1.FilterOp{
				commonv1.FilterOp_FILTER_OP_EQ,
				commonv1.FilterOp_FILTER_OP_IN,
				commonv1.FilterOp_FILTER_OP_IS_NULL,
				commonv1.FilterOp_FILTER_OP_IS_NOT_NULL,
			},
			IsFK: true,
		},
		"starts_at": {
			Column:     "starts_at",
			Type:       queryutil.TypeDate,
			AllowedOps: queryutil.DateOps,
		},
		"published_at": {
			Column:     "published_at",
			Type:       queryutil.TypeDate,
			AllowedOps: queryutil.DateOps,
		},
	},
}

func applyPublicProgramEventFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	remaining := make([]*commonv1.FilterSpec, 0, len(filters))
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "status":
			q, err := applyPublicProgramEventStatusFilter(query, filter)
			if err != nil {
				return nil, err
			}
			query = q
		case "time_window":
			q, err := applyProgramEventTimeWindowFilter(query, filter)
			if err != nil {
				return nil, err
			}
			query = q
		case "type_slug":
			q, err := applySlugSubqueryFilter(query, filter, "type_id", "program_event_type", "slug", "id")
			if err != nil {
				return nil, err
			}
			query = q
		case "series_slug":
			q, err := applySlugSubqueryFilter(query, filter, "series_id", "program_event_series", "slug", "id")
			if err != nil {
				return nil, err
			}
			query = q
		case "artist_id":
			q, err := applyRelationIDFilter(query, filter, "program_event_artist", "artist_id")
			if err != nil {
				return nil, err
			}
			query = q
		case "label_id":
			q, err := applyRelationIDFilter(query, filter, "program_event_label", "label_id")
			if err != nil {
				return nil, err
			}
			query = q
		case "client_id":
			q, err := applyRelationIDFilter(query, filter, "program_event_client", "client_id")
			if err != nil {
				return nil, err
			}
			query = q
		default:
			remaining = append(remaining, filter)
		}
	}
	return programEventPublicFilterConfig.ApplyFilters(query, remaining)
}

func applyPublicProgramEventStatusFilter(query *gorm.DB, filter *commonv1.FilterSpec) (*gorm.DB, error) {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		if isPublicProgramEventStatus(filter.GetValue()) {
			return query.Where("status = ?", filter.GetValue()), nil
		}
		return query.Where("1 = 0"), nil
	case commonv1.FilterOp_FILTER_OP_IN:
		statuses := make([]string, 0, len(filter.GetValues()))
		for _, value := range filter.GetValues() {
			if isPublicProgramEventStatus(value) {
				statuses = append(statuses, value)
			}
		}
		if len(statuses) == 0 {
			return query.Where("1 = 0"), nil
		}
		return query.Where("status IN ?", statuses), nil
	default:
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
}

func applyProgramEventTimeWindowFilter(query *gorm.DB, filter *commonv1.FilterSpec) (*gorm.DB, error) {
	if filter.GetOp() != commonv1.FilterOp_FILTER_OP_EQ {
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
	now := time.Now().UTC()
	switch strings.TrimSpace(strings.ToLower(filter.GetValue())) {
	case "", "all":
		return query, nil
	case "upcoming":
		return query.Where("starts_at >= ?", now), nil
	case "past":
		return query.Where("COALESCE(ends_at, starts_at) < ?", now), nil
	case "current":
		return query.Where("starts_at <= ? AND COALESCE(ends_at, starts_at) >= ?", now, now), nil
	default:
		return nil, errs.InvalidArgument("time_window", "must be one of upcoming, past, current, all")
	}
}

func applySlugSubqueryFilter(query *gorm.DB, filter *commonv1.FilterSpec, eventColumn string, tableName string, slugColumn string, idColumn string) (*gorm.DB, error) {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		return query.Where(eventColumn+" IN (SELECT "+idColumn+" FROM "+tableName+" WHERE "+slugColumn+" = ?)", filter.GetValue()), nil
	case commonv1.FilterOp_FILTER_OP_IN:
		return query.Where(eventColumn+" IN (SELECT "+idColumn+" FROM "+tableName+" WHERE "+slugColumn+" IN ?)", filter.GetValues()), nil
	default:
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
}

func applyRelationIDFilter(query *gorm.DB, filter *commonv1.FilterSpec, tableName string, relationColumn string) (*gorm.DB, error) {
	switch filter.GetOp() {
	case commonv1.FilterOp_FILTER_OP_EQ:
		return query.Where("id IN (SELECT event_id FROM "+tableName+" WHERE "+relationColumn+" = ?)", filter.GetValue()), nil
	case commonv1.FilterOp_FILTER_OP_IN:
		return query.Where("id IN (SELECT event_id FROM "+tableName+" WHERE "+relationColumn+" IN ?)", filter.GetValues()), nil
	default:
		return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
	}
}

func applySimpleProgramEventSeriesFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "search":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_LIKE && filter.GetOp() != commonv1.FilterOp_FILTER_OP_ILIKE {
				return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
			pattern := "%" + filter.GetValue() + "%"
			query = query.Where("title ILIKE ? OR summary ILIKE ?", pattern, pattern)
		default:
			return nil, errs.InvalidArgument("filter", "unsupported program event series filter")
		}
	}
	return query, nil
}

func applySimpleProgramEventTypeFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		switch filter.GetField() {
		case "search":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_LIKE && filter.GetOp() != commonv1.FilterOp_FILTER_OP_ILIKE {
				return nil, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
			query = query.Where("id IN (SELECT type_id FROM program_event_type_locale WHERE name ILIKE ? OR description ILIKE ?)", "%"+filter.GetValue()+"%", "%"+filter.GetValue()+"%")
		default:
			return nil, errs.InvalidArgument("filter", "unsupported program event type filter")
		}
	}
	return query, nil
}
