package member

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var errRegistrationMemberAlreadyExists = errors.New("registration member already exists")

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
) (model.Member, error) {
	var member model.Member
	var subject auth.AccountIdentitySubject
	_, err := authzmutation.Execute(ctx, s.db, s.spicedb, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		if err := lockRegistrationIdentityNamespace(tx, plan.input.IdentityID); err != nil {
			return err
		}
		found, err := loadRegistrationMember(tx, plan, &member)
		if err != nil {
			return err
		}
		if found {
			return errRegistrationMemberAlreadyExists
		}
		if err := createRegistrationMember(tx, plan, &member); err != nil {
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
		subject, err = auth.NewAccountIdentitySubject(auth.IdentityID(plan.input.IdentityID))
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

func lockRegistrationIdentityNamespace(tx *gorm.DB, identityID string) error {
	if err := tx.Exec(
		`SELECT pg_advisory_xact_lock(hashtextextended('member-provision:' || ?::text, 0))`,
		identityID,
	).Error; err != nil {
		return err
	}
	var collision bool
	if err := tx.Raw(
		`SELECT EXISTS (SELECT 1 FROM member WHERE id = ?::uuid)`, identityID,
	).Scan(&collision).Error; err != nil {
		return err
	}
	if collision {
		return fmt.Errorf("identity_id collides with an existing member_id")
	}
	return nil
}

func loadRegistrationMember(
	tx *gorm.DB,
	plan registrationProvisioningPlan,
	member *model.Member,
) (bool, error) {
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("account_identity_id = ?", plan.input.IdentityID).
		First(member).Error
	if err == nil {
		if member.DeletedAt != nil {
			return true, fmt.Errorf("registration identity points to a deleted member")
		}
		if plan.identity.ExternalID != "" && plan.identity.ExternalID != member.ID {
			return true, fmt.Errorf("identity.external_id conflicts with the member reverse pointer")
		}
		return true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if plan.identity.ExternalID != "" {
		return false, fmt.Errorf("identity.external_id conflicts with the member reverse pointer")
	}
	return false, nil
}

func createRegistrationMember(
	tx *gorm.DB,
	plan registrationProvisioningPlan,
	member *model.Member,
) error {
	candidate, err := allocateMemberUUID(tx, uuid.NewString)
	if err != nil {
		return err
	}
	identityID := plan.input.IdentityID
	preferredLocale := plan.input.PreferredLocale
	// account_identity is the durable application anchor and the sole
	// SpiceDB subject. Kratos identity creation and this application anchor are
	// separate lifecycle steps, so establish the anchor in the same
	// transaction as the Member link before the FK is written.
	if err := tx.Exec(
		`INSERT INTO public.account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING`,
		identityID,
	).Error; err != nil {
		return fmt.Errorf("create account identity anchor: %w", err)
	}
	*member = model.Member{
		ID:                candidate,
		AccountIdentityID: &identityID,
		Nickname:          candidate,
		Onboarded:         false,
		PrimaryEmail:      &plan.primaryEmail,
		AvailableEmails:   plan.availableEmails,
		SocialLinks:       map[string]string{},
		PreferredLocale:   &preferredLocale,
	}
	return tx.Create(member).Error
}

func (s *MemberProvisioner) convergeRegistrationMemberLink(
	ctx context.Context,
	plan registrationProvisioningPlan,
	member model.Member,
) (*model.Member, error) {
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
	if err := s.emails.SyncMemberEmailProjection(
		ctx, s.db, s.identity, plan.input.IdentityID, verifiedIdentity,
	); err != nil {
		return nil, fmt.Errorf("sync initial Member email projection: %w", err)
	}
	if err := s.db.WithContext(ctx).Where("id = ?::uuid", member.ID).Take(&member).Error; err != nil {
		return nil, fmt.Errorf("reload provisioned Member: %w", err)
	}
	return &member, nil
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
	if err := s.db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1 FROM member
			WHERE id = ?::uuid
			  AND account_identity_id = ?::uuid
			  AND deleted_at IS NULL
		)
	`, memberID, identityID).Scan(&linked).Error; err != nil {
		return fmt.Errorf("verify member reverse pointer: %w", err)
	}
	if !linked {
		return fmt.Errorf("identity/member reverse pointer did not converge")
	}
	return nil
}
