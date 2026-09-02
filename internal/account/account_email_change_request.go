package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var (
	ErrAccountEmailChangeConflict = errors.New("account email change conflicts with another identity")
	ErrAccountEmailChangeInFlight = errors.New("verified account email change reconciliation is in progress")
	// ErrAccountEmailChangeNotificationPublish means the identity and account
	// projection already converged, but the durable notification command was not
	// confirmed. The active request remains the retry authority.
	ErrAccountEmailChangeNotificationPublish = errors.New("account email change notification was not published")
)

const (
	accountEmailChangeReconcileInterval = time.Minute
	accountEmailChangeProofGrace        = 15 * time.Minute
	accountEmailChangeBatchSize         = 50
	accountEmailChangeLeaderLockKey     = int64(0x4745554c454d4149) // GUELEMAI
)

type AccountEmailChangePublisher interface {
	PublishSendEmail(context.Context, *managev1.SendEmailEvent) error
}

type accountEmailChangeIdentityManager interface {
	auth.IdentityManager
	auth.IdentityCredentialFinder
	auth.IdentityAccountEmailStateWriter
}

// AccountEmailChangeLifecycle converges active email-change requests from
// Kratos' canonical, pending and verified address facts. It does not persist a
// copy of the derived phase or PGMQ delivery state.
type AccountEmailChangeLifecycle struct {
	db           *gorm.DB
	identity     accountEmailChangeIdentityManager
	publisher    AccountEmailChangePublisher
	auditWriter  domainaudit.Appender
	now          func() time.Time
	interval     time.Duration
	memberEmails MemberEmailProjection
}

// AuthorizeSettingsGeneratedVerification verifies the narrow Kratos v26.2
// settings courier fallback. Canonical changes require their active request.
func (s *AccountEmailChangeLifecycle) AuthorizeSettingsGeneratedVerification(
	ctx context.Context,
	identityID string,
	recipient string,
) (bool, error) {
	identityID = strings.TrimSpace(identityID)
	recipient = normalizeAccountEmail(recipient)
	if identityID == "" || recipient == "" {
		return false, nil
	}

	identity, err := LoadIdentityWithEmailCredentials(ctx, s.identity, identityID)
	if err != nil {
		return false, err
	}
	if identity == nil || identity.ID != identityID ||
		identity.HasVerifiedEmailAddress(recipient) ||
		!identity.HasUnverifiedEmailAddress(recipient) {
		return false, nil
	}
	var request model.AccountEmailChangeRequest
	result := s.db.WithContext(ctx).
		Where("identity_id = ?::uuid AND requested_email_address = ?", identityID, recipient).
		First(&request)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, result.Error
	}
	if !accountEmailsEqual(identity.CurrentEmail(), request.PreviousEmailAddress) {
		return false, nil
	}
	pending := normalizeAccountEmail(identity.PendingEmail())
	return pending == "" || accountEmailsEqual(pending, recipient), nil
}

func NewAccountEmailChangeLifecycle(
	db *gorm.DB,
	identity accountEmailChangeIdentityManager,
	publisher AccountEmailChangePublisher,
	memberEmails MemberEmailProjection,
) *AccountEmailChangeLifecycle {
	if db == nil {
		panic("account email change lifecycle: db is required")
	}
	if identity == nil {
		panic("account email change lifecycle: identity manager is required")
	}
	if publisher == nil {
		panic("account email change lifecycle: publisher is required")
	}
	if memberEmails == nil {
		panic("account email change lifecycle: member email projection is required")
	}
	lifecycle := &AccountEmailChangeLifecycle{
		db:           db,
		identity:     identity,
		publisher:    publisher,
		memberEmails: memberEmails,
		now:          time.Now,
		interval:     accountEmailChangeReconcileInterval,
	}
	return lifecycle
}

func NewAuditedAccountEmailChangeLifecycle(
	db *gorm.DB,
	identity accountEmailChangeIdentityManager,
	publisher AccountEmailChangePublisher,
	memberEmails MemberEmailProjection,
	auditWriter domainaudit.Appender,
) *AccountEmailChangeLifecycle {
	if auditWriter == nil {
		panic("account email change lifecycle: audit writer is required")
	}
	lifecycle := NewAccountEmailChangeLifecycle(db, identity, publisher, memberEmails)
	lifecycle.auditWriter = auditWriter
	return lifecycle
}

func normalizeAccountEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func accountEmailsEqual(a, b string) bool {
	return normalizeAccountEmail(a) == normalizeAccountEmail(b)
}

func validateAccountEmailLength(value string) error {
	if utf8.RuneCountInString(value) > 254 {
		return fmt.Errorf("account email exceeds 254 characters")
	}
	return nil
}
