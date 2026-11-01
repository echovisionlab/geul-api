package member

import (
	"context"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// NewsletterStore is the newsletter subscription row of an Identity, the one
// Member fact an Account request and the public opt-out change.
type NewsletterStore interface {
	// SubscribeNewsletter stores the subscription unless one exists and
	// reports whether it did.
	SubscribeNewsletter(ctx context.Context, identityID string, subscribedAt time.Time) (bool, error)
	// UnsubscribeNewsletter deletes the subscription and reports whether one
	// existed.
	UnsubscribeNewsletter(ctx context.Context, identityID string) (bool, error)
	// NewsletterSubscription reads when identityID subscribed, nil when it
	// did not.
	NewsletterSubscription(ctx context.Context, identityID string) (*time.Time, error)
}

// EmailProjectionStore is the Member email projection Account keeps in step
// with the proven Identity candidates.
type EmailProjectionStore interface {
	// PrimaryEmail reads the primary email of the exact live linked pair, or
	// reports ErrMemberNotFound.
	PrimaryEmail(ctx context.Context, memberID, identityID string) (string, error)
	// LockLinkedMember locks the Member row of the exact live linked pair FOR
	// UPDATE and returns its ID, or reports ErrMemberNotFound.
	LockLinkedMember(ctx context.Context, memberID, identityID string) (string, error)
	// UpdateEmailProjection writes the projection of the live linked pair.
	UpdateEmailProjection(ctx context.Context, memberID, identityID, primaryEmail string, availableEmails []string) error
}

// DeletionStore is the Member side of an account deletion, which runs in
// Account's transaction or on its fenced connection.
type DeletionStore interface {
	// DeletionMember reads the link, email projection, onboarding and
	// deletion of a Member, or reports ErrMemberNotFound.
	DeletionMember(ctx context.Context, memberID string) (Member, error)
	// DeletionPairWitness reports whether the exact pair is linked
	// bilaterally and whether the Identity exists.
	DeletionPairWitness(ctx context.Context, memberID, identityID string) (linked, identityExists bool, err error)
	// IdentityExists reports whether the Kratos Identity exists.
	IdentityExists(ctx context.Context, identityID string) (bool, error)
	// LockMember locks a Member row FOR UPDATE, or reports ErrMemberNotFound.
	LockMember(ctx context.Context, memberID string) (Member, error)
	// DeletePersonalAccessTokens deletes every personal access token of a
	// Member.
	DeletePersonalAccessTokens(ctx context.Context, memberID string) error
	// TombstoneMember scrubs the profile of an unlinked live Member and marks
	// it deleted.
	TombstoneMember(ctx context.Context, memberID string, deletedAt time.Time) error
	// NotificationMember reads the nickname, primary email and locale of the
	// exact live linked pair, or reports ErrMemberNotFound.
	NotificationMember(ctx context.Context, memberID, identityID string) (Member, error)
	// CompletionEligible reports whether the Member is an onboarded
	// tombstone.
	CompletionEligible(ctx context.Context, memberID string) (bool, error)
	// AvatarAssetID reads the asset of the Member's avatar binding, empty when
	// there is none.
	AvatarAssetID(ctx context.Context, memberID string) (string, error)
	// ReleaseAvatarBindings releases every avatar binding of the Member.
	ReleaseAvatarBindings(ctx context.Context, memberID string) error
	// PublicAssetStatus reads the status of a public asset.
	PublicAssetStatus(ctx context.Context, assetID string) (status string, found bool, err error)
	// RequestPublicAssetDeletion schedules the deletion of a public asset.
	RequestPublicAssetDeletion(ctx context.Context, assetID string) error
	// DeleteUnonboardedReferences releases the avatar, the tags and the
	// bootstrap references of a Member about to be hard-deleted.
	DeleteUnonboardedReferences(ctx context.Context, memberID string) error
	// DeleteUnonboardedMember deletes the unlinked, unonboarded live Member
	// row and reports whether it did.
	DeleteUnonboardedMember(ctx context.Context, memberID string) (bool, error)
	// ExpiredUnonboarded lists linked, unonboarded live Members created no
	// later than cutoff, in creation order after the cursor.
	ExpiredUnonboarded(ctx context.Context, cutoff time.Time, limit int, after *UnonboardedCursor) ([]UnonboardedCandidate, error)
	// UnonboardedLinked reports whether the exact pair is linked, unonboarded
	// and live, and created no later than cutoff when it is set.
	UnonboardedLinked(ctx context.Context, memberID, identityID string, cutoff *time.Time) (bool, error)
	// UnonboardedIdentity reads the Identity linked to an unonboarded live
	// Member.
	UnonboardedIdentity(ctx context.Context, memberID string) (string, bool, error)
}

// Store is the Member persistence of one transaction or read snapshot. It
// never opens a transaction.
type Store interface {
	NewsletterStore
	EmailProjectionStore
	DeletionStore

	// Members reads the Members in ids, every Member when ids is empty.
	Members(ctx context.Context, ids []string) ([]Member, error)
	// MemberByID reads one Member, or reports ErrMemberNotFound.
	MemberByID(ctx context.Context, memberID string) (Member, error)
	// SessionMember reads the session columns of one Member, or reports
	// ErrMemberNotFound.
	SessionMember(ctx context.Context, memberID string) (Member, error)
	// OwnMember reads an onboarded live linked Member, or reports
	// ErrMemberNotFound.
	OwnMember(ctx context.Context, memberID string) (Member, error)
	// SettingsMember reads a live linked Member, onboarded or not, or reports
	// ErrMemberNotFound.
	SettingsMember(ctx context.Context, memberID string) (Member, error)
	// LinkedMember reads the live Member of the exact pair, or reports
	// ErrMemberNotFound.
	LinkedMember(ctx context.Context, memberID, identityID string) (Member, error)
	// SearchMembers reads onboarded live linked Members in nickname order.
	SearchMembers(ctx context.Context, search MemberSearch) ([]Member, error)
	// AdminMemberPage reads one filtered, sorted page of every Member and how
	// many match.
	AdminMemberPage(ctx context.Context, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, limit, offset int) ([]Member, int64, error)
	// Avatars reads the ready avatar of each Member that has one.
	Avatars(ctx context.Context, memberIDs []string) (map[string]*Asset, error)
	// NewsletterSubscriptions reads when each linked Member's Identity
	// subscribed.
	NewsletterSubscriptions(ctx context.Context, memberIDs []string) (map[string]time.Time, error)
	// LatestCookieConsent reads the Member's latest consent, nil when there
	// is none.
	LatestCookieConsent(ctx context.Context, memberID string) (*CookieConsent, error)
	// NicknameClaimed reports whether another live Member holds nickname.
	NicknameClaimed(ctx context.Context, nickname, exceptMemberID string) (bool, error)

	// RequireTarget, RequireLinkedTarget resolve a target before a mutation
	// with the shared helper's answers (authorizationtarget.Require,
	// RequireLinkedMember).
	RequireTarget(ctx context.Context, memberID string) (Target, error)
	RequireLinkedTarget(ctx context.Context, memberID string, requireOnboarded bool) (Target, error)
	// CurrentTarget and CurrentLinkedTarget re-read a target inside the
	// mutation (authorizationtarget.ForMember, LinkedMemberForMember), or
	// report ErrIneligible.
	CurrentTarget(ctx context.Context, memberID string) (Target, error)
	CurrentLinkedTarget(ctx context.Context, memberID string, requireOnboarded bool) (Target, error)

	// UpdateProfile writes the set fields of update, or reports
	// ErrNicknameTaken.
	UpdateProfile(ctx context.Context, memberID string, update ProfileUpdate) error
	// CompleteOnboarding onboards the unonboarded live pair under nickname
	// and reports whether it did, or reports ErrNicknameTaken.
	CompleteOnboarding(ctx context.Context, memberID, identityID, nickname string, now time.Time) (bool, error)
	// UpdatePreferredLocale writes the Member's preferred locale.
	UpdatePreferredLocale(ctx context.Context, memberID, locale string, now time.Time) error
	// InsertCookieConsent records a consent and returns its ID.
	InsertCookieConsent(ctx context.Context, consent CookieConsent) (string, error)

	// TagIDs reads the tags of each Member that has one, in tag ID order.
	TagIDs(ctx context.Context, memberIDs []string) (map[string][]string, error)
	// TagPage reads one filtered, sorted page of tags with their member
	// counts and how many tags match.
	TagPage(ctx context.Context, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, limit, offset int) ([]Tag, int64, error)
	// InsertTag creates a tag, or reports ErrTagNameTaken.
	InsertTag(ctx context.Context, name string) (Tag, error)
	// LockTag locks a tag FOR UPDATE, or reports ErrTagNotFound.
	LockTag(ctx context.Context, tagID string) (Tag, error)
	// TagReferencedBySegment reports whether an audience segment uses the
	// tag.
	TagReferencedBySegment(ctx context.Context, tagID string) (bool, error)
	// DeleteTag deletes a tag and its Member assignments.
	DeleteTag(ctx context.Context, tagID string) error
	// CountTags counts the existing tags among tagIDs.
	CountTags(ctx context.Context, tagIDs []string) (int64, error)
	// ReplaceMemberTags replaces the Member's tag assignments.
	ReplaceMemberTags(ctx context.Context, memberID string, tagIDs []string) error

	// AvatarSourceOwned reports whether fileID is an avatar upload, of
	// ownerID when it is set.
	AvatarSourceOwned(ctx context.Context, fileID, ownerID string) (bool, error)
	// LockAvatarBinding locks the Member's avatar binding and returns its
	// asset.
	LockAvatarBinding(ctx context.Context, memberID string) (assetID string, found bool, err error)
	// BindAvatar binds assetID, promoted from sourceFileID, as the avatar.
	BindAvatar(ctx context.Context, memberID, assetID, sourceFileID string) error
	// ReleaseAvatar releases the Member's exact avatar binding.
	ReleaseAvatar(ctx context.Context, memberID string) error

	// WelcomeDelivery resolves the recipient of the welcome email of an
	// Identity through the Account email projection.
	WelcomeDelivery(ctx context.Context, identityID string) (WelcomeDelivery, error)
	// EnqueueWelcomeEmail enqueues the welcome email command in the
	// transaction.
	EnqueueWelcomeEmail(ctx context.Context, email WelcomeEmail) error

	// LockRegistrationIdentity takes the provisioning lock of an Identity
	// and fails when an existing Member ID collides with it.
	LockRegistrationIdentity(ctx context.Context, identityID string) error
	// LockRegistrationMember locks the Member linked to identityID.
	LockRegistrationMember(ctx context.Context, identityID string) (Member, bool, error)
	// MemberIDOccupied reports whether candidate is a Member ID, a Member
	// nickname or an Identity ID.
	MemberIDOccupied(ctx context.Context, candidate string) (bool, error)
	// CreateRegistrationMember stores the account identity anchor and the
	// Member linked to it.
	CreateRegistrationMember(ctx context.Context, member Member) (Member, error)
	// EmailReuseHeld reports whether a deleted or unlinked Member still
	// holds the normalized email (RegistrationEmailReuseBlocked).
	EmailReuseHeld(ctx context.Context, email string) (bool, error)
	// LinkedMemberExists reports whether the exact live pair is linked from
	// the Member side.
	LinkedMemberExists(ctx context.Context, memberID, identityID string) (bool, error)
}

// MemberSearch is one Member picker search.
type MemberSearch struct {
	Query string
	Limit int
	// IdentityIDs restricts the result to these Identities when not nil.
	IdentityIDs      []string
	ExcludeMemberIDs []string
}

// Optional is a nullable text field of an update: Set writes Value, and a
// nil Value clears it.
type Optional struct {
	Set   bool
	Value *string
}

// ProfileUpdate is the profile change a Store writes; a nil Nickname or
// SocialLinks and an unset Optional stay unchanged.
type ProfileUpdate struct {
	Nickname    *string
	Bio         Optional
	Website     Optional
	SocialLinks map[string]string
	UpdatedAt   time.Time
}

// WelcomeDelivery is the welcome recipient the Account email projection
// resolved, or why it found none.
type WelcomeDelivery struct {
	Recipient      string
	LinkedMemberID string
	Reason         string
}

// WelcomeEmail is the welcome email command of an onboarded Member.
type WelcomeEmail struct {
	CommandID  string
	IdentityID string
	Recipient  string
	Nickname   string
	LoginURL   string
	Locale     *string
}

// UnonboardedCandidate is a Member-owned retention predicate result.
type UnonboardedCandidate struct {
	MemberID   string
	IdentityID string
	CreatedAt  time.Time
}

// UnonboardedCursor carries the stable bounded-page ordering for retention.
type UnonboardedCursor struct {
	CreatedAt time.Time
	MemberID  string
}
