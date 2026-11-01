package member

import (
	"errors"
	"time"
)

// Member is a Member row: the durable domain actor, retained as a tombstone
// after its account is deleted.
type Member struct {
	ID                string
	AccountIdentityID *string
	Nickname          string
	Onboarded         bool
	PrimaryEmail      *string
	AvailableEmails   []string
	Bio               *string
	Website           *string
	SocialLinks       map[string]string
	PreferredLocale   *string
	DeletedAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// tombstone reports a deleted Member or one whose Identity link is gone;
// neither exposes profile PII.
func (m Member) tombstone() bool {
	return m.DeletedAt != nil || m.AccountIdentityID == nil
}

// AssetDisposition is the content disposition of a public asset, numbered as
// the API's AssetDisposition.
type AssetDisposition int32

// Asset is a ready public asset as the media runtime projects it.
type Asset struct {
	ID               string
	URL              string
	Extension        string
	MIMEType         string
	FileSize         int64
	SHA256           []byte
	Disposition      AssetDisposition
	DownloadFilename *string
}

// Summary is the Member reference every other domain shows.
type Summary struct {
	ID       string
	Nickname string
	Avatar   *Asset
	Deleted  bool
}

// Profile is a Member's profile. A tombstone carries only its summary and
// creation time.
type Profile struct {
	Summary         Summary
	Bio             *string
	Website         *string
	SocialLinks     map[string]string
	PreferredLocale *string
	CreatedAt       time.Time
	UpdatedAt       *time.Time
}

// Target is an exact linked Member and Identity pair.
type Target struct {
	MemberID   string
	IdentityID string
	Onboarded  bool
}

// NewsletterState is a newsletter subscription; SubscribedAt is set while
// subscribed.
type NewsletterState struct {
	Subscribed   bool
	SubscribedAt *time.Time
}

func newsletterState(subscribedAt *time.Time) NewsletterState {
	if subscribedAt == nil {
		return NewsletterState{}
	}
	return NewsletterState{Subscribed: true, SubscribedAt: subscribedAt}
}

// CookieConsent is one recorded cookie consent choice.
type CookieConsent struct {
	ID         string
	MemberID   string
	Essential  bool
	Analytics  bool
	Version    int32
	Source     string
	RecordedAt time.Time
}

// Pagination is the page an administrator list returned.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// Store errors the use cases branch on.
var (
	ErrMemberNotFound = errors.New("member not found")
	// ErrIneligible answers a target lookup whose Member has no live,
	// eligible Identity link.
	ErrIneligible    = errors.New("member is not eligible for authorization")
	ErrNicknameTaken = errors.New("member nickname is taken")
	ErrTagNotFound   = errors.New("member tag not found")
	ErrTagNameTaken  = errors.New("member tag name is taken")
)
