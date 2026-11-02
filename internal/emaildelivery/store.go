package emaildelivery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/sitelogo"
)

// MailAdapter is a stored mail adapter. Config is the stored provider
// configuration document, kept encoded: an update merges into its raw
// fields, and each reader decides what an undecodable one means.
type MailAdapter struct {
	ID        string
	Name      string
	Type      AdapterType
	IsActive  bool
	Priority  int
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt *time.Time
}

// SESConfig is the stored configuration of an Amazon SES adapter.
type SESConfig struct {
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	FromEmail       string `json:"from_email"`
	FromName        string `json:"from_name,omitempty"`
}

// SMTPConfig is the stored configuration of an SMTP adapter.
type SMTPConfig struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Secure    bool   `json:"secure"`
	User      string `json:"user"`
	Password  string `json:"password"`
	FromEmail string `json:"from_email"`
	FromName  string `json:"from_name,omitempty"`
}

// SESConfig decodes the stored configuration as SES configuration.
func (a MailAdapter) SESConfig() (SESConfig, error) {
	var config SESConfig
	err := json.Unmarshal(a.Config, &config)
	return config, err
}

// SMTPConfig decodes the stored configuration as SMTP configuration.
func (a MailAdapter) SMTPConfig() (SMTPConfig, error) {
	var config SMTPConfig
	err := json.Unmarshal(a.Config, &config)
	return config, err
}

// MailAdapterChanges are the columns an update writes. A nil field and a
// nil Config keep the stored value.
type MailAdapterChanges struct {
	Name      *string
	IsActive  *bool
	Priority  *int32
	Type      *AdapterType
	Config    json.RawMessage
	UpdatedAt time.Time
}

// MailAdapterListing is one page of adapters in priority order, then oldest
// first.
type MailAdapterListing struct {
	ActiveOnly bool
	Limit      int32
	Offset     int32
}

// Suppression is a stored delivery suppression of an address. It is active
// until ReleasedAt is set; an address has at most one active suppression.
type Suppression struct {
	ID           string
	Email        string
	Reason       string
	Source       string
	ReferenceID  *string
	LastError    *string
	SuppressedAt time.Time
	ReleasedAt   *time.Time
	ReleasedBy   *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RenderDefaults are the site settings an email falls back to, with the
// ready email logo URLs; a logo that is unset or unusable is nil.
type RenderDefaults struct {
	SiteTitle    string
	CompanyName  string
	LegalEmail   string
	ContactEmail string
	Logos        sitelogo.Email[string]
}

var (
	// ErrMailAdapterNotFound is an adapter ID that names no stored adapter.
	ErrMailAdapterNotFound = errors.New("mail adapter not found")
	// ErrSuppressionNotFound is an address without an active suppression.
	ErrSuppressionNotFound = errors.New("email suppression not found")
	// ErrSiteSettingsNotFound is a database without the site settings row.
	ErrSiteSettingsNotFound = errors.New("site settings not found")
)

// Store is what the email delivery use cases read and write, bound to one
// transaction or read snapshot. The administrator use cases reach it through
// their unit of work; the principal-less delivery steps (the worker, the
// provider callback, the renderers) bind it to the transaction or pool their
// caller holds (emaildeliverypostgres.Bind). Addresses are already
// normalized for delivery.
type Store interface {
	// InsertMailAdapter stores adapter under a new ID and returns it as
	// stored.
	InsertMailAdapter(ctx context.Context, adapter MailAdapter) (MailAdapter, error)
	// LockMailAdapter locks the adapter for update, or answers
	// ErrMailAdapterNotFound.
	LockMailAdapter(ctx context.Context, id string) (MailAdapter, error)
	// MailAdapter reads the adapter, or answers ErrMailAdapterNotFound.
	MailAdapter(ctx context.Context, id string) (MailAdapter, error)
	UpdateMailAdapter(ctx context.Context, id string, changes MailAdapterChanges) error
	DeleteMailAdapter(ctx context.Context, id string) error
	// MailAdapters returns one page of adapters and how many the listing
	// holds in all.
	MailAdapters(ctx context.Context, listing MailAdapterListing) ([]MailAdapter, int64, error)

	// ActiveSuppression reads the active suppression of email, or answers
	// ErrSuppressionNotFound.
	ActiveSuppression(ctx context.Context, email string) (Suppression, error)
	// LockActiveSuppression locks the active suppression of email for
	// update, or answers ErrSuppressionNotFound.
	LockActiveSuppression(ctx context.Context, email string) (Suppression, error)
	// ActiveSuppressions returns the active suppressions of emails.
	ActiveSuppressions(ctx context.Context, emails []string) ([]Suppression, error)
	// SuppressionRecorded reports whether a suppression of email with the
	// source, reason and reference was ever stored, released or not.
	SuppressionRecorded(ctx context.Context, email, source, reason, referenceID string) (bool, error)
	// InsertSuppression stores a new active suppression; an address that has
	// one already is refused.
	InsertSuppression(ctx context.Context, suppression Suppression) error
	// ReplaceSuppression rewrites the reason, source, reference, last error
	// and suppression time of suppression id.
	ReplaceSuppression(ctx context.Context, id string, replacement Suppression) error
	ReleaseSuppression(ctx context.Context, id string, releasedAt time.Time, releasedBy *string) error

	// EmailRenderDefaults reads the site settings an email falls back to
	// and resolves its email logos on cdnDomain, or answers
	// ErrSiteSettingsNotFound.
	EmailRenderDefaults(ctx context.Context, cdnDomain string) (RenderDefaults, error)
	// RecipientLocale is the stored preferred locale of the active Member
	// whose primary email is recipient, or "".
	RecipientLocale(ctx context.Context, recipient string) (string, error)
	// IdentityLocale is the stored preferred locale of the active Member
	// linked to the Account identity, or "".
	IdentityLocale(ctx context.Context, identityID string) (string, error)
}
