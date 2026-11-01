package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type accountSummaryRow struct {
	MemberID       string     `gorm:"column:member_id"`
	IdentityID     *string    `gorm:"column:identity_id"`
	Email          *string    `gorm:"column:email"`
	EmailVerified  bool       `gorm:"column:email_verified"`
	IdentityState  *string    `gorm:"column:identity_state"`
	MetadataBanned bool       `gorm:"column:metadata_banned"`
	BanReason      *string    `gorm:"column:ban_reason"`
	BanExpires     *time.Time `gorm:"column:ban_expires"`
	DeletedAt      *time.Time `gorm:"column:deleted_at"`
	PendingDelete  bool       `gorm:"column:pending_delete"`
}

func accountRoleProto(role policyv1.RoleID) policyv1.AuthorizationRole {
	switch role {
	case policyv1.Role.Admin():
		return policyv1.AuthorizationRole_ADMIN
	case policyv1.Role.Author():
		return policyv1.AuthorizationRole_AUTHOR
	default:
		return policyv1.AuthorizationRole_USER
	}
}

func accountRoleReplacementMutations(
	subject auth.AccountIdentitySubject,
	role policyv1.RoleID,
) ([]policyv1.RelationshipMutation, error) {
	if !role.Valid() {
		return nil, fmt.Errorf("unsupported account role %q", role.ID())
	}
	desiredRole := role
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, err
	}
	mutations := make([]policyv1.RelationshipMutation, 0, 3)
	for _, candidateRole := range []policyv1.RoleID{
		policyv1.Role.Admin(),
		policyv1.Role.Author(),
		policyv1.Role.User(),
	} {
		var (
			mutation policyv1.RelationshipMutation
			err      error
		)
		if candidateRole == desiredRole {
			mutation, err = policyv1.Role.TouchMember(candidateRole, actor)
		} else {
			mutation, err = policyv1.Role.DeleteMember(candidateRole, actor)
		}
		if err != nil {
			return nil, err
		}
		mutations = append(mutations, mutation)
	}
	return mutations, nil
}

func RoleReplacementMutations(subject auth.AccountIdentitySubject, role policyv1.RoleID) ([]policyv1.RelationshipMutation, error) {
	return accountRoleReplacementMutations(subject, role)
}

func accountRoleRemovalMutations(subject auth.AccountIdentitySubject) ([]policyv1.RelationshipMutation, error) {
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, err
	}
	mutations := make([]policyv1.RelationshipMutation, 0, 3)
	for _, role := range []policyv1.RoleID{
		policyv1.Role.Admin(),
		policyv1.Role.Author(),
		policyv1.Role.User(),
	} {
		mutation, err := policyv1.Role.DeleteMember(role, actor)
		if err != nil {
			return nil, err
		}
		mutations = append(mutations, mutation)
	}
	return mutations, nil
}

func accountRoleRestoreMutations(
	subject auth.AccountIdentitySubject,
	role policyv1.RoleID,
	found bool,
) ([]policyv1.RelationshipMutation, error) {
	if !found {
		return accountRoleRemovalMutations(subject)
	}
	return accountRoleReplacementMutations(subject, role)
}

func RoleRestoreMutations(subject auth.AccountIdentitySubject, role policyv1.RoleID, found bool) ([]policyv1.RelationshipMutation, error) {
	return accountRoleRestoreMutations(subject, role, found)
}

func accountSummaryFromRow(row accountSummaryRow, role policyv1.RoleID) *managev1.AccountSummary {
	summary := &managev1.AccountSummary{MemberId: row.MemberID, Role: accountRoleProto(role)}
	if row.DeletedAt != nil || row.IdentityID == nil {
		summary.Status = managev1.AccountStatus_ACCOUNT_STATUS_DELETED
		return summary
	}
	if row.Email != nil && strings.TrimSpace(*row.Email) != "" {
		summary.CanonicalEmail = &managev1.CanonicalEmailSummary{Email: strings.TrimSpace(*row.Email), Verified: row.EmailVerified}
	}
	inactive := valueOrEmpty(row.IdentityState) == auth.KratosStateInactive
	summary.Banned = row.MetadataBanned || inactive
	switch {
	case row.PendingDelete:
		summary.Status = managev1.AccountStatus_ACCOUNT_STATUS_PENDING_DELETION
	case summary.Banned:
		summary.Status = managev1.AccountStatus_ACCOUNT_STATUS_BANNED
	default:
		summary.Status = managev1.AccountStatus_ACCOUNT_STATUS_ACTIVE
	}
	if summary.Banned {
		summary.BanDetails = &managev1.AccountBanDetails{MetadataBanned: row.MetadataBanned, IdentityState: valueOrEmpty(row.IdentityState), InactiveState: inactive, Reason: row.BanReason}
		if row.BanExpires != nil {
			summary.BanDetails.ExpiresAt = timestamppb.New(*row.BanExpires)
		}
	}
	return summary
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func loadAccountSummaryRows(ctx context.Context, db *gorm.DB, memberIDs []string) ([]accountSummaryRow, error) {
	if len(memberIDs) == 0 {
		return nil, nil
	}
	var rows []accountSummaryRow
	if err := db.WithContext(ctx).Raw(`
		SELECT
			m.id::text AS member_id,
			i.id::text AS identity_id,
			NULLIF(btrim(i.traits ->> 'email'), '') AS email,
			`+identityread.EmailVerifiedSQL("i")+` AS email_verified,
			i.state::text AS identity_state,
			COALESCE((i.metadata_admin ->> 'banned')::boolean, false) AS metadata_banned,
			NULLIF(i.metadata_admin ->> 'ban_reason', '') AS ban_reason,
			CASE WHEN NULLIF(i.metadata_admin ->> 'ban_expires', '') IS NULL THEN NULL ELSE (i.metadata_admin ->> 'ban_expires')::timestamptz END AS ban_expires,
			m.deleted_at,
			EXISTS (
				SELECT 1 FROM user_deletion_request d
				WHERE d.member_id=m.id
				  AND d.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
				  AND d.scheduled_at IS NOT NULL
			) AS pending_delete
		FROM member AS m
		`+identityread.LeftJoinLinked("m", "i")+`
		WHERE m.id IN ?
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func accountSummariesForMembers(ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberIDs []string) (map[string]*managev1.AccountSummary, error) {
	result := make(map[string]*managev1.AccountSummary, len(memberIDs))
	rows, err := loadAccountSummaryRows(ctx, db, memberIDs)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return result, nil
	}
	roles, err := globalRolesForAccountIdentities(ctx, spicedb)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	for _, row := range rows {
		if row.IdentityID == nil {
			result[row.MemberID] = accountSummaryFromRow(row, policyv1.Role.User())
			continue
		}
		role, found := roles[*row.IdentityID]
		if !found {
			return nil, domainerr.Precondition("account identity has no global SpiceDB role")
		}
		result[row.MemberID] = accountSummaryFromRow(row, role)
	}
	return result, nil
}

func SummariesForMembers(ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberIDs []string) (map[string]*managev1.AccountSummary, error) {
	return accountSummariesForMembers(ctx, db, spicedb, memberIDs)
}

func accountSummaryForMember(ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberID string) (*managev1.AccountSummary, error) {
	rows, err := loadAccountSummaryRows(ctx, db, []string{memberID})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, domainerr.ResourceNotFound("member", memberID)
	}
	row := rows[0]
	if row.IdentityID == nil {
		return accountSummaryFromRow(row, policyv1.Role.User()), nil
	}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(*row.IdentityID))
	if err != nil {
		return nil, err
	}
	role, found, err := spicedb.ReadDirectGlobalRole(ctx, subject)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !found {
		return nil, domainerr.Precondition("account identity has no global SpiceDB role")
	}
	return accountSummaryFromRow(row, role), nil
}

func SummaryForMember(ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberID string) (*managev1.AccountSummary, error) {
	return accountSummaryForMember(ctx, db, spicedb, memberID)
}

func sessionSummaryForMember(ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberID string) (*managev1.AccountSummary, error) {
	var row accountSummaryRow
	result := db.WithContext(ctx).Raw(`
		SELECT
			m.id::text AS member_id,
			i.id::text AS identity_id,
			NULLIF(btrim(i.traits ->> 'email'), '') AS email,
			i.state::text AS identity_state,
			COALESCE((i.metadata_admin ->> 'banned')::boolean, false) AS metadata_banned,
			m.deleted_at,
			EXISTS (
				SELECT 1 FROM user_deletion_request d
				WHERE d.member_id=m.id
				  AND d.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
				  AND d.scheduled_at IS NOT NULL
			) AS pending_delete
		FROM member AS m
		`+identityread.LeftJoinLinked("m", "i")+`
		WHERE m.id = ?::uuid
	`, memberID).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, domainerr.ResourceNotFound("member", memberID)
	}
	role := policyv1.Role.User()
	if row.IdentityID != nil {
		subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(*row.IdentityID))
		if err != nil {
			return nil, err
		}
		resolved, found, err := spicedb.ReadDirectGlobalRole(ctx, subject)
		if err != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
		}
		if !found {
			return nil, domainerr.Precondition("account identity has no global SpiceDB role")
		}
		role = resolved
	}
	summary := accountSummaryFromRow(row, role)
	// The session boundary only consumes the canonical address, role and status.
	// Do not synthesize verification or ban-detail fields that it cannot expose.
	if summary.CanonicalEmail != nil {
		summary.CanonicalEmail.Verified = false
	}
	summary.Banned = false
	summary.BanDetails = nil
	return summary, nil
}

func SessionSummaryForMember(ctx context.Context, db *gorm.DB, spicedb *auth.SpiceDBClient, memberID string) (*managev1.AccountSummary, error) {
	return sessionSummaryForMember(ctx, db, spicedb, memberID)
}

func (s *Service) identityIDForMember(ctx context.Context, memberID string) (string, error) {
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, false)
	if err != nil {
		return "", err
	}
	return target.IdentityID, nil
}

func providerProto(identity *auth.Identity) []*managev1.AccountProvider {
	if identity == nil {
		return nil
	}
	providers := []*managev1.AccountProvider{}
	for _, provider := range auth.NewCredentialInventory(identity.Credentials).OIDCProviders() {
		providers = append(providers, &managev1.AccountProvider{Provider: provider.Provider, Identifier: provider.Subject})
	}
	return providers
}

func ProviderProto(identity *auth.Identity) []*managev1.AccountProvider {
	return providerProto(identity)
}

func sourceTypeProto(value string) managev1.AccountEmailSourceType {
	if value == string(model.AccountEmailSourceTypeEmailCode) {
		return managev1.AccountEmailSourceType_ACCOUNT_EMAIL_SOURCE_TYPE_EMAIL_CODE
	}
	if value == string(model.AccountEmailSourceTypeOIDCProvider) {
		return managev1.AccountEmailSourceType_ACCOUNT_EMAIL_SOURCE_TYPE_OIDC_PROVIDER
	}
	return managev1.AccountEmailSourceType_ACCOUNT_EMAIL_SOURCE_TYPE_IDENTITY_CURRENT
}

func SourceTypeProto(value string) managev1.AccountEmailSourceType { return sourceTypeProto(value) }
