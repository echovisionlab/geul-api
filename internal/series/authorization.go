package series

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type seriesPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	LookupResources(
		context.Context,
		policyv1.ResourceLookup,
		policyv1.Actor,
	) ([]string, error)
}

type seriesAction = auth.ResourceAction

// SeriesService implements the SeriesService Connect handler
func (s *SeriesService) requireAssignableSeries(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
) error {
	return s.requireSeriesPermissionAndLock(ctx, tx, seriesID, policyv1.PostSeries.Manage)
}

func (s *SeriesService) requireSeriesManageAndLock(ctx context.Context, tx *gorm.DB, seriesID string) error {
	return s.requireSeriesPermissionAndLock(ctx, tx, seriesID, policyv1.PostSeries.Manage)
}

func (s *SeriesService) requireSeriesPermissionAndLock(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	action seriesAction,
) error {
	return requireSeriesPermissionAndLock(ctx, tx, s.permissions, seriesID, action)
}

func requireSeriesPermissionAndLock(
	ctx context.Context,
	tx *gorm.DB,
	checker seriesPermissionChecker,
	seriesID string,
	action seriesAction,
) error {
	if err := lockSeriesRoot(ctx, tx, seriesID); err != nil {
		return err
	}
	principal := auth.GetUser(ctx)
	if principal == nil {
		return errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if !active {
		return errs.NotFound("series", seriesID)
	}
	return checkSeriesPermission(ctx, checker, seriesID, action, principal)
}

// RequireViewAndLockWithDB is the translation read boundary for a concrete
// Series. It performs exactly one generated view permission check after the
// root and active principal are locked.
func RequireViewAndLockWithDB(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	seriesID string,
) error {
	return requireSeriesPermissionAndLock(ctx, tx, spiceDB, seriesID, policyv1.PostSeries.View)
}

// RequireEditAndLockWithDB is the translation source/target mutation boundary
// for a concrete Series.
func RequireEditAndLockWithDB(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	seriesID string,
) error {
	return requireSeriesPermissionAndLock(ctx, tx, spiceDB, seriesID, policyv1.PostSeries.Edit)
}

func checkSeriesPermission(
	ctx context.Context,
	checker seriesPermissionChecker,
	seriesID string,
	action seriesAction,
	principal *auth.UserInfo,
) error {
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := action(seriesID)
	if err != nil {
		return errs.WithCause(errs.NotFound("series", seriesID), err)
	}
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return errs.AuthenticationRequired()
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	if !allowed {
		return errs.NotFound("series", seriesID)
	}
	return nil
}

func (s *SeriesService) requireSeriesPermissionOrNotFound(
	ctx context.Context,
	seriesID string,
	action seriesAction,
) error {
	return checkSeriesPermission(ctx, s.permissions, seriesID, action, auth.GetUser(ctx))
}

func requireSeriesPlatformPermission(
	ctx context.Context,
	checker seriesPermissionChecker,
	can policyv1.Can,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return errs.AuthenticationRequired()
	}
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), can.Resource().Type())
	}
	return nil
}

func requireFreshSeriesPlatformPermission(
	ctx context.Context,
	tx *gorm.DB,
	checker seriesPermissionChecker,
	can policyv1.Can,
) error {
	principal := auth.GetUser(ctx)
	if principal == nil {
		return errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if !active {
		return errs.NoPermission(can.Action().Name(), can.Resource().Type())
	}
	return requireSeriesPlatformPermission(ctx, checker, can)
}

func (s *SeriesService) lookupSeriesResources(
	ctx context.Context,
	lookup policyv1.ResourceLookup,
) ([]string, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return nil, errs.AuthenticationRequired()
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return nil, errs.WithCause(errs.AuthenticationRequired(), err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, errs.WithCause(errs.AuthenticationRequired(), err)
	}
	if s.permissions == nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	ids, err := s.permissions.LookupResources(ctx, lookup, actor)
	if err != nil {
		return nil, errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	return ids, nil
}

func (s *SeriesService) requireSeriesViewAndLock(ctx context.Context, tx *gorm.DB, seriesID string) error {
	if err := s.requireSeriesPermissionAndLock(ctx, tx, seriesID, policyv1.PostSeries.View); err != nil {
		return err
	}
	return nil
}

func lockSeriesRoot(ctx context.Context, tx *gorm.DB, seriesID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("series").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		Where("id = ?", seriesID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("series", seriesID)
		}
		return errs.Internal(err)
	}
	return nil
}

func lockPostSeriesRelation(ctx context.Context, tx *gorm.DB, posts PostMembership, postID string) (*string, error) {
	seriesID, err := posts.LockSeriesRelation(ctx, tx, postID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.NotFound("post", postID)
	}
	if err != nil {
		return nil, errs.Internal(err)
	}
	return seriesID, nil
}

func lockSeriesOrderPosts(ctx context.Context, tx *gorm.DB, posts PostMembership, postIDs []string) error {
	locked, err := posts.LockSeriesPosts(ctx, tx, postIDs)
	if err != nil {
		return errs.Internal(err)
	}
	if locked != len(postIDs) {
		return errs.InvalidArgument("post_ids", "must be the exact set of posts currently assigned to the series")
	}
	return nil
}

func compactSeriesPostOrders(ctx context.Context, tx *gorm.DB, posts PostMembership, seriesID string, now time.Time) error {
	postIDs, err := posts.SeriesPostIDs(ctx, tx, seriesID)
	if err != nil {
		return errs.Internal(err)
	}
	if err := lockSeriesOrderPosts(ctx, tx, posts, postIDs); err != nil {
		return err
	}
	return orderSeriesPosts(ctx, tx, posts, seriesID, postIDs, now)
}

// orderSeriesPosts gives the Posts of seriesID the positions of postIDs.
func orderSeriesPosts(ctx context.Context, tx *gorm.DB, posts PostMembership, seriesID string, postIDs []string, now time.Time) error {
	return seriesRelationError(posts.OrderSeries(ctx, tx, seriesID, postIDs, now))
}

func seriesRelationError(err error) error {
	if errors.Is(err, ErrPostRelationChanged) {
		return errs.WithCause(errs.FailedPrecondition("post series relation changed; retry"), err)
	}
	if err != nil {
		return errs.Internal(err)
	}
	return nil
}

func validateSeriesPostOrder(postIDs []string) ([]string, error) {
	result := make([]string, len(postIDs))
	seen := make(map[string]struct{}, len(postIDs))
	for i, postID := range postIDs {
		if _, err := uuidutil.ParseCanonical(postID, "post_ids"); err != nil {
			return nil, errs.InvalidArgument("post_ids", "must contain canonical Post UUIDs")
		}
		if _, exists := seen[postID]; exists {
			return nil, errs.InvalidArgument("post_ids", "must not contain duplicates")
		}
		seen[postID] = struct{}{}
		result[i] = postID
	}
	return result, nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := values[value]; !ok {
			return false
		}
	}
	return true
}

func validateSeriesStatus(status string) error {
	switch status {
	case managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
		managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String():
		return nil
	default:
		return errs.InvalidArgument("status", "must be draft or published")
	}
}

func seriesSlugFromTitle(title string) string {
	var slug strings.Builder
	separator := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if separator && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(r)
			separator = false
			continue
		}
		separator = slug.Len() > 0
	}
	return slug.String()
}

func validateSeriesSlug(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", errs.Required("slug")
	}
	if utf8.RuneCountInString(slug) > 255 {
		return "", errs.InvalidArgument("slug", "must be at most 255 characters")
	}
	if strings.Contains(slug, "/") {
		return "", errs.InvalidArgument("slug", "must be a single route segment")
	}
	return slug, nil
}
