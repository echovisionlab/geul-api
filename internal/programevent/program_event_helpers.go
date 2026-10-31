package programevent

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toProtoProgramEventSummary(event *model.ProgramEvent, posterFileID *string) *managev1.ProgramEventSummary {
	slug := event.Slug
	return &managev1.ProgramEventSummary{
		Id:           event.ID,
		Status:       programEventStatusToProto(event.Status),
		SourceLocale: event.SourceLocale,
		Title:        event.Title,
		Slug:         &slug,
		TypeId:       event.TypeID,
		SeriesId:     event.SeriesID,
		StartsAt:     timestamppb.New(event.StartsAt),
		EndsAt:       timestampProtoPtr(event.EndsAt),
		Timezone:     event.Timezone,
		LocationMode: locationModeToProto(event.LocationMode),
		MapPlaceId:   event.MapPlaceID,
		PosterFileId: posterFileID,
		PublishedAt:  timestampProtoPtr(event.PublishedAt),
		UpdatedAt:    timestamppb.New(event.UpdatedAt),
	}
}

func collectProgramEventIDs(events []model.ProgramEvent) []string {
	ids := make([]string, 0, len(events))
	for i := range events {
		ids = append(ids, events[i].ID)
	}
	return ids
}

func validateProgramEventSlug(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", errs.Required("slug")
	}
	if err := pathslug.Validate(slug); err != nil {
		return "", err
	}
	if len(slug) > 160 {
		return "", errs.InvalidArgument("slug", "must be at most 160 characters")
	}
	for _, r := range slug {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '-' {
			return "", errs.InvalidArgument("slug", "can only contain lowercase letters, numbers, and hyphens")
		}
	}
	return slug, nil
}

func normalizeRequiredProgramEventLocale(field, locale string) (string, error) {
	normalized := localization.NormalizeSupportedLocale(locale)
	if normalized == nil {
		return "", errs.InvalidArgument(field, "unsupported locale")
	}
	return *normalized, nil
}

func validateProgramEventLocation(mode model.ProgramEventLocationMode, mapPlaceID *string) error {
	switch mode {
	case model.ProgramEventLocationModeMapPlace, model.ProgramEventLocationModeHybrid:
		if mapPlaceID == nil || strings.TrimSpace(*mapPlaceID) == "" {
			return errs.Required("map_place_id")
		}
	case model.ProgramEventLocationModeOnline, model.ProgramEventLocationModeTBA:
	default:
		return errs.InvalidArgument("location_mode", "unsupported location mode")
	}
	return nil
}

func validateProgramEventTimeRange(startsAt time.Time, endsAt *time.Time) error {
	if endsAt != nil && endsAt.Before(startsAt) {
		return errs.InvalidArgument("ends_at", "must be greater than or equal to starts_at")
	}
	return nil
}

func programEventSeriesStatusStorageValue(status managev1.ProgramEventSeriesStatus) (model.ProgramEventStatus, error) {
	stored, ok := programEventSeriesStatusFromProto(status)
	if !ok {
		return "", errs.InvalidArgument("status", "must be draft or published")
	}
	return stored, nil
}

func mapProgramEventSeriesFilters(filters []*commonv1.FilterSpec) ([]*commonv1.FilterSpec, error) {
	mapped := make([]*commonv1.FilterSpec, 0, len(filters))
	for _, filter := range filters {
		if filter == nil || filter.GetField() != "status" {
			mapped = append(mapped, filter)
			continue
		}
		mappedFilter := &commonv1.FilterSpec{Field: filter.GetField(), Op: filter.GetOp()}
		if filter.GetOp() == commonv1.FilterOp_FILTER_OP_IN || filter.GetOp() == commonv1.FilterOp_FILTER_OP_NOT_IN {
			mappedFilter.Values = make([]string, 0, len(filter.GetValues()))
			for _, value := range filter.GetValues() {
				storageValue, err := programEventSeriesFilterStorageValue(value)
				if err != nil {
					return nil, err
				}
				mappedFilter.Values = append(mappedFilter.Values, string(storageValue))
			}
		} else {
			storageValue, err := programEventSeriesFilterStorageValue(filter.GetValue())
			if err != nil {
				return nil, err
			}
			mappedFilter.Value = string(storageValue)
		}
		mapped = append(mapped, mappedFilter)
	}
	return mapped, nil
}

func programEventSeriesFilterStorageValue(value string) (model.ProgramEventStatus, error) {
	stored, ok := programEventSeriesStatusFilterValues[value]
	if !ok {
		return "", errs.InvalidArgument("status", "must be draft or published")
	}
	return stored, nil
}

func paginationLimitOffset(p *commonv1.PaginationRequest, defaultLimit int32) (int32, int32) {
	limit := defaultLimit
	offset := int32(0)
	if p != nil {
		if p.Limit > 0 {
			limit = p.Limit
		}
		offset = p.Offset
	}
	return limit, offset
}

func timestampPtr(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	value := ts.AsTime()
	return &value
}

func timestampProtoPtr(ts *time.Time) *timestamppb.Timestamp {
	if ts == nil {
		return nil
	}
	return timestamppb.New(*ts)
}

func nullableString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func countPresent(values ...*string) int {
	count := 0
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			count++
		}
	}
	return count
}

func normalizedStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// lockProgramEventForMutation serializes an admin-only Program Event mutation
// with lifecycle transitions. Archived is a visibility state; its general
// mutations remain permitted for an active Site Admin.
func lockProgramEventForMutation(ctx context.Context, db *gorm.DB, eventID string) error {
	var event struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("program_event").
		Select("id").
		Where("id = ?", eventID).
		Take(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("program event", eventID)
		}
		return errs.Internal(err)
	}
	return nil
}

func requireProgramEventMutationPermissionWithDB(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	eventID string,
	normal programEventAction,
) error {
	var event struct {
		Status model.ProgramEventStatus `gorm:"column:status"`
	}
	if err := db.WithContext(ctx).Table("program_event").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("status").Where("id = ?", eventID).Take(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("program event", eventID)
		}
		return errs.Internal(err)
	}
	can, err := normal(eventID)
	if err != nil {
		return errs.WithCause(errs.InvalidArgument("resource.id", "must be a canonical resource UUID"), err)
	}
	if err := requireActiveProgramEventPrincipal(ctx, db, can.Action().Name(), false); err != nil {
		return err
	}
	return requireProgramEventPermission(ctx, checker, eventID, programEventMutationAction(event.Status, normal))
}

func requireActiveProgramEventPrincipal(
	ctx context.Context,
	tx *gorm.DB,
	actionName string,
	maskNotFound bool,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if active {
		return nil
	}
	if maskNotFound {
		return errs.NotFoundMsg("program event not found")
	}
	return errs.NoPermission(actionName, "program event")
}

func RequireLockedSourceLocaleEdit(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	eventID string,
) error {
	return requireProgramEventMutationPermissionWithDB(
		ctx, tx, checker, eventID, policyv1.ProgramEvent.Edit,
	)
}

func RequireLockedView(
	ctx context.Context,
	tx *gorm.DB,
	checker CollaborationPermissionChecker,
	eventID string,
) error {
	var event struct {
		Status model.ProgramEventStatus `gorm:"column:status"`
	}
	if err := tx.WithContext(ctx).Table("program_event").
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Select("status").Where("id = ?", eventID).Take(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("program event", eventID)
		}
		return errs.Internal(err)
	}
	if err := requireActiveProgramEventPrincipal(ctx, tx, "view", true); err != nil {
		return err
	}
	if err := requireProgramEventPermission(
		ctx, checker, eventID, programEventViewAction(event.Status),
	); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return errs.WithCause(errs.NotFound("program event", eventID), err)
		}
		return err
	}
	return nil
}

func nextProgramEventCreditSortOrder(ctx context.Context, db *gorm.DB, eventID string) (int32, error) {
	var row struct {
		SortOrder int32 `gorm:"column:sort_order"`
	}
	if err := db.WithContext(ctx).
		Raw("SELECT COALESCE(MAX(sort_order), -1) + 1 AS sort_order FROM program_event_credit WHERE event_id = ?", eventID).
		Scan(&row).Error; err != nil {
		return 0, errs.Internal(err)
	}
	return row.SortOrder, nil
}

func touchProgramEvent(ctx context.Context, db *gorm.DB, eventID string) error {
	if err := db.WithContext(ctx).
		Model(&model.ProgramEvent{}).
		Where("id = ?", eventID).
		Update("updated_at", time.Now().UTC()).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}
