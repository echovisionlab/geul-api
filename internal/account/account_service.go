package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Status is the administrator-facing lifecycle state of an account.
type Status string

// The account statuses.
const (
	StatusActive          Status = "active"
	StatusBanned          Status = "banned"
	StatusPendingDeletion Status = "pending_deletion"
	StatusDeleted         Status = "deleted"
)

// Summary is the Account state an administrator or session view shows.
type Summary struct {
	MemberID       string
	Role           policyv1.AuthorizationRole
	Status         Status
	CanonicalEmail *CanonicalEmail
	Banned         bool
	BanDetails     *BanDetails
}

// CanonicalEmail is the canonical account email.
type CanonicalEmail struct {
	Email    string
	Verified bool
}

// BanDetails is why and until when an account is banned.
type BanDetails struct {
	MetadataBanned bool
	IdentityState  string
	InactiveState  bool
	Reason         *string
	ExpiresAt      *time.Time
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

func summaryFromRow(row SummaryRow, role policyv1.RoleID) *Summary {
	summary := &Summary{MemberID: row.MemberID, Role: accountRoleProto(role)}
	if row.DeletedAt != nil || row.IdentityID == nil {
		summary.Status = StatusDeleted
		return summary
	}
	if row.Email != nil && strings.TrimSpace(*row.Email) != "" {
		summary.CanonicalEmail = &CanonicalEmail{Email: strings.TrimSpace(*row.Email), Verified: row.EmailVerified}
	}
	inactive := valueOrEmpty(row.IdentityState) == auth.KratosStateInactive
	summary.Banned = row.MetadataBanned || inactive
	switch {
	case row.PendingDelete:
		summary.Status = StatusPendingDeletion
	case summary.Banned:
		summary.Status = StatusBanned
	default:
		summary.Status = StatusActive
	}
	if summary.Banned {
		summary.BanDetails = &BanDetails{MetadataBanned: row.MetadataBanned, IdentityState: valueOrEmpty(row.IdentityState), InactiveState: inactive, Reason: row.BanReason, ExpiresAt: row.BanExpires}
	}
	return summary
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Service) summaryRows(ctx context.Context, memberIDs []string) ([]SummaryRow, error) {
	if len(memberIDs) == 0 {
		return nil, nil
	}
	var rows []SummaryRow
	err := s.read(ctx, func(store Store) error {
		var err error
		rows, err = store.AccountSummaries(ctx, memberIDs)
		return err
	})
	return rows, err
}

// Summaries returns the Account summary of each Member in memberIDs that
// exists.
func (s *Service) Summaries(ctx context.Context, memberIDs []string) (map[string]*Summary, error) {
	result := make(map[string]*Summary, len(memberIDs))
	rows, err := s.summaryRows(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return result, nil
	}
	roles, err := globalRolesForAccountIdentities(ctx, s.spicedb)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	for _, row := range rows {
		if row.IdentityID == nil {
			result[row.MemberID] = summaryFromRow(row, policyv1.Role.User())
			continue
		}
		role, found := roles[*row.IdentityID]
		if !found {
			return nil, domainerr.Precondition("account identity has no global SpiceDB role")
		}
		result[row.MemberID] = summaryFromRow(row, role)
	}
	return result, nil
}

// directRole reads the direct global role of a linked row; an unlinked row
// is a user.
func (s *Service) directRole(ctx context.Context, row SummaryRow) (policyv1.RoleID, error) {
	if row.IdentityID == nil {
		return policyv1.Role.User(), nil
	}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(*row.IdentityID))
	if err != nil {
		return policyv1.RoleID{}, err
	}
	role, found, err := s.spicedb.ReadDirectGlobalRole(ctx, subject)
	if err != nil {
		return policyv1.RoleID{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !found {
		return policyv1.RoleID{}, domainerr.Precondition("account identity has no global SpiceDB role")
	}
	return role, nil
}

// Summary returns the Account summary of one Member.
func (s *Service) Summary(ctx context.Context, memberID string) (*Summary, error) {
	rows, err := s.summaryRows(ctx, []string{memberID})
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, domainerr.ResourceNotFound("member", memberID)
	}
	role, err := s.directRole(ctx, rows[0])
	if err != nil {
		return nil, err
	}
	return summaryFromRow(rows[0], role), nil
}

// SessionSummary returns the lean Account summary of the session view: the
// canonical address, role and status only.
func (s *Service) SessionSummary(ctx context.Context, memberID string) (*Summary, error) {
	var (
		row   SummaryRow
		found bool
	)
	if err := s.read(ctx, func(store Store) error {
		var err error
		row, found, err = store.SessionSummary(ctx, memberID)
		return err
	}); err != nil {
		return nil, err
	}
	if !found {
		return nil, domainerr.ResourceNotFound("member", memberID)
	}
	role, err := s.directRole(ctx, row)
	if err != nil {
		return nil, err
	}
	summary := summaryFromRow(row, role)
	// The session boundary only consumes the canonical address, role and status.
	// Do not synthesize verification or ban-detail fields that it cannot expose.
	if summary.CanonicalEmail != nil {
		summary.CanonicalEmail.Verified = false
	}
	summary.Banned = false
	summary.BanDetails = nil
	return summary, nil
}

func (s *Service) identityIDForMember(ctx context.Context, memberID string) (string, error) {
	var target Target
	err := s.read(ctx, func(store Store) error {
		var err error
		target, err = store.RequireLinkedMember(ctx, memberID, false)
		return err
	})
	return target.IdentityID, err
}

// Provider is one linked social sign-in.
type Provider struct {
	Provider   string
	Identifier string
}

// Providers lists the social sign-ins of an Identity read with its OIDC
// credentials.
func Providers(identity *auth.Identity) []Provider {
	if identity == nil {
		return nil
	}
	providers := []Provider{}
	for _, provider := range auth.NewCredentialInventory(identity.Credentials).OIDCProviders() {
		providers = append(providers, Provider{Provider: provider.Provider, Identifier: provider.Subject})
	}
	return providers
}
