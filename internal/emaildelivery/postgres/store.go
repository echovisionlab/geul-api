// Package emaildeliverypostgres is the PostgreSQL Store of the email
// delivery use cases: the mail_adapter and email_suppression rows, and the
// site settings, media and Member locale reads an email renders with.
package emaildeliverypostgres

import (
	"context"
	stderrors "errors"
	"log/slog"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sitelogo"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// NewUnits is the production unit of work of the email delivery use cases.
func NewUnits(db *gorm.DB) mutation.UnitOfWork[emaildelivery.Store] {
	return mutation.Postgres(db, Bind)
}

// Bind binds the Store to db: the transaction a principal-less delivery step
// runs in (the worker's and the provider callback's suppression writes), or
// the pool for a read of one statement (a renderer's locale and site
// defaults, the worker's suppression lookups).
func Bind(db *gorm.DB) emaildelivery.Store {
	return store{tx: db}
}

// store is the email delivery Store bound to one transaction or the pool.
type store struct{ tx *gorm.DB }

func mailAdapterNotFound(err error) error {
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return emaildelivery.ErrMailAdapterNotFound
	}
	return err
}

func suppressionNotFound(err error) error {
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return emaildelivery.ErrSuppressionNotFound
	}
	return err
}

func (s store) InsertMailAdapter(_ context.Context, adapter emaildelivery.MailAdapter) (emaildelivery.MailAdapter, error) {
	row := rowFromMailAdapter(adapter)
	if err := s.tx.Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return emaildelivery.MailAdapter{}, err
	}
	return mailAdapterFromRow(row), nil
}

func (s store) LockMailAdapter(_ context.Context, id string) (emaildelivery.MailAdapter, error) {
	var row model.MailAdapter
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = ?", id).Error; err != nil {
		return emaildelivery.MailAdapter{}, mailAdapterNotFound(err)
	}
	return mailAdapterFromRow(row), nil
}

func (s store) MailAdapter(ctx context.Context, id string) (emaildelivery.MailAdapter, error) {
	var row model.MailAdapter
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return emaildelivery.MailAdapter{}, mailAdapterNotFound(err)
	}
	return mailAdapterFromRow(row), nil
}

func (s store) UpdateMailAdapter(_ context.Context, id string, changes emaildelivery.MailAdapterChanges) error {
	updates := structured.Fields{"updated_at": changes.UpdatedAt}
	if changes.Name != nil {
		updates["name"] = *changes.Name
	}
	if changes.IsActive != nil {
		updates["is_active"] = *changes.IsActive
	}
	if changes.Priority != nil {
		updates["priority"] = *changes.Priority
	}
	if changes.Type != nil {
		updates["type"] = model.MailAdapterType(*changes.Type)
	}
	if changes.Config != nil {
		updates["config"] = model.MailAdapterConfig{RawMessage: changes.Config}
	}
	return s.tx.Model(&model.MailAdapter{ID: id}).Updates(updates).Error
}

func (s store) DeleteMailAdapter(_ context.Context, id string) error {
	return s.tx.Delete(&model.MailAdapter{ID: id}).Error
}

func (s store) MailAdapters(ctx context.Context, listing emaildelivery.MailAdapterListing) ([]emaildelivery.MailAdapter, int64, error) {
	list := s.tx.WithContext(ctx).Model(&model.MailAdapter{})
	if listing.ActiveOnly {
		list = list.Where("is_active = ?", true)
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.MailAdapter
	if err := list.Order("priority ASC, created_at ASC").Limit(int(listing.Limit)).Offset(int(listing.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	adapters := make([]emaildelivery.MailAdapter, len(rows))
	for i, row := range rows {
		adapters[i] = mailAdapterFromRow(row)
	}
	return adapters, total, nil
}

func (s store) ActiveSuppression(ctx context.Context, email string) (emaildelivery.Suppression, error) {
	var row model.EmailSuppression
	if err := s.tx.WithContext(ctx).
		Where("LOWER(email) = ? AND released_at IS NULL", email).
		First(&row).Error; err != nil {
		return emaildelivery.Suppression{}, suppressionNotFound(err)
	}
	return suppressionFromRow(row), nil
}

func (s store) LockActiveSuppression(ctx context.Context, email string) (emaildelivery.Suppression, error) {
	var row model.EmailSuppression
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("LOWER(email) = ? AND released_at IS NULL", email).
		First(&row).Error; err != nil {
		return emaildelivery.Suppression{}, suppressionNotFound(err)
	}
	return suppressionFromRow(row), nil
}

func (s store) ActiveSuppressions(ctx context.Context, emails []string) ([]emaildelivery.Suppression, error) {
	var rows []model.EmailSuppression
	if err := s.tx.WithContext(ctx).
		Where("LOWER(email) IN ? AND released_at IS NULL", emails).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	suppressions := make([]emaildelivery.Suppression, len(rows))
	for i, row := range rows {
		suppressions[i] = suppressionFromRow(row)
	}
	return suppressions, nil
}

func (s store) SuppressionRecorded(ctx context.Context, email, source, reason, referenceID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).Model(&model.EmailSuppression{}).
		Where("LOWER(email) = ? AND source = ? AND reason = ? AND reference_id = ?", email, source, reason, referenceID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s store) InsertSuppression(ctx context.Context, suppression emaildelivery.Suppression) error {
	return s.tx.WithContext(ctx).Create(&model.EmailSuppression{
		Email:        suppression.Email,
		Reason:       suppression.Reason,
		Source:       suppression.Source,
		ReferenceID:  suppression.ReferenceID,
		LastError:    suppression.LastError,
		SuppressedAt: suppression.SuppressedAt,
	}).Error
}

func (s store) ReplaceSuppression(ctx context.Context, id string, replacement emaildelivery.Suppression) error {
	return s.tx.WithContext(ctx).Model(&model.EmailSuppression{ID: id}).Updates(structured.Fields{
		"reason":        replacement.Reason,
		"source":        replacement.Source,
		"reference_id":  replacement.ReferenceID,
		"last_error":    replacement.LastError,
		"suppressed_at": replacement.SuppressedAt,
	}).Error
}

func (s store) ReleaseSuppression(_ context.Context, id string, releasedAt time.Time, releasedBy *string) error {
	return s.tx.Model(&model.EmailSuppression{ID: id}).Updates(map[string]any{
		"released_at": releasedAt,
		"released_by": releasedBy,
	}).Error
}

// renderSiteSettings are the site settings columns an email renders with.
type renderSiteSettings struct {
	CompanyName          string  `gorm:"column:company_name"`
	ContactEmail         string  `gorm:"column:contact_email"`
	LegalEmail           string  `gorm:"column:legal_email"`
	SiteTitle            string  `gorm:"column:site_title"`
	LogoEmailLightFileID *string `gorm:"column:logo_email_light_file_id"`
	LogoEmailDarkFileID  *string `gorm:"column:logo_email_dark_file_id"`
}

func (s store) EmailRenderDefaults(ctx context.Context, cdnDomain string) (emaildelivery.RenderDefaults, error) {
	var settings renderSiteSettings
	err := s.tx.WithContext(ctx).
		Table("site_settings").
		Select("site_title, company_name, legal_email, contact_email, logo_email_light_file_id, logo_email_dark_file_id").
		Where("id = 1").
		Take(&settings).Error
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return emaildelivery.RenderDefaults{}, emaildelivery.ErrSiteSettingsNotFound
	}
	if err != nil {
		return emaildelivery.RenderDefaults{}, err
	}
	return emaildelivery.RenderDefaults{
		SiteTitle: settings.SiteTitle, CompanyName: settings.CompanyName,
		LegalEmail: settings.LegalEmail, ContactEmail: settings.ContactEmail,
		Logos: sitelogo.Email[string]{
			Light: s.readyEmailLogoURL(ctx, cdnDomain, settings.LogoEmailLightFileID),
			Dark:  s.readyEmailLogoURL(ctx, cdnDomain, settings.LogoEmailDarkFileID),
		},
	}, nil
}

// readyEmailLogoURL returns the ready email logo URL for a slot, or nil when
// the slot is unset or unusable so the other theme can stand in.
func (s store) readyEmailLogoURL(ctx context.Context, cdnDomain string, logoFileID *string) *string {
	if logoFileID == nil || strings.TrimSpace(*logoFileID) == "" {
		return nil
	}
	asset, _, err := mediaasset.BrandingResultProto(mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), cdnDomain).
		ReadyBrandingAssetRef(ctx, strings.TrimSpace(*logoFileID), mediaasset.BrandingKindEmailImage))
	if err != nil {
		slog.WarnContext(ctx, "Failed to resolve ready email logo asset", "fileId", *logoFileID, failurecause.Attr(err))
		return nil
	}
	if asset.GetMimeType() == "" || asset.GetUrl() == "" {
		return nil
	}
	url := asset.GetUrl()
	return &url
}

func (s store) RecipientLocale(ctx context.Context, recipient string) (string, error) {
	return s.memberLocale(ctx, "m.primary_email = ?", recipient)
}

func (s store) IdentityLocale(ctx context.Context, identityID string) (string, error) {
	return s.memberLocale(ctx, "m.account_identity_id = ?", identityID)
}

// memberLocale reads the preferred locale of the active Member that where
// selects, or "" when there is none or it has no locale.
func (s store) memberLocale(ctx context.Context, where string, value string) (string, error) {
	var row struct {
		PreferredLocale *string `gorm:"column:preferred_locale"`
	}
	err := s.tx.WithContext(ctx).
		Table("member AS m").
		Joins(identityread.JoinLinked("m", "ki")).
		Select("NULLIF(m.preferred_locale, '') AS preferred_locale").
		Where("ki.state = 'active' AND m.deleted_at IS NULL AND "+where, value).
		Limit(1).
		Scan(&row).Error
	if err != nil || row.PreferredLocale == nil {
		return "", err
	}
	return *row.PreferredLocale, nil
}

func mailAdapterFromRow(row model.MailAdapter) emaildelivery.MailAdapter {
	return emaildelivery.MailAdapter{
		ID: row.ID, Name: row.Name, Type: emaildelivery.AdapterType(row.Type), IsActive: row.IsActive,
		Priority: row.Priority, Config: row.Config.RawMessage, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func rowFromMailAdapter(adapter emaildelivery.MailAdapter) model.MailAdapter {
	return model.MailAdapter{
		ID: adapter.ID, Name: adapter.Name, Type: model.MailAdapterType(adapter.Type), IsActive: adapter.IsActive,
		Priority: adapter.Priority, Config: model.MailAdapterConfig{RawMessage: adapter.Config},
		CreatedAt: adapter.CreatedAt, UpdatedAt: adapter.UpdatedAt,
	}
}

func suppressionFromRow(row model.EmailSuppression) emaildelivery.Suppression {
	return emaildelivery.Suppression{
		ID: row.ID, Email: row.Email, Reason: row.Reason, Source: row.Source,
		ReferenceID: row.ReferenceID, LastError: row.LastError, SuppressedAt: row.SuppressedAt,
		ReleasedAt: row.ReleasedAt, ReleasedBy: row.ReleasedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
