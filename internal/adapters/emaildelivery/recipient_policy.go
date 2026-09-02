package emaildeliveryadapter

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type RecipientPolicy struct {
	db           *gorm.DB
	kratosClient auth.IdentityManager
}

func NewRecipientPolicy(db *gorm.DB, kratosClient auth.IdentityManager) *RecipientPolicy {
	return &RecipientPolicy{db: db, kratosClient: kratosClient}
}

type recipientBlockedError struct{ reason string }

func (e recipientBlockedError) Error() string { return e.reason }

var allowedSystemDirectEmailReasons = map[string]struct{}{
	"account_deletion_complete":  {},
	"account_deletion_scheduled": {},
	"account_recovery_confirm":   {},
	"account_recovery_complete":  {},
	"primary_email_changed":      {},
}

func (h *RecipientPolicy) Authorize(ctx context.Context, job *managev1.SendEmailEvent) (emaildelivery.RecipientDecision, error) {
	_, err := h.authorize(ctx, job)
	var blocked recipientBlockedError
	if errors.As(err, &blocked) {
		return emaildelivery.RecipientDecision{Blocked: true, Reason: blocked.reason}, nil
	}
	return emaildelivery.RecipientDecision{}, err
}

func (h *RecipientPolicy) authorize(ctx context.Context, job *managev1.SendEmailEvent) (bool, error) {
	if job.GetRecipientContext() == nil {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_required", "email recipient context is required")
	}
	if !emailauthoring.EmailTemplateContextAllowed(job) {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_template_mismatch", "recipient context is not allowed for email template class")
	}

	switch recipient := job.GetRecipientContext().(type) {
	case *managev1.SendEmailEvent_AccountSelectedPrimaryEmail:
		return h.ensureAccountSelectedPrimaryEmail(ctx, job, recipient.AccountSelectedPrimaryEmail.GetIdentityId())
	case *managev1.SendEmailEvent_NewsletterSubscription:
		return h.enforceNewsletterRecipientContext(ctx, job, recipient.NewsletterSubscription)
	case *managev1.SendEmailEvent_AccountVerification:
		return h.enforceAccountVerificationRecipientContext(ctx, job, recipient.AccountVerification)
	case *managev1.SendEmailEvent_AuthLogin:
		return h.enforceAuthLoginRecipientContext(ctx, job, recipient.AuthLogin)
	case *managev1.SendEmailEvent_AuthRegistration:
		return h.enforceAuthRegistrationRecipientContext(ctx, job, recipient.AuthRegistration)
	case *managev1.SendEmailEvent_TestEmail:
		return h.enforceTestEmailRecipientContext(ctx, job, recipient.TestEmail)
	case *managev1.SendEmailEvent_SystemDirect:
		return h.enforceSystemDirectRecipientContext(ctx, job, recipient.SystemDirect)
	default:
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "email recipient context is unsupported")
	}
}

func (h *RecipientPolicy) enforceNewsletterRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	recipient *managev1.NewsletterSubscriptionRecipient,
) (bool, error) {
	if recipient == nil || strings.TrimSpace(recipient.GetIdentityId()) == "" ||
		strings.TrimSpace(recipient.GetMemberId()) == "" {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "missing newsletter identity or member context")
	}
	return h.ensureNewsletterCurrentVerifiedEmail(ctx, job, recipient.GetIdentityId(), recipient.GetMemberId())
}

func (h *RecipientPolicy) enforceAccountVerificationRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	recipient *managev1.AccountVerificationRecipient,
) (bool, error) {
	if recipient == nil || strings.TrimSpace(recipient.GetIdentityId()) == "" ||
		!sameEmailAddress(recipient.GetTargetEmail(), job.Recipient) {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "invalid account verification recipient context")
	}
	return h.ensureAccountVerificationAllowed(ctx, job, recipient.GetIdentityId())
}

func (h *RecipientPolicy) enforceAuthLoginRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	recipient *managev1.AuthLoginRecipient,
) (bool, error) {
	if recipient == nil || strings.TrimSpace(recipient.GetIdentityId()) == "" ||
		!sameEmailAddress(recipient.GetTargetEmail(), job.Recipient) {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "invalid authentication login recipient context")
	}
	return h.ensureAuthLoginAllowed(ctx, job, recipient.GetIdentityId())
}

func (h *RecipientPolicy) enforceAuthRegistrationRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	recipient *managev1.AuthRegistrationRecipient,
) (bool, error) {
	if recipient == nil || !sameEmailAddress(recipient.GetTargetEmail(), job.Recipient) {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "invalid authentication registration recipient context")
	}
	return false, nil
}

func (h *RecipientPolicy) enforceTestEmailRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	recipient *managev1.TestEmailRecipient,
) (bool, error) {
	if recipient == nil || strings.TrimSpace(recipient.GetActorMemberId()) == "" {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "missing test email actor context")
	}
	return false, nil
}

func (h *RecipientPolicy) enforceSystemDirectRecipientContext(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	recipient *managev1.SystemDirectRecipient,
) (bool, error) {
	reason := ""
	if recipient != nil {
		reason = strings.TrimSpace(recipient.GetReason())
	}
	if _, allowed := allowedSystemDirectEmailReasons[reason]; !allowed {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "system direct email reason is not allowed")
	}
	if strings.TrimSpace(job.TemplateType) != reason {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "system direct email reason does not match template")
	}
	return false, nil
}

func (h *RecipientPolicy) ensureAccountVerificationAllowed(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	identityID string,
) (bool, error) {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "missing account verification identity context")
	}
	if h.kratosClient == nil {
		return false, fmt.Errorf("identity client is required for verification-code delivery gate")
	}

	identity, err := h.kratosClient.GetIdentity(ctx, identityID)
	if err != nil {
		return false, err
	}
	if identity == nil {
		return h.blockEmailForRecipientContext(ctx, job, "identity_missing", "account verification identity was not found")
	}
	if identity.IsBanned() {
		return h.blockEmailForRecipientContext(ctx, job, "account_banned", "account verification identity is inactive or banned")
	}

	currentEmail := identity.CurrentEmail()
	pendingEmail := identity.PendingEmail()
	if !sameEmailAddress(currentEmail, job.Recipient) && !sameEmailAddress(pendingEmail, job.Recipient) {
		return h.blockEmailForRecipientContext(ctx, job, "verification_email_stale", "verification recipient is no longer current or pending")
	}
	if !identity.HasUnverifiedEmailAddress(job.Recipient) {
		return h.blockEmailForRecipientContext(ctx, job, "verification_not_pending", "verification recipient is missing or already verified")
	}

	return false, nil
}

func (h *RecipientPolicy) ensureAuthLoginAllowed(ctx context.Context, job *managev1.SendEmailEvent, identityID string) (bool, error) {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "missing authentication login identity context")
	}
	if h.kratosClient == nil {
		return false, fmt.Errorf("identity client is required for login-code delivery gate")
	}

	identity, err := h.kratosClient.GetIdentityWithIncludeCredential(ctx, identityID, "code")
	if err != nil {
		return false, err
	}
	if identity == nil {
		return h.blockEmailForRecipientContext(ctx, job, "identity_missing", "authentication login identity was not found")
	}
	if identity.IsBanned() {
		return h.blockEmailForRecipientContext(ctx, job, "account_banned", "authentication login identity is inactive or banned")
	}
	codeCredential, ok := identity.Credentials["code"]
	if !ok || !auth.CodeCredentialHasAddress(codeCredential, job.Recipient) {
		return h.blockEmailForRecipientContext(ctx, job, "code_address_mismatch", "login recipient is not an identity code credential address")
	}
	return false, nil
}

func (h *RecipientPolicy) ensureAccountSelectedPrimaryEmail(ctx context.Context, job *managev1.SendEmailEvent, identityID string) (bool, error) {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "missing account identity context")
	}
	if h.kratosClient == nil {
		return false, fmt.Errorf("kratos client is required for account email delivery gate")
	}
	identity, err := account.LoadIdentityWithEmailCredentials(ctx, h.kratosClient, identityID)
	if err != nil {
		return false, err
	}
	if identity == nil || identity.ID != identityID || identity.IsBanned() {
		return h.blockEmailForRecipientContext(ctx, job, "identity_inactive", "account identity is missing or inactive")
	}
	var member model.Member
	if err := h.db.WithContext(ctx).
		Select("id", "account_identity_id", "primary_email", "onboarded", "deleted_at").
		Where("account_identity_id = ? AND onboarded = TRUE AND deleted_at IS NULL", identityID).
		Take(&member).Error; err != nil {
		return h.blockEmailForRecipientContext(ctx, job, "member_identity_link_mismatch", "account identity is not linked to an active Member")
	}
	if member.ID != identity.ExternalID {
		return h.blockEmailForRecipientContext(ctx, job, "member_identity_link_mismatch", "account identity link is not bilateral")
	}
	canonicalEmail := identity.CurrentEmail()
	if member.PrimaryEmail == nil || !sameEmailAddress(*member.PrimaryEmail, canonicalEmail) || !sameEmailAddress(canonicalEmail, job.Recipient) || !account.IdentityHasUsableDeliveryEmail(ctx, identity, canonicalEmail) {
		return h.blockEmailForRecipientContext(ctx, job, "account_email_mismatch", "recipient is not the synchronized canonical account email")
	}
	if blocked, err := h.ensureCommittedAccountSecurityMutation(ctx, job, identity); blocked || err != nil {
		return blocked, err
	}
	return false, nil
}

func (h *RecipientPolicy) ensureCommittedAccountSecurityMutation(ctx context.Context, job *managev1.SendEmailEvent, identity *auth.Identity) (bool, error) {
	switch job.GetTemplateType() {
	case email.EventSocialLoginAdded.String(), email.EventSocialLoginRemoved.String():
		provider := strings.TrimSpace(job.GetTemplateData()["provider"])
		subject := strings.TrimSpace(job.GetTemplateData()["_provider_subject"])
		if provider == "" || subject == "" {
			return h.blockEmailForRecipientContext(ctx, job, "security_mutation_context_invalid", "social sign-in notice is missing exact credential context")
		}
		committed := auth.NewCredentialInventory(identity.Credentials).HasOIDCProvider(provider, subject)
		expected := job.GetTemplateType() == email.EventSocialLoginAdded.String()
		if committed != expected {
			return h.blockEmailForRecipientContext(ctx, job, "security_mutation_not_committed", "social sign-in mutation was not committed")
		}
		delete(job.TemplateData, "_provider_subject")
	case email.EventPasskeyAdded.String(), email.EventPasskeyRemoved.String():
		expectedCount, err := strconv.Atoi(strings.TrimSpace(job.GetTemplateData()["_credential_count"]))
		if err != nil || expectedCount < 0 {
			return h.blockEmailForRecipientContext(ctx, job, "security_mutation_context_invalid", "passkey notice is missing exact credential context")
		}
		passkeyIdentity, err := h.kratosClient.GetIdentityWithIncludeCredential(ctx, identity.ID, "passkey")
		if err != nil {
			return false, err
		}
		actualCount := 0
		if passkeyIdentity != nil {
			actualCount = auth.UsablePasskeyCredentialCount(passkeyIdentity.Credentials["passkey"])
		}
		if actualCount != expectedCount {
			return h.blockEmailForRecipientContext(ctx, job, "security_mutation_not_committed", "passkey mutation was not committed")
		}
		delete(job.TemplateData, "_credential_count")
	}
	return false, nil
}

func (h *RecipientPolicy) ensureNewsletterCurrentVerifiedEmail(ctx context.Context, job *managev1.SendEmailEvent, identityID string, memberID string) (bool, error) {
	identityID = strings.TrimSpace(identityID)
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "invalid newsletter identity context")
	}
	memberID = strings.TrimSpace(memberID)
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return h.blockEmailForRecipientContext(ctx, job, "recipient_context_invalid", "invalid newsletter member context")
	}
	if h.kratosClient == nil {
		return false, fmt.Errorf("kratos client is required for newsletter delivery gate")
	}
	identity, err := account.LoadIdentityWithEmailCredentials(ctx, h.kratosClient, identityID)
	if err != nil {
		return false, err
	}
	if identity == nil || identity.ID != identityID || identity.IsBanned() {
		return h.blockEmailForRecipientContext(ctx, job, "identity_inactive", "newsletter identity is missing, inactive or banned")
	}
	if identity.ExternalID != memberID {
		return h.blockEmailForRecipientContext(ctx, job, "member_identity_link_mismatch", "newsletter identity does not point to the requested Member")
	}
	var member model.Member
	if err := h.db.WithContext(ctx).
		Select("id", "account_identity_id", "primary_email", "onboarded", "deleted_at").
		Where("id = ? AND account_identity_id = ? AND onboarded = TRUE AND deleted_at IS NULL", memberID, identityID).
		First(&member).Error; err != nil {
		return h.blockEmailForRecipientContext(ctx, job, "member_identity_link_mismatch", "newsletter identity is not linked to the requested active Member")
	}
	if member.AccountIdentityID == nil || *member.AccountIdentityID != identityID {
		return h.blockEmailForRecipientContext(ctx, job, "member_identity_link_mismatch", "newsletter member identity link is not bilateral")
	}
	canonicalEmail := identity.CurrentEmail()
	if member.PrimaryEmail == nil || !sameEmailAddress(*member.PrimaryEmail, canonicalEmail) || !sameEmailAddress(canonicalEmail, job.Recipient) || !account.IdentityHasUsableDeliveryEmail(ctx, identity, canonicalEmail) {
		return h.blockEmailForRecipientContext(ctx, job, "account_email_mismatch", "recipient is not the synchronized canonical account email")
	}
	var count int64
	if err := h.db.WithContext(ctx).
		Model(&model.NewsletterSubscription{}).
		Where("identity_id = ?", identity.ID).
		Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return h.blockEmailForRecipientContext(ctx, job, "newsletter_subscription_missing", "newsletter subscription is not enabled")
	}
	return false, nil
}

func (h *RecipientPolicy) blockEmailForRecipientContext(_ context.Context, _ *managev1.SendEmailEvent, reason string, _ string) (bool, error) {
	return false, recipientBlockedError{reason: strings.TrimSpace(reason)}
}

func sameEmailAddress(a string, b string) bool {
	normalizedA := email.NormalizeAddressForDelivery(a)
	normalizedB := email.NormalizeAddressForDelivery(b)
	return normalizedA != "" && normalizedA == normalizedB
}
