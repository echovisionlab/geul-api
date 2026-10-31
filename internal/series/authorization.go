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
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
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

// requireSeriesPermissionAndLock locks the Series root and requires action
// on it with the principal fence require applies. A caller without it,
// inactive or denied, finds no Series.
func requireSeriesPermissionAndLock(
	ctx context.Context,
	tx *gorm.DB,
	require mutation.Require,
	seriesID string,
	action seriesAction,
) error {
	if err := lockSeriesRoot(ctx, tx, seriesID); err != nil {
		return err
	}
	principal := auth.GetUser(ctx)
	if principal == nil {
		return domainerr.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return err
	}
	if !active {
		return domainerr.ResourceNotFound("series", seriesID)
	}
	can, err := action(seriesID)
	if err != nil {
		return domainerr.ResourceNotFound("series", seriesID).WithCause(err)
	}
	if err := require(can); err != nil {
		if mutation.IsDenied(err) {
			return domainerr.ResourceNotFound("series", seriesID).WithCause(err)
		}
		return err
	}
	return nil
}

// lockedSeries is the access of a Series mutation: the root is locked
// before the one exact action is checked.
func lockedSeries(seriesID string, action seriesAction) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		return requireSeriesPermissionAndLock(ctx, tx, require, seriesID, action)
	})
}

func freshRequire(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient) mutation.Require {
	return func(can policyv1.Can) error {
		return identitystate.RequireFreshCan(ctx, tx, spiceDB, can)
	}
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
	return requireSeriesPermissionAndLock(ctx, tx, freshRequire(ctx, tx, spiceDB), seriesID, policyv1.PostSeries.View)
}

// RequireEditAndLockWithDB is the translation source/target mutation boundary
// for a concrete Series.
func RequireEditAndLockWithDB(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	seriesID string,
) error {
	return requireSeriesPermissionAndLock(ctx, tx, freshRequire(ctx, tx, spiceDB), seriesID, policyv1.PostSeries.Edit)
}

func checkSeriesPermission(
	ctx context.Context,
	checker seriesPermissionChecker,
	seriesID string,
	action seriesAction,
	principal *auth.UserInfo,
) error {
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	can, err := action(seriesID)
	if err != nil {
		return domainerr.ResourceNotFound("series", seriesID).WithCause(err)
	}
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return domainerr.AuthenticationRequired()
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.ResourceNotFound("series", seriesID)
	}
	return nil
}

func (s *Service) requireSeriesPermissionOrNotFound(
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
		return domainerr.AuthenticationRequired()
	}
	if checker == nil {
		return domainerr.DependencyUnavailable("SpiceDB")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.NoPermission(can.Action().Name(), can.Resource().Type())
	}
	return nil
}

func (s *Service) lookupSeriesResources(
	ctx context.Context,
	lookup policyv1.ResourceLookup,
) ([]string, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.IdentityID == "" || principal.MemberID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	subject, err := auth.NewAccountIdentitySubject(principal.IdentityID)
	if err != nil {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, domainerr.AuthenticationRequired().WithCause(err)
	}
	if s.permissions == nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB")
	}
	ids, err := s.permissions.LookupResources(ctx, lookup, actor)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return ids, nil
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
			return domainerr.ResourceNotFound("series", seriesID)
		}
		return err
	}
	return nil
}

func lockPostSeriesRelation(ctx context.Context, tx *gorm.DB, posts PostMembership, postID string) (*string, error) {
	seriesID, err := posts.LockSeriesRelation(ctx, tx, postID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domainerr.ResourceNotFound("post", postID)
	}
	if err != nil {
		return nil, err
	}
	return seriesID, nil
}

func lockSeriesOrderPosts(ctx context.Context, tx *gorm.DB, posts PostMembership, postIDs []string) error {
	locked, err := posts.LockSeriesPosts(ctx, tx, postIDs)
	if err != nil {
		return err
	}
	if locked != len(postIDs) {
		return domainerr.InvalidField("post_ids", "must be the exact set of posts currently assigned to the series")
	}
	return nil
}

func compactSeriesPostOrders(ctx context.Context, tx *gorm.DB, posts PostMembership, seriesID string, now time.Time) error {
	postIDs, err := posts.SeriesPostIDs(ctx, tx, seriesID)
	if err != nil {
		return err
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
		return domainerr.Precondition("post series relation changed; retry").WithCause(err)
	}
	if err != nil {
		return err
	}
	return nil
}

func validateSeriesPostOrder(postIDs []string) ([]string, error) {
	result := make([]string, len(postIDs))
	seen := make(map[string]struct{}, len(postIDs))
	for i, postID := range postIDs {
		if _, err := uuidutil.ParseCanonical(postID, "post_ids"); err != nil {
			return nil, domainerr.InvalidField("post_ids", "must contain canonical Post UUIDs")
		}
		if _, exists := seen[postID]; exists {
			return nil, domainerr.InvalidField("post_ids", "must not contain duplicates")
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
		return domainerr.InvalidField("status", "must be draft or published")
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
		return "", domainerr.Required("slug")
	}
	if utf8.RuneCountInString(slug) > 255 {
		return "", domainerr.InvalidField("slug", "must be at most 255 characters")
	}
	if strings.Contains(slug, "/") {
		return "", domainerr.InvalidField("slug", "must be a single route segment")
	}
	return slug, nil
}
