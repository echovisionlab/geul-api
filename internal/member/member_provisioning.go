package member

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const maxMemberUUIDGenerationAttempts = 16

// ErrMemberLinkRepairable marks an active identity that has not yet received
// its exact Member reverse link. The login lifecycle may retry the same
// identity provisioning once; malformed or conflicting links remain fail-closed.
var ErrMemberLinkRepairable = errors.New("identity/member link requires exact identity provisioning")

var errRegistrationMemberAlreadyExists = errors.New("registration member already exists")

type RegistrationMemberInput struct {
	IdentityID      string
	Email           string
	PreferredLocale string
}

// ProvisioningIdentity is the Kratos admin API registration reads and links.
type ProvisioningIdentity interface {
	auth.IdentityGetter
	auth.IdentityExternalIDWriter
}

// ProvisioningAuthorizer is the SpiceDB port of registration: the direct
// role read and the relationship batch of the new Member.
type ProvisioningAuthorizer interface {
	authzmutation.RelationshipWriter
	ReadDirectGlobalRole(ctx context.Context, subject auth.AccountIdentitySubject) (policyv1.RoleID, bool, error)
}

// RegistrationAccountEmailProjection is the narrow Account-owned projection
// Member needs while provisioning the bilateral identity link.
type RegistrationAccountEmailProjection interface {
	PrepareRegistration(
		ctx context.Context, identity auth.IdentityGetter, identityID, email string,
	) (*auth.Identity, string, []string, error)
	// SyncMemberEmailProjection converges the Member email projection of
	// identityID in its own transaction; a nil identity is read from Kratos.
	SyncMemberEmailProjection(ctx context.Context, identityID string, identity *auth.Identity) error
}

type registrationDirectRoleTransition interface {
	Transition(
		auth.AccountIdentitySubject,
		policyv1.RoleID,
		policyv1.RoleID,
		bool,
	) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error)
}

type MemberProvisioner struct {
	units    mutation.UnitOfWork[Store]
	identity ProvisioningIdentity
	spicedb  ProvisioningAuthorizer
	emails   RegistrationAccountEmailProjection
	roles    registrationDirectRoleTransition
}

func NewMemberProvisioner(
	units mutation.UnitOfWork[Store],
	identity ProvisioningIdentity,
	spicedb ProvisioningAuthorizer,
	emails RegistrationAccountEmailProjection,
	roles registrationDirectRoleTransition,
) *MemberProvisioner {
	if units == nil {
		panic("member units are required")
	}
	if identity == nil {
		panic("identity external-id manager is required")
	}
	if spicedb == nil {
		panic("SpiceDB relationship authority is required")
	}
	if emails == nil {
		panic("account email projection is required")
	}
	if roles == nil {
		panic("direct role transition is required")
	}
	return &MemberProvisioner{units: units, identity: identity, spicedb: spicedb, emails: emails, roles: roles}
}

func normalizeRegistrationMemberInput(input RegistrationMemberInput) (RegistrationMemberInput, error) {
	if _, err := uuidutil.ParseCanonical(input.IdentityID, "identity_id"); err != nil {
		return RegistrationMemberInput{}, err
	}
	input.Email = strings.TrimSpace(input.Email)
	input.PreferredLocale = strings.TrimSpace(input.PreferredLocale)
	normalized := localization.NormalizeSupportedLocale(input.PreferredLocale)
	if normalized == nil {
		return RegistrationMemberInput{}, fmt.Errorf("preferred_locale must be a supported locale")
	}
	input.PreferredLocale = *normalized
	return input, nil
}

func (s *MemberProvisioner) registrationReuseBlocked(ctx context.Context, email string) (bool, error) {
	normalized, ok := NormalizeEmailInput(email)
	if !ok {
		return false, fmt.Errorf("registration email is invalid")
	}
	var blocked bool
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		blocked, err = store.EmailReuseHeld(ctx, normalized)
		return err
	})
	return blocked, err
}

func (s *MemberProvisioner) ProvisionRegistration(ctx context.Context, raw RegistrationMemberInput) (*Member, error) {
	plan, err := s.prepareRegistrationProvisioning(ctx, raw)
	if err != nil {
		return nil, err
	}
	member, err := s.persistRegistrationMember(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("provision member row: %w", err)
	}
	return s.convergeRegistrationMemberLink(ctx, plan, member)
}

func allocateMemberUUID(ctx context.Context, store Store, next func() string) (string, error) {
	if store == nil || next == nil {
		return "", fmt.Errorf("member UUID allocator dependencies are required")
	}
	for range maxMemberUUIDGenerationAttempts {
		candidate := next()
		if _, err := uuidutil.ParseCanonical(candidate, "member_id"); err != nil {
			return "", err
		}
		occupied, err := store.MemberIDOccupied(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !occupied {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not allocate a collision-free member UUID")
}

func (s *MemberProvisioner) ValidateExistingLink(ctx context.Context, identity *auth.Identity) (*Member, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity is required")
	}
	if _, err := uuidutil.ParseCanonical(identity.ID, "identity_id"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(identity.ExternalID) == "" {
		return nil, fmt.Errorf("%w: identity.external_id is empty", ErrMemberLinkRepairable)
	}
	if _, err := uuidutil.ParseCanonical(identity.ExternalID, "identity.external_id"); err != nil {
		return nil, err
	}
	if identity.ID == identity.ExternalID {
		return nil, fmt.Errorf("identity_id and member_id must be distinct")
	}
	var member Member
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		member, err = store.LinkedMember(ctx, identity.ExternalID, identity.ID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("validate existing identity/member link: %w", err)
	}
	if err := s.emails.SyncMemberEmailProjection(ctx, identity.ID, nil); err != nil {
		return nil, fmt.Errorf("sync login Member email projection: %w", err)
	}
	reloaded, err := s.reload(ctx, member.ID)
	if err != nil {
		return nil, fmt.Errorf("reload login Member: %w", err)
	}
	return &reloaded, nil
}

func (s *MemberProvisioner) reload(ctx context.Context, memberID string) (Member, error) {
	var member Member
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		member, err = store.MemberByID(ctx, memberID)
		return err
	})
	return member, err
}

type registrationProvisioningPlan struct {
	input           RegistrationMemberInput
	identity        *auth.Identity
	primaryEmail    string
	availableEmails []string
}

func (s *MemberProvisioner) prepareRegistrationProvisioning(
	ctx context.Context,
	raw RegistrationMemberInput,
) (registrationProvisioningPlan, error) {
	plan := registrationProvisioningPlan{}
	input, err := normalizeRegistrationMemberInput(raw)
	if err != nil {
		return plan, err
	}
	plan.input = input
	blocked, err := s.registrationReuseBlocked(ctx, input.Email)
	if err != nil {
		return plan, fmt.Errorf("check registration email reuse hold: %w", err)
	}
	if blocked {
		return plan, fmt.Errorf("registration cannot be completed")
	}
	plan.identity, plan.primaryEmail, plan.availableEmails, err = s.emails.PrepareRegistration(
		ctx, s.identity, input.IdentityID, input.Email,
	)
	if err != nil {
		return plan, fmt.Errorf("prepare account email projection for registration: %w", err)
	}
	return plan, nil
}

func (s *MemberProvisioner) persistRegistrationMember(
	ctx context.Context,
	plan registrationProvisioningPlan,
) (Member, error) {
	var member Member
	err := s.units.RelationshipTransaction(ctx, s.spicedb, func(
		tx mutation.Tx[Store],
		write authzmutation.WriteRelationships,
	) error {
		store := tx.Store()
		if err := store.LockRegistrationIdentity(ctx, plan.input.IdentityID); err != nil {
			return err
		}
		found, err := loadRegistrationMember(ctx, store, plan, &member)
		if err != nil {
			return err
		}
		if found {
			return errRegistrationMemberAlreadyExists
		}
		if member, err = createRegistrationMember(ctx, store, plan); err != nil {
			return err
		}
		memberPolicy, err := policyv1.Member.TouchPolicy(member.ID)
		if err != nil {
			return fmt.Errorf("build Member policy relationship: %w", err)
		}
		removeMemberPolicy, err := policyv1.Member.DeletePolicy(member.ID)
		if err != nil {
			return fmt.Errorf("build inverse Member policy relationship: %w", err)
		}
		subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(plan.input.IdentityID))
		if err != nil {
			return err
		}
		previousRole, previousRoleFound, err := s.spicedb.ReadDirectGlobalRole(ctx, subject)
		if err != nil {
			return fmt.Errorf("read registration account role: %w", err)
		}
		roleMutations, inverseRoleMutations, err := s.roles.Transition(
			subject, policyv1.Role.User(), previousRole, previousRoleFound,
		)
		if err != nil {
			return fmt.Errorf("build default account role transition: %w", err)
		}
		return write(
			append([]policyv1.RelationshipMutation{memberPolicy}, roleMutations...),
			append([]policyv1.RelationshipMutation{removeMemberPolicy}, inverseRoleMutations...),
		)
	})
	if errors.Is(err, errRegistrationMemberAlreadyExists) {
		return member, nil
	}
	if err != nil {
		return member, err
	}
	return member, nil
}

func loadRegistrationMember(
	ctx context.Context,
	store Store,
	plan registrationProvisioningPlan,
	member *Member,
) (bool, error) {
	current, found, err := store.LockRegistrationMember(ctx, plan.input.IdentityID)
	if err != nil {
		return false, err
	}
	if found {
		*member = current
		if member.DeletedAt != nil {
			return true, fmt.Errorf("registration identity points to a deleted member")
		}
		if plan.identity.ExternalID != "" && plan.identity.ExternalID != member.ID {
			return true, fmt.Errorf("identity.external_id conflicts with the member reverse pointer")
		}
		return true, nil
	}
	if plan.identity.ExternalID != "" {
		return false, fmt.Errorf("identity.external_id conflicts with the member reverse pointer")
	}
	return false, nil
}

func createRegistrationMember(
	ctx context.Context,
	store Store,
	plan registrationProvisioningPlan,
) (Member, error) {
	candidate, err := allocateMemberUUID(ctx, store, uuid.NewString)
	if err != nil {
		return Member{}, err
	}
	identityID := plan.input.IdentityID
	preferredLocale := plan.input.PreferredLocale
	primaryEmail := plan.primaryEmail
	return store.CreateRegistrationMember(ctx, Member{
		ID:                candidate,
		AccountIdentityID: &identityID,
		Nickname:          candidate,
		Onboarded:         false,
		PrimaryEmail:      &primaryEmail,
		AvailableEmails:   plan.availableEmails,
		SocialLinks:       map[string]string{},
		PreferredLocale:   &preferredLocale,
	})
}

func (s *MemberProvisioner) convergeRegistrationMemberLink(
	ctx context.Context,
	plan registrationProvisioningPlan,
	member Member,
) (*Member, error) {
	if _, err := uuidutil.ParseCanonical(member.ID, "member_id"); err != nil {
		return nil, err
	}
	if member.ID == plan.input.IdentityID {
		return nil, fmt.Errorf("identity_id and member_id must be distinct")
	}
	if plan.identity.ExternalID == "" {
		if err := s.identity.UpdateIdentityExternalID(
			ctx, plan.input.IdentityID, member.ID,
		); err != nil {
			return nil, fmt.Errorf("set identity external_id: %w", err)
		}
	}
	verifiedIdentity, err := s.loadConvergedRegistrationIdentity(ctx, plan.input.IdentityID, member.ID)
	if err != nil {
		return nil, err
	}
	if err := s.verifyRegistrationMemberReverseLink(ctx, member.ID, plan.input.IdentityID); err != nil {
		return nil, err
	}
	if err := s.emails.SyncMemberEmailProjection(ctx, plan.input.IdentityID, verifiedIdentity); err != nil {
		return nil, fmt.Errorf("sync initial Member email projection: %w", err)
	}
	reloaded, err := s.reload(ctx, member.ID)
	if err != nil {
		return nil, fmt.Errorf("reload provisioned Member: %w", err)
	}
	return &reloaded, nil
}

func (s *MemberProvisioner) loadConvergedRegistrationIdentity(
	ctx context.Context,
	identityID string,
	memberID string,
) (*auth.Identity, error) {
	identity, err := loadMemberIdentityWithEmailCredentials(ctx, s.identity, identityID)
	if err != nil || identity == nil {
		if err == nil {
			err = fmt.Errorf("identity was not returned")
		}
		return nil, fmt.Errorf("verify identity external_id: %w", err)
	}
	if identity.ID != identityID || identity.ExternalID != memberID {
		return nil, fmt.Errorf("identity/member bilateral link did not converge")
	}
	return identity, nil
}

func (s *MemberProvisioner) verifyRegistrationMemberReverseLink(
	ctx context.Context,
	memberID string,
	identityID string,
) error {
	var linked bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		linked, err = store.LinkedMemberExists(ctx, memberID, identityID)
		return err
	}); err != nil {
		return fmt.Errorf("verify member reverse pointer: %w", err)
	}
	if !linked {
		return fmt.Errorf("identity/member reverse pointer did not converge")
	}
	return nil
}

func loadMemberIdentityWithEmailCredentials(
	ctx context.Context, identity auth.IdentityGetter, identityID string,
) (*auth.Identity, error) {
	if identity == nil {
		return nil, fmt.Errorf("identity manager is required")
	}
	var result *auth.Identity
	for _, credentialType := range []string{"code", "oidc"} {
		candidate, err := identity.GetIdentityWithIncludeCredential(ctx, identityID, credentialType)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			continue
		}
		if result == nil {
			result = candidate
			if result.Credentials == nil {
				result.Credentials = map[string]auth.Credential{}
			}
		}
		if credential, ok := candidate.Credentials[strings.TrimSpace(credentialType)]; ok {
			result.Credentials[credentialType] = credential
		}
	}
	return result, nil
}
