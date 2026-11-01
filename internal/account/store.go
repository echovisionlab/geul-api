package account

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Target is an exact linked Member and Kratos Identity pair.
type Target struct {
	MemberID   string
	IdentityID string
	Onboarded  bool
}

// SummaryRow is the Member, linked Identity and pending deletion an Account
// summary is derived from.
type SummaryRow struct {
	MemberID       string
	IdentityID     *string
	Email          *string
	EmailVerified  bool
	IdentityState  *string
	MetadataBanned bool
	BanReason      *string
	BanExpires     *time.Time
	DeletedAt      *time.Time
	PendingDelete  bool
}

// Session is one live Kratos session of an Identity.
type Session struct {
	ID              string
	Active          bool
	AuthenticatedAt time.Time
	ExpiresAt       time.Time
}

// DeletionRequest is the durable account deletion and recovery lifecycle of
// a Member and Identity pair.
type DeletionRequest struct {
	ID                          string
	MemberID                    string
	IdentityID                  string
	Token                       string
	TokenExpiresAt              time.Time
	ConfirmedAt                 *time.Time
	ScheduledAt                 *time.Time
	LifecycleState              string
	NotificationEmail           *string
	NotificationEmailVerifiedAt *time.Time
	NotificationName            *string
	NotificationLocale          *string
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

// EmailChangeRequest is the active canonical email change of an Identity.
type EmailChangeRequest struct {
	ID                    string
	MemberID              string
	IdentityID            string
	PreviousEmailAddress  string
	RequestedEmailAddress string
	CreatedAt             time.Time
}

// NewsletterState is a newsletter subscription; SubscribedAt is set while
// subscribed.
type NewsletterState struct {
	Subscribed   bool
	SubscribedAt *time.Time
}

// DeleteIdentityMode is how the deletion worker removes an Identity.
type DeleteIdentityMode int32

// The deletion modes, numbered as the API's UserDeleteIdentityMode.
const (
	DeleteIdentityTombstone             DeleteIdentityMode = 1
	DeleteIdentityUnonboardedHardDelete DeleteIdentityMode = 2
)

// DeleteIdentityCommand is the durable command that deletes an Identity and
// settles its Member.
type DeleteIdentityCommand struct {
	Mode               DeleteIdentityMode
	MemberID           string
	IdentityID         string
	NotificationEmail  *string
	NotificationName   *string
	NotificationLocale *string
	AvatarAssetID      *string
}

// DeleteAvatarCommand is the durable command that cleans up a deleted
// Member's avatar.
type DeleteAvatarCommand struct {
	MemberID      string
	AvatarAssetID *string
}

// Store errors the use cases branch on.
var (
	// ErrMemberLinkNotFound answers a Member and Identity pair that is not
	// an exact live link.
	ErrMemberLinkNotFound = errors.New("account member link not found")
	// ErrIneligible answers a target lookup whose Member has no live,
	// eligible Identity link.
	ErrIneligible = errors.New("member is not eligible for authorization")
	// ErrIdentityNotFound answers an Identity Kratos did not return.
	ErrIdentityNotFound           = errors.New("record not found")
	ErrDeletionRequestNotFound    = errors.New("account deletion request not found")
	ErrEmailChangeRequestNotFound = errors.New("account email change request not found")
	// ErrEmailChangeRequestExists answers an email change request of an
	// Identity or Member that already has an active one.
	ErrEmailChangeRequestExists = errors.New("account email change request already exists")
)

// Store is the Account persistence of one transaction, read snapshot or
// fenced connection step, and the Member steps Account runs in it. It never
// opens a transaction.
type Store interface {
	// RequireLinkedMember and RequireLive resolve a target with the shared
	// helper's answers (authorizationtarget.RequireLinkedMember, RequireLive).
	RequireLinkedMember(ctx context.Context, memberID string, requireOnboarded bool) (Target, error)
	RequireLive(ctx context.Context, memberID string) (Target, error)
	// LinkedMemberForMember re-reads a linked target, or reports
	// ErrIneligible.
	LinkedMemberForMember(ctx context.Context, memberID string, requireOnboarded bool) (Target, error)
	// ValidateActivePair requires the exact active pair, or reports
	// ErrMemberLinkNotFound.
	ValidateActivePair(ctx context.Context, memberID, identityID string) error
	// ActiveMemberIDForIdentity reads the active Member linked to an
	// Identity, or reports ErrMemberLinkNotFound.
	ActiveMemberIDForIdentity(ctx context.Context, identityID string) (string, error)
	// ActiveOnboardedMemberForIdentity reads the active, onboarded pair of
	// an Identity, or reports ErrMemberLinkNotFound.
	ActiveOnboardedMemberForIdentity(ctx context.Context, identityID string) (Target, error)
	// LockActivePairForCredentialMutation locks the active, onboarded Member
	// row of the exact pair FOR UPDATE, or reports ErrMemberLinkNotFound.
	LockActivePairForCredentialMutation(ctx context.Context, memberID, identityID string) error
	// LockIdentity takes the Identity lifecycle fence in the transaction.
	LockIdentity(ctx context.Context, identityID string) error
	// LockedIdentityState locks the Kratos Identity and reads its state,
	// empty when it is gone.
	LockedIdentityState(ctx context.Context, identityID string) (string, error)
	// IdentityExists reports whether the Kratos Identity exists.
	IdentityExists(ctx context.Context, identityID string) (bool, error)

	// AccountSummaries reads the summary rows of the Members in memberIDs.
	AccountSummaries(ctx context.Context, memberIDs []string) ([]SummaryRow, error)
	// SessionSummary reads the lean summary row of a Member.
	SessionSummary(ctx context.Context, memberID string) (SummaryRow, bool, error)
	// LiveSessions lists the live sessions of an Identity, newest first.
	LiveSessions(ctx context.Context, identityID string) ([]Session, error)
	// OwnsLiveSession reports whether sessionID is a live session of the
	// Identity.
	OwnsLiveSession(ctx context.Context, identityID, sessionID string) (bool, error)
	// OtherLiveSessionIDs lists the Identity's live sessions other than
	// currentSessionID, in ID order.
	OtherLiveSessionIDs(ctx context.Context, identityID, currentSessionID string) ([]string, error)

	// DeletionRequestIdentityByToken reads the Identity of the request a
	// token hash names, or reports ErrDeletionRequestNotFound.
	DeletionRequestIdentityByToken(ctx context.Context, tokenHash string) (string, error)
	// LockDeletionRequestByToken locks the unexpired request of a token
	// hash in one of states FOR UPDATE, or reports
	// ErrDeletionRequestNotFound.
	LockDeletionRequestByToken(ctx context.Context, tokenHash string, now time.Time, states []string) (DeletionRequest, error)
	// LockPairDeletionRequest locks the request of a pair FOR UPDATE.
	LockPairDeletionRequest(ctx context.Context, memberID, identityID string) (DeletionRequest, bool, error)
	// DeleteDeletionRequest deletes one request.
	DeleteDeletionRequest(ctx context.Context, requestID string) error
	// DeletePairDeletionRequests deletes every request of a pair.
	DeletePairDeletionRequests(ctx context.Context, memberID, identityID string) error
	// InsertDeletionRequest stores a request.
	InsertDeletionRequest(ctx context.Context, request DeletionRequest) error
	// ScheduleDeletionRequest confirms a request for deletion at scheduledAt.
	ScheduleDeletionRequest(ctx context.Context, requestID string, now, scheduledAt time.Time) error
	// EndDeletionRequest moves a request to a terminal state and clears its
	// deadline.
	EndDeletionRequest(ctx context.Context, requestID, state string) error
	// StartRecovery moves a request to recovery confirmation under a new
	// token hash.
	StartRecovery(ctx context.Context, requestID, tokenHash string, tokenExpiresAt, now time.Time) error
	// PendingRecoveryIdentity reads the Identity of a pending deletion that
	// recorded email.
	PendingRecoveryIdentity(ctx context.Context, email string, now time.Time) (string, bool, error)
	// LockPendingRecovery locks the pending deletion that recorded email FOR
	// UPDATE.
	LockPendingRecovery(ctx context.Context, email string, now time.Time) (DeletionRequest, bool, error)
	// DeletionRequestPair reads the pair of a request.
	DeletionRequestPair(ctx context.Context, requestID string) (memberID, identityID string, found bool, err error)
	// LockDueDeletionRequest locks the due, pending request of the pair FOR
	// UPDATE.
	LockDueDeletionRequest(ctx context.Context, requestID, memberID, identityID string, now time.Time) (DeletionRequest, bool, error)
	// DeletionRequestInStates reads the request of an Identity in one of
	// states.
	DeletionRequestInStates(ctx context.Context, requestID, identityID string, states []string) (DeletionRequest, bool, error)
	// HasPendingDeletion reports whether the Identity's account is pending
	// deletion.
	HasPendingDeletion(ctx context.Context, identityID string) (bool, error)
	// ScheduledIdentityDrift lists pending deletions whose Identity is still
	// active or has a live session, oldest change first.
	ScheduledIdentityDrift(ctx context.Context, limit int) ([]DeletionRequest, error)
	// TerminalIdentityDrift lists cancelled or recovered deletions whose
	// Identity is still inactive since before the change, oldest first.
	TerminalIdentityDrift(ctx context.Context, limit int) ([]DeletionRequest, error)

	// LockAdminFence takes the last-administrator fence in the transaction.
	LockAdminFence(ctx context.Context) error
	// ActiveAdminCount counts the active, onboarded, unbanned Members of
	// identityIDs whose deletion is not pending.
	ActiveAdminCount(ctx context.Context, identityIDs []string) (int64, error)

	// LockEmailChangeRequest locks the Identity's active request FOR
	// UPDATE, nil when there is none.
	LockEmailChangeRequest(ctx context.Context, identityID string) (*EmailChangeRequest, error)
	// InsertEmailChangeRequest stores a request, or reports
	// ErrEmailChangeRequestExists.
	InsertEmailChangeRequest(ctx context.Context, request EmailChangeRequest) error
	// DeleteEmailChangeRequest deletes a request, or reports
	// ErrEmailChangeRequestNotFound.
	DeleteEmailChangeRequest(ctx context.Context, requestID string) error
	// EmailChangeRequest reads one request, nil when it is gone.
	EmailChangeRequest(ctx context.Context, requestID string) (*EmailChangeRequest, error)
	// EmailChangeRequestFor reads the exact request of a transition, nil
	// when there is none.
	EmailChangeRequestFor(ctx context.Context, identityID, previousEmail, requestedEmail string) (*EmailChangeRequest, error)
	// EmailChangeRequestTo reads the Identity's request of recipient, nil
	// when there is none.
	EmailChangeRequestTo(ctx context.Context, identityID, recipient string) (*EmailChangeRequest, error)
	// LatestEmailChangeRequest reads the newest request, nil when there is
	// none.
	LatestEmailChangeRequest(ctx context.Context) (*EmailChangeRequest, error)
	// EmailChangeRequests lists the requests up to upper and after the
	// cursor, oldest first.
	EmailChangeRequests(ctx context.Context, upper EmailChangeRequest, after *EmailChangeRequest, limit int) ([]EmailChangeRequest, error)

	// MemberPrimaryEmail reads the Member primary email of the exact live
	// pair, or reports ErrMemberLinkNotFound.
	MemberPrimaryEmail(ctx context.Context, memberID, identityID string) (string, error)
	// SyncMemberEmails replaces the Member email projection of the exact
	// live pair, or reports ErrMemberLinkNotFound.
	SyncMemberEmails(ctx context.Context, memberID, identityID, primaryEmail string, availableEmails []string) error
	// MutateNewsletter subscribes or unsubscribes an Identity and reports
	// whether its subscription changed.
	MutateNewsletter(ctx context.Context, identityID string, subscribed bool) (bool, error)
	// NewsletterState reads an Identity's subscription.
	NewsletterState(ctx context.Context, identityID string) (NewsletterState, error)
	// The Member steps of an account deletion (member.DeletionLifecycle).
	PrepareMemberTombstone(ctx context.Context, memberID, identityID, primaryEmail string) (MemberDeletionSnapshot, error)
	FinalizeMemberTombstone(ctx context.Context, snapshot MemberDeletionSnapshot, deletedAt time.Time, audit MemberDeletionAudit) error
	PrepareUnonboardedHardDelete(ctx context.Context, memberID, identityID string) (MemberUnonboardedDeletionTarget, error)
	FinalizeUnonboardedHardDelete(ctx context.Context, target MemberUnonboardedDeletionTarget, audit MemberDeletionAudit) error
	MemberDeletionNotification(ctx context.Context, memberID, identityID string) (MemberDeletionNotification, error)
	MemberCompletionEligible(ctx context.Context, memberID string) (bool, error)
	MemberAvatarAssetID(ctx context.Context, memberID string) (string, error)
	ExpiredUnonboardedMembers(ctx context.Context, cutoff time.Time, limit int, after *MemberUnonboardedCursor) ([]MemberUnonboardedCandidate, *MemberUnonboardedCursor, error)
	RecheckUnonboardedMember(ctx context.Context, memberID, identityID string, cutoff *time.Time) (bool, error)
	UnonboardedMemberIdentity(ctx context.Context, memberID string) (string, bool, error)
	// DeleteAccountIdentityAnchor deletes the application anchor of an
	// Identity whose SpiceDB relationships are gone.
	DeleteAccountIdentityAnchor(ctx context.Context, identityID string) error

	// EnqueueUserDeleteIdentity and EnqueueUserDeleteAvatar enqueue the
	// durable deletion commands in the transaction.
	EnqueueUserDeleteIdentity(ctx context.Context, command DeleteIdentityCommand) error
	EnqueueUserDeleteAvatar(ctx context.Context, command DeleteAvatarCommand) error

	// AppendRequestAudit, AppendSystemAudit and AppendAuditRecord append a
	// record through appender in the store's own transaction, for the
	// Account transactions the mutation pipeline does not run
	// (domainaudit.AppendRequest, domainaudit.AppendSystem).
	AppendRequestAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error
	AppendSystemAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error
	AppendAuditRecord(ctx context.Context, appender domainaudit.Appender, record sharedtelemetry.AuditRecord) error
}

// MemberDeletionAudit appends the terminal account.deleted record of a
// Member in the transaction of its Member step.
type MemberDeletionAudit func(ctx context.Context, memberID string) error

// Units are the transactions of the Account use cases: the pipeline's units
// of work, the explicit steps of a principal-less flow that holds an
// Identity fence across Kratos calls, and the leader lock of a background
// reconciler.
type Units interface {
	mutation.UnitOfWork[Store]
	// Fenced holds the global authorization lock and the Identity fence of
	// identityID on one dedicated connection while work runs (the session
	// form of identitystate.WithMutation). Each Step of work is one
	// transaction on that connection and commits when it returns, so a
	// later Kratos failure leaves the earlier steps committed.
	Fenced(ctx context.Context, identityID string, work func(ctx context.Context, steps Steps) error) error
	// Lead runs work only while this process holds the leader lock of
	// leader, and returns nil without running it otherwise.
	Lead(ctx context.Context, leader Leader, work func(ctx context.Context) error) error
}

// Steps opens the step transactions of a fenced flow.
type Steps interface {
	// Step runs work in one transaction on the fenced connection and
	// commits when it returns nil.
	Step(ctx context.Context, work func(Store) error) error
}

// Leader names a background reconciler that runs on one process at a time.
type Leader string

// The Account reconcilers.
const (
	LeaderLifecycle   Leader = "account-lifecycle"
	LeaderEmailChange Leader = "account-email-change"
)
