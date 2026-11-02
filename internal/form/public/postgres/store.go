// Package formpublicpostgres is the PostgreSQL public form Store: the form
// tables a visitor reads and submits to, and the media, Member and Share
// Link runtimes a public Form read drives.
package formpublicpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/form"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"gorm.io/gorm"

	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/form/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// Assets projects Form-owned File and OG references for public reads.
type Assets interface {
	LocalizedOGDisposition(ctx context.Context, db *gorm.DB, formID, locale string) (form.LocalizedOGDisposition, error)
	ResolvedOG(ctx context.Context, db *gorm.DB, sourceID, localizedID *string) (*mediaasset.Reference, error)
	// FeaturedImage resolves the Form's ready featured image; a failure is
	// logged and answers nil.
	FeaturedImage(ctx context.Context, db *gorm.DB, formID string) *mediaasset.Reference
}

// Members reads the Member rows public Forms need; Member owns them.
type Members interface {
	// PreferredLocale returns the preferred locale of a live Member, or nil.
	PreferredLocale(ctx context.Context, db *gorm.DB, memberID string) (*string, error)
}

// ShareLinks reads the share links that open a Form or its dashboard; Share
// Link owns them.
type ShareLinks interface {
	// FindShareLink returns the share link of token, or
	// gorm.ErrRecordNotFound.
	FindShareLink(ctx context.Context, db *gorm.DB, token string) (*model.ShareLink, error)
}

// Ports are the other domains and shared runtimes a public Form read drives.
type Ports struct {
	Assets     Assets
	Members    Members
	ShareLinks ShareLinks
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Assets": p.Assets == nil, "Members": p.Members == nil, "ShareLinks": p.ShareLinks == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public form store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the public Form use
// cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[public.Store], error) {
	if db == nil {
		return nil, errors.New("public form store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) public.Store { return NewStore(tx, ports) }), nil
}

// NewStore binds the Store to tx.
func NewStore(tx *gorm.DB, ports Ports) public.Store {
	return store{tx: tx, ports: ports}
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ public.Store = store{}

func (s store) FindForm(ctx context.Context, locator form.Locator, value string) (form.Form, error) {
	return formpostgres.FindForm(ctx, s.tx, locator, value)
}

func (s store) LockForm(ctx context.Context, formID string) (form.Form, error) {
	return formpostgres.LockForm(ctx, s.tx, formID)
}

func (s store) SourceLocale(ctx context.Context, formID string) (string, error) {
	return formpostgres.SourceLocale(ctx, s.tx, formID)
}

func (s store) LocaleState(ctx context.Context, formID, locale string) (form.TranslationDocumentState, error) {
	return formpostgres.LocaleState(ctx, s.tx, formID, locale)
}

func (s store) LocalizationRows(ctx context.Context, formID string, locales []string) ([]form.LocalizationRow, error) {
	return formpostgres.LocalizationRows(ctx, s.tx, formID, locales)
}

func (s store) RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	return translation.LoadRuntimeSettings(ctx, s.tx)
}

func (s store) CountSubmissions(ctx context.Context, formID string) (int64, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Model(&model.FormSubmission{}).
		Where("form_id = ?", formID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (s store) HasMemberSubmission(ctx context.Context, formID, memberID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Model(&model.FormSubmission{}).
		Where("form_id = ? AND member_id = ?", formID, memberID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s store) InsertSubmission(ctx context.Context, submission *form.Submission) error {
	return formpostgres.InsertSubmission(ctx, s.tx, submission)
}

func (s store) Submissions(ctx context.Context, formID string) ([]form.Submission, error) {
	var rows []model.FormSubmission
	if err := s.tx.WithContext(ctx).
		Where("form_id = ?", formID).
		Order("created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	submissions := make([]form.Submission, len(rows))
	for i, row := range rows {
		submissions[i] = formpostgres.SubmissionOf(row)
	}
	return submissions, nil
}

func (s store) ShareLink(ctx context.Context, token string) (public.ShareLink, error) {
	link, err := s.ports.ShareLinks.FindShareLink(ctx, s.tx, token)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return public.ShareLink{}, public.ErrShareLinkNotFound
		}
		return public.ShareLink{}, err
	}
	return public.ShareLink{
		EntityID: link.EntityID, EntityType: link.EntityType, ExpiresAt: link.ExpiresAt, PasswordHash: link.PasswordHash,
	}, nil
}

func (s store) PreferredLocale(ctx context.Context, memberID string) (*string, error) {
	return s.ports.Members.PreferredLocale(ctx, s.tx, memberID)
}

func (s store) LocalizedOGDisposition(ctx context.Context, formID, locale string) (form.LocalizedOGDisposition, error) {
	return s.ports.Assets.LocalizedOGDisposition(ctx, s.tx, formID, locale)
}

func (s store) ReadyOGAsset(ctx context.Context, localizedID, sourceID *string) (*form.Asset, error) {
	asset, err := s.ports.Assets.ResolvedOG(ctx, s.tx, sourceID, localizedID)
	return formpostgres.AssetOf(asset), err
}

func (s store) FeaturedImage(ctx context.Context, formID string) *form.Asset {
	return formpostgres.AssetOf(s.ports.Assets.FeaturedImage(ctx, s.tx, formID))
}
