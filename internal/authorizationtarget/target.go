// Package authorizationtarget resolves Member/Identity pairs that are safe to
// use as authorization principals and protects those pairs during mutations.
package authorizationtarget

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrIneligible = errors.New("member is not eligible for authorization")

var deletionPendingStates = []string{
	"scheduled",
	"recovery_confirmation_pending",
}

// Target is the exact linked Member and Kratos Identity pair.
type Target struct {
	MemberID   string `gorm:"column:member_id"`
	IdentityID string `gorm:"column:identity_id"`
	Onboarded  bool   `gorm:"column:onboarded"`
}

// RequireAuthenticatedPrincipal returns the request principal only when the
// gateway-authenticated account has an active Member and is not banned. It is
// shared so Account and Member expose identical Connect failure contracts.
func RequireAuthenticatedPrincipal(ctx context.Context) (*auth.UserInfo, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.MemberID == "" || principal.IdentityID == "" {
		return nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return nil, errs.AccountBanned()
	}
	return principal, nil
}

// RequireGlobalAdmin applies the shared authenticated-principal boundary and
// then checks the current global administrator permission in SpiceDB.
func RequireGlobalAdmin(ctx context.Context, spicedb *auth.SpiceDBClient) (*auth.UserInfo, error) {
	principal, err := RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	can, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return nil, errs.Internal(err)
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return nil, errs.InvalidSession()
	}
	allowed, err := spicedb.Can(ctx, decision)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return nil, errs.AdminRequired()
	}
	return principal, nil
}

// Reference identifies a durable relation whose Member target must be locked.
type Reference struct {
	MemberID string
	Field    string
}

// Require returns an active, onboarded target and maps resolution failures to
// the Connect errors used by service request boundaries.
func Require(ctx context.Context, db *gorm.DB, memberID string) (Target, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return Target{}, errs.InvalidArgument("member_id", "must be a canonical UUID")
	}
	target, err := ForMember(db.WithContext(ctx), memberID)
	if err == nil {
		return target, nil
	}
	if errors.Is(err, ErrIneligible) {
		return Target{}, errs.NotFound("member", memberID)
	}
	return Target{}, errs.Internal(err)
}

// RequireLocked keeps an active, onboarded target's exact identity link and
// role state stable until the caller's transaction commits.
func RequireLocked(ctx context.Context, tx *gorm.DB, memberID string) (Target, error) {
	target, err := Require(ctx, tx, memberID)
	if err != nil {
		return Target{}, err
	}
	if err := identitystate.Lock(tx, target.IdentityID); err != nil {
		return Target{}, errs.Internal(err)
	}

	var member struct {
		ID string `gorm:"column:id"`
	}
	result := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id").
		Where("id = ?::uuid AND account_identity_id = ?::uuid", memberID, target.IdentityID).
		Take(&member)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return Target{}, errs.NotFound("member", memberID)
	}
	if result.Error != nil {
		return Target{}, errs.Internal(result.Error)
	}

	var identity struct {
		ID string `gorm:"column:id"`
	}
	result = tx.WithContext(ctx).
		Table("kratos.identities").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id").
		Where("id = ?::uuid AND external_id = ?", target.IdentityID, target.MemberID).
		Take(&identity)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return Target{}, errs.NotFound("member", memberID)
	}
	if result.Error != nil {
		return Target{}, errs.Internal(result.Error)
	}

	current, err := Require(ctx, tx, memberID)
	if err != nil {
		return Target{}, err
	}
	if current.IdentityID != target.IdentityID {
		return Target{}, errs.NotFound("member", memberID)
	}
	return current, nil
}

// RequireLockedLinked keeps a linked, non-deleted target stable while an
// authorization relationship is being removed. It deliberately permits an
// inactive or banned identity because no new authority is granted.
func RequireLockedLinked(ctx context.Context, tx *gorm.DB, memberID string) (Target, error) {
	target, err := RequireLinkedMember(ctx, tx, memberID, false)
	if err != nil {
		return Target{}, err
	}
	if err := identitystate.Lock(tx, target.IdentityID); err != nil {
		return Target{}, errs.Internal(err)
	}
	current, err := RequireLinkedMember(ctx, tx, memberID, false)
	if err != nil {
		return Target{}, err
	}
	if current.IdentityID != target.IdentityID {
		return Target{}, errs.NotFound("member", memberID)
	}
	return current, nil
}

// WithMutation holds the identity mutation fence and verifies that the target
// did not change before invoking work.
func WithMutation(
	ctx context.Context,
	db *gorm.DB,
	memberID string,
	work func(context.Context, *gorm.DB) error,
) error {
	target, err := Require(ctx, db, memberID)
	if err != nil {
		return err
	}
	return identitystate.WithMutation(ctx, db, target.IdentityID, func(
		mutationCtx context.Context,
		connection *gorm.DB,
	) error {
		current, err := ForMember(connection, memberID)
		if err != nil {
			if errors.Is(err, ErrIneligible) {
				return errs.NotFound("member", memberID)
			}
			return errs.Internal(err)
		}
		if current.IdentityID != target.IdentityID {
			return errs.NotFound("member", memberID)
		}
		return work(mutationCtx, connection)
	})
}

// ForMember returns only an active, non-banned, onboarded target whose exact
// bilateral Member/Identity link is live.
func ForMember(db *gorm.DB, memberID string) (Target, error) {
	return forMemberState(db, memberID, true)
}

// LiveForMember returns an active, non-banned target regardless of onboarding.
func LiveForMember(db *gorm.DB, memberID string) (Target, error) {
	return forMemberState(db, memberID, false)
}

// ActiveForIdentity returns the active, non-banned, non-deleted Member linked
// bilaterally to identityID. It does not require onboarding because Account
// owns lifecycle operations for both onboarding states.
func ActiveForIdentity(db *gorm.DB, identityID string) (Target, error) {
	if db == nil {
		return Target{}, fmt.Errorf("authorization target database is required")
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return Target{}, err
	}
	var target Target
	if err := db.Raw(`
		SELECT member.id::text AS member_id,
		       identity.id::text AS identity_id,
		       member.onboarded
		FROM kratos.identities AS identity
		JOIN member
		  ON member.id::text = identity.external_id
		 AND member.account_identity_id = identity.id
		WHERE identity.id = ?::uuid
		  AND identity.state = 'active'
		  AND NOT COALESCE((identity.metadata_admin->>'banned')::boolean, false)
		  AND member.deleted_at IS NULL
	`, identityID).Scan(&target).Error; err != nil {
		return Target{}, err
	}
	return validatedTarget(target)
}

// ActiveMemberIDForIdentity is the canonical reverse lookup for callers that
// have an identity but need its active, non-banned Member. Missing or invalid
// links retain the historic gorm.ErrRecordNotFound contract.
func ActiveMemberIDForIdentity(ctx context.Context, db *gorm.DB, identityID string) (string, error) {
	target, err := ActiveForIdentity(db.WithContext(ctx), identityID)
	if errors.Is(err, ErrIneligible) {
		return "", gorm.ErrRecordNotFound
	}
	if err != nil {
		return "", err
	}
	return target.MemberID, nil
}

// ActiveOnboardedMemberForIdentity resolves the exact active bilateral pair
// for recipient work that requires a completed Member onboarding projection.
// Missing, inactive, banned, deleted, malformed, or unonboarded pairs retain
// the historic gorm.ErrRecordNotFound contract.
func ActiveOnboardedMemberForIdentity(ctx context.Context, db *gorm.DB, identityID string) (Target, error) {
	target, err := ActiveForIdentity(db.WithContext(ctx), identityID)
	if errors.Is(err, ErrIneligible) {
		return Target{}, gorm.ErrRecordNotFound
	}
	if err != nil {
		return Target{}, err
	}
	if !target.Onboarded {
		return Target{}, gorm.ErrRecordNotFound
	}
	return target, nil
}

// LockActivePairForCredentialMutation preserves the legacy credential-hook
// lock order: the caller has already acquired the Identity mutation fence,
// then this takes an UPDATE lock on the active, onboarded Member row while
// proving both pointers still agree. Resolution failures are gorm.ErrRecordNotFound.
func LockActivePairForCredentialMutation(ctx context.Context, tx *gorm.DB, memberID, identityID string) error {
	if tx == nil {
		return fmt.Errorf("authorization target transaction is required")
	}
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return gorm.ErrRecordNotFound
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return gorm.ErrRecordNotFound
	}
	if memberID == identityID {
		return gorm.ErrRecordNotFound
	}
	var member struct {
		ID string `gorm:"column:id"`
	}
	result := tx.WithContext(ctx).
		Table("public.member AS member").
		Joins("JOIN kratos.identities AS identity ON identity.id = member.account_identity_id AND identity.external_id = member.id::text").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "member"}}).
		Select("member.id::text AS id").
		Where(`member.id = ?::uuid
			AND identity.id = ?::uuid
			AND member.onboarded = TRUE
			AND member.deleted_at IS NULL
			AND identity.state = 'active'
			AND NOT COALESCE((identity.metadata_admin->>'banned')::boolean, false)`, memberID, identityID).
		Take(&member)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return gorm.ErrRecordNotFound
	}
	if result.Error != nil {
		return result.Error
	}
	if member.ID != memberID {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ValidateActivePair verifies that memberID and identityID are the same
// active, non-banned bilateral link. Callers that accept both IDs use this
// instead of independently reimplementing the link query.
func ValidateActivePair(ctx context.Context, db *gorm.DB, memberID, identityID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	if memberID == identityID {
		return fmt.Errorf("identity_id and member_id must be distinct")
	}
	target, err := ActiveForIdentity(db.WithContext(ctx), identityID)
	if errors.Is(err, ErrIneligible) {
		return gorm.ErrRecordNotFound
	}
	if err != nil {
		return err
	}
	if target.MemberID != memberID {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func forMemberState(db *gorm.DB, memberID string, requireOnboarded bool) (Target, error) {
	var target Target
	if db == nil {
		return target, fmt.Errorf("authorization target database is required")
	}
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return target, err
	}
	if err := db.Raw(`
		SELECT member.id::text AS member_id,
		       identity.id::text AS identity_id,
		       member.onboarded
		FROM member
		JOIN kratos.identities AS identity
		  ON identity.id = member.account_identity_id
		 AND identity.external_id = member.id::text
		WHERE member.id = ?::uuid
		  AND member.deleted_at IS NULL
		  AND (? = FALSE OR member.onboarded = TRUE)
		  AND identity.state = 'active'
		  AND NOT COALESCE((identity.metadata_admin->>'banned')::boolean, false)
		  AND NOT EXISTS (
		      SELECT 1
		      FROM user_deletion_request AS request
		      WHERE request.member_id = member.id
		        AND request.identity_id = identity.id
		        AND request.lifecycle_state IN ?
		  )
	`, memberID, requireOnboarded, deletionPendingStates).Scan(&target).Error; err != nil {
		return Target{}, err
	}
	return validatedTarget(target)
}

// RequireLive resolves an active target regardless of onboarding and maps
// resolution failures to the Connect errors used by service request boundaries.
func RequireLive(ctx context.Context, db *gorm.DB, memberID string) (Target, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return Target{}, errs.InvalidArgument("member_id", "must be a canonical UUID")
	}
	target, err := LiveForMember(db.WithContext(ctx), memberID)
	if err == nil {
		return target, nil
	}
	if errors.Is(err, ErrIneligible) {
		return Target{}, errs.NotFound("member", memberID)
	}
	return Target{}, errs.Internal(err)
}

// LinkedMemberForMember returns a non-deleted target with an exact bilateral
// Member/Identity link, optionally requiring onboarding and never requiring
// current authority.
func LinkedMemberForMember(db *gorm.DB, memberID string, requireOnboarded bool) (Target, error) {
	var target Target
	if db == nil {
		return target, fmt.Errorf("member target database is required")
	}
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return target, err
	}
	if err := db.Raw(`
		SELECT member.id::text AS member_id,
		       identity.id::text AS identity_id,
		       member.onboarded
		FROM member
		JOIN kratos.identities AS identity
		  ON identity.id = member.account_identity_id
		 AND identity.external_id = member.id::text
		WHERE member.id = ?::uuid
		  AND member.deleted_at IS NULL
		  AND (? = FALSE OR member.onboarded = TRUE)
	`, memberID, requireOnboarded).Scan(&target).Error; err != nil {
		return Target{}, err
	}
	return validatedTarget(target)
}

func validatedTarget(target Target) (Target, error) {
	if target.MemberID == "" || target.IdentityID == "" {
		return Target{}, ErrIneligible
	}
	if target.MemberID == target.IdentityID {
		return Target{}, ErrIneligible
	}
	return target, nil
}

// RequireLinkedMember maps a linked target lookup to the Connect errors used
// by service request boundaries.
func RequireLinkedMember(
	ctx context.Context,
	db *gorm.DB,
	memberID string,
	requireOnboarded bool,
) (Target, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return Target{}, errs.InvalidArgument("member_id", "must be a canonical UUID")
	}
	target, err := LinkedMemberForMember(db.WithContext(ctx), memberID, requireOnboarded)
	if err == nil {
		return target, nil
	}
	if errors.Is(err, ErrIneligible) {
		return Target{}, errs.NotFound("member", memberID)
	}
	return Target{}, errs.Internal(err)
}

// EligibleMemberIDs returns onboarded, non-deleted members with an exact
// linked Identity for durable references; temporary identity state is ignored.
func EligibleMemberIDs(ctx context.Context, db *gorm.DB, memberIDs []string) ([]string, error) {
	if len(memberIDs) == 0 {
		return []string{}, nil
	}
	for _, memberID := range memberIDs {
		if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
			return nil, err
		}
	}
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).Raw(`
		SELECT member.id::text AS id
		FROM member
		JOIN kratos.identities AS identity
		  ON identity.id = member.account_identity_id
		 AND identity.external_id = member.id::text
		WHERE member.id IN ?
		  AND member.deleted_at IS NULL
		  AND member.onboarded = TRUE
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.ID)
	}
	return result, nil
}

// LockReferences protects durable relations from racing identity deletion
// without treating a temporary ban or inactive state as a reason to reject an
// already onboarded target.
func LockReferences(ctx context.Context, tx *gorm.DB, references []Reference) error {
	fieldsByMember := make(map[string]string, len(references))
	memberIDs := make([]string, 0, len(references))
	for _, reference := range references {
		if _, exists := fieldsByMember[reference.MemberID]; exists {
			continue
		}
		fieldsByMember[reference.MemberID] = reference.Field
		memberIDs = append(memberIDs, reference.MemberID)
	}
	sort.Strings(memberIDs)

	for _, memberID := range memberIDs {
		field := fieldsByMember[memberID]
		if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
			return errs.InvalidArgument(field, "must be a canonical UUID")
		}
		target, err := LinkedMemberForMember(tx.WithContext(ctx), memberID, true)
		if err != nil {
			if errors.Is(err, ErrIneligible) {
				return errs.InvalidArgument(field, fmt.Sprintf("not found: %s", memberID))
			}
			return errs.Internal(err)
		}
		if err := identitystate.Lock(tx, target.IdentityID); err != nil {
			return errs.Internal(err)
		}
		current, err := LinkedMemberForMember(tx.WithContext(ctx), memberID, true)
		if err != nil {
			if errors.Is(err, ErrIneligible) {
				return errs.InvalidArgument(field, fmt.Sprintf("not found: %s", memberID))
			}
			return errs.Internal(err)
		}
		if current.IdentityID != target.IdentityID {
			return errs.InvalidArgument(field, fmt.Sprintf("not found: %s", memberID))
		}
	}
	return nil
}
