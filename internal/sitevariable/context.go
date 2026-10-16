// Package sitevariable resolves authored references against trusted site settings.
package sitevariable

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

type Context struct {
	SiteTitle, SiteURL, CompanyName        string
	LegalEmail, SupportEmail, PrivacyEmail string
	// ContactEmail distinguishes an explicit empty unified value from older contexts.
	ContactEmail *string
}

func FromSettings(settings model.SiteSettings, origin string) Context {
	return Context{SiteTitle: settings.SiteTitle, SiteURL: strings.TrimRight(strings.TrimSpace(origin), "/"), CompanyName: settings.CompanyName, LegalEmail: settings.LegalEmail, ContactEmail: &settings.ContactEmail}
}

func Load(ctx context.Context, db *gorm.DB, origin string) (Context, error) {
	var settings model.SiteSettings
	if db == nil {
		return Context{}, fmt.Errorf("site settings database is required")
	}
	if err := db.WithContext(ctx).Select("site_title", "company_name", "legal_email", "contact_email").Where("id = 1").Take(&settings).Error; err != nil {
		return Context{}, err
	}
	return FromSettings(settings, origin), nil
}

func LoadRuntime(ctx context.Context, db *gorm.DB) (Context, error) {
	return Load(ctx, db, os.Getenv("SITE_ORIGIN"))
}

func (c Context) Values() map[string]string {
	contact := c.SupportEmail
	if contact == "" {
		contact = c.PrivacyEmail
	}
	support, privacy := c.SupportEmail, c.PrivacyEmail
	if c.ContactEmail != nil {
		contact, support, privacy = *c.ContactEmail, *c.ContactEmail, *c.ContactEmail
	}
	return map[string]string{"site.title": c.SiteTitle, "site.url": c.SiteURL, "site.companyName": c.CompanyName, "contact.legalEmail": c.LegalEmail, "contact.email": contact, "contact.supportEmail": support, "contact.privacyEmail": privacy}
}

func (c Context) Resolve(key string) (string, error) {
	value, ok := c.Values()[key]
	if !ok {
		return "", fmt.Errorf("unknown site variable %q", key)
	}
	return value, nil
}

var tokenPattern = regexp.MustCompile(`\{\{(?:site|contact)\.[^{}]+\}\}`)

// ResolveText performs exactly one expansion; setting values remain literal data.
func (c Context) ResolveText(source string) string {
	return tokenPattern.ReplaceAllStringFunc(source, func(token string) string {
		value, err := c.Resolve(token[2 : len(token)-2])
		if err != nil {
			slog.WarnContext(context.Background(), "content operation failed", "domain", "sitevariable", "operation", "sitevariable.ResolveText", "outcome", "failed", failurecause.Attr(err))
			return token
		}
		return value
	})
}

// ResolvePublicText loads settings only when a public projection needs them.
func ResolvePublicText(ctx context.Context, db *gorm.DB, source string) (string, error) {
	if !tokenPattern.MatchString(source) {
		return source, nil
	}
	c, err := LoadRuntime(ctx, db)
	if err != nil {
		return "", err
	}
	return c.ResolveText(source), nil
}

// TemplateContext keeps email projections dynamic until trusted delivery data is bound.
func TemplateContext() Context {
	contact := "{{contact.email}}"
	return Context{SiteTitle: "{{site.title}}", SiteURL: "{{site.url}}", CompanyName: "{{site.companyName}}", LegalEmail: "{{contact.legalEmail}}", ContactEmail: &contact}
}
