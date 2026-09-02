package member

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

const maxMemberUUIDGenerationAttempts = 16

// ErrMemberLinkRepairable marks an active identity that has not yet received
// its exact Member reverse link. The login lifecycle may retry the same
// identity provisioning once; malformed or conflicting links remain fail-closed.
var ErrMemberLinkRepairable = errors.New("identity/member link requires exact identity provisioning")

type RegistrationMemberInput struct {
	IdentityID      string
	Email           string
	PreferredLocale string
}

type memberProvisioningIdentity interface {
	auth.IdentityGetter
	auth.IdentityExternalIDWriter
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
	db       *gorm.DB
	identity memberProvisioningIdentity
	spicedb  *auth.SpiceDBClient
	emails   RegistrationAccountEmailProjection
	roles    registrationDirectRoleTransition
}

func NewMemberProvisioner(
	db *gorm.DB,
	identity memberProvisioningIdentity,
	spicedb *auth.SpiceDBClient,
	emails RegistrationAccountEmailProjection,
	roles registrationDirectRoleTransition,
) *MemberProvisioner {
	if db == nil {
		panic("db is required")
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
	return &MemberProvisioner{db: db, identity: identity, spicedb: spicedb, emails: emails, roles: roles}
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
	if _, ok := normalizeMemberEmailInput(email); !ok {
		return false, fmt.Errorf("registration email is invalid")
	}
	return RegistrationEmailReuseBlocked(ctx, s.db, email)
}

func (s *MemberProvisioner) ProvisionRegistration(ctx context.Context, raw RegistrationMemberInput) (*model.Member, error) {
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

func allocateMemberUUID(tx *gorm.DB, next func() string) (string, error) {
	if tx == nil || next == nil {
		return "", fmt.Errorf("member UUID allocator dependencies are required")
	}
	for range maxMemberUUIDGenerationAttempts {
		candidate := next()
		if _, err := uuidutil.ParseCanonical(candidate, "member_id"); err != nil {
			return "", err
		}
		var occupied bool
		if err := tx.Raw(`
			SELECT EXISTS (SELECT 1 FROM member WHERE id = ?::uuid)
			    OR EXISTS (SELECT 1 FROM member WHERE nickname = ?)
			    OR EXISTS (SELECT 1 FROM kratos.identities WHERE id = ?::uuid)
		`, candidate, candidate, candidate).Scan(&occupied).Error; err != nil {
			return "", err
		}
		if !occupied {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not allocate a collision-free member UUID")
}

func (s *MemberProvisioner) ValidateExistingLink(ctx context.Context, identity *auth.Identity) (*model.Member, error) {
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
	var member model.Member
	if err := s.db.WithContext(ctx).
		Where("id = ? AND account_identity_id = ? AND deleted_at IS NULL", identity.ExternalID, identity.ID).
		First(&member).Error; err != nil {
		return nil, fmt.Errorf("validate existing identity/member link: %w", err)
	}
	if err := s.emails.SyncMemberEmailProjection(ctx, s.db, s.identity, identity.ID, nil); err != nil {
		return nil, fmt.Errorf("sync login Member email projection: %w", err)
	}
	if err := s.db.WithContext(ctx).Where("id = ?::uuid", member.ID).Take(&member).Error; err != nil {
		return nil, fmt.Errorf("reload login Member: %w", err)
	}
	return &member, nil
}
