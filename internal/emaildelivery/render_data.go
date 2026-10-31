package emaildelivery

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitelogo"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"gorm.io/gorm"
)

type emailSiteSettings struct {
	CompanyName          string  `gorm:"column:company_name"`
	ContactEmail         string  `gorm:"column:contact_email"`
	LegalEmail           string  `gorm:"column:legal_email"`
	SiteTitle            string  `gorm:"column:site_title"`
	LogoEmailLightFileID *string `gorm:"column:logo_email_light_file_id"`
	LogoEmailDarkFileID  *string `gorm:"column:logo_email_dark_file_id"`
}

// Email logo template variables. logo_email_url is the light logo (dark when no
// light logo is set); logo_email_dark_url is the dark logo (light when unset).
const (
	emailLogoURLVariable     = "logo_email_url"
	emailLogoDarkURLVariable = "logo_email_dark_url"
)

type emailLogoURLs = sitelogo.Email[string]

const defaultEmailRenderLocale = localization.LocaleEnglish

// emailFontBaseURL serves geul-web's Geul font system with public CORS, so mail
// clients can load it from any origin. Each profile alias carries the Han forms
// of its locale; Arabic and Thai are part of Geul Sans.
const emailFontBaseURL = "https://static.rawsector.com/fonts"

// BuildEmailRenderData merges explicit template data with site-level defaults.
// site_origin is always projected from runtime configuration and cannot be
// overridden by template input or persisted settings.
func BuildEmailRenderData(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	siteOrigin string,
	requestedLocale string,
	input map[string]string,
) map[string]string {
	settings, logos := loadEmailRenderDefaults(ctx, db, cdnDomain)
	return buildEmailRenderDataWithDefaults(
		cdnDomain,
		siteOrigin,
		requestedLocale,
		input,
		settings,
		logos,
	)
}

func loadEmailRenderDefaults(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
) (*emailSiteSettings, emailLogoURLs) {
	if db == nil {
		return nil, emailLogoURLs{}
	}
	var settings emailSiteSettings
	err := db.WithContext(ctx).
		Table("site_settings").
		Select("site_title, company_name, legal_email, contact_email, logo_email_light_file_id, logo_email_dark_file_id").
		Where("id = 1").
		Take(&settings).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("Failed to load site settings for email defaults", "error", err)
		}
		return nil, emailLogoURLs{}
	}
	return &settings, emailLogoURLs{
		Light: resolveEmailLogoURL(ctx, db, cdnDomain, settings.LogoEmailLightFileID),
		Dark:  resolveEmailLogoURL(ctx, db, cdnDomain, settings.LogoEmailDarkFileID),
	}
}

func buildEmailRenderDataWithDefaults(
	cdnDomain string,
	siteOrigin string,
	requestedLocale string,
	input map[string]string,
	settings *emailSiteSettings,
	logos emailLogoURLs,
) map[string]string {
	data := cloneEmailRenderData(input)

	if settings != nil {
		if strings.TrimSpace(data["site_name"]) == "" && strings.TrimSpace(settings.SiteTitle) != "" {
			data["site_name"] = strings.TrimSpace(settings.SiteTitle)
		}
		for variable, url := range map[string]*string{
			emailLogoURLVariable:     logos.Resolve(sitelogo.Light),
			emailLogoDarkURLVariable: logos.Resolve(sitelogo.Dark),
		} {
			if strings.TrimSpace(data[variable]) == "" && url != nil {
				data[variable] = *url
			}
		}
	}

	// Remove case aliases before binding the reserved namespace to trusted values.
	for key := range data {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "site.") || strings.HasPrefix(lower, "contact.") {
			delete(data, key)
		}
	}
	trusted := model.SiteSettings{}
	if settings != nil {
		trusted = model.SiteSettings{SiteTitle: settings.SiteTitle, CompanyName: settings.CompanyName, LegalEmail: settings.LegalEmail, ContactEmail: settings.ContactEmail}
	}
	for key, value := range sitevariable.FromSettings(trusted, siteOrigin).Values() {
		data[key] = value
	}
	data["site_origin"] = strings.TrimRight(strings.TrimSpace(siteOrigin), "/")

	// Prevent unresolved placeholders in rendered emails.
	if _, ok := data["site_name"]; !ok {
		data["site_name"] = ""
	}
	for _, variable := range []string{emailLogoURLVariable, emailLogoDarkURLVariable} {
		if _, ok := data[variable]; !ok {
			data[variable] = ""
		}
	}

	normalizedLocale := normalizeEmailRenderLocale(requestedLocale)
	if _, ok := data["email_lang"]; !ok || strings.TrimSpace(data["email_lang"]) == "" {
		data["email_lang"] = normalizedLocale
	}
	if _, ok := data["email_direction"]; !ok || strings.TrimSpace(data["email_direction"]) == "" {
		data["email_direction"] = emailRenderLocaleDirection(normalizedLocale)
	}
	if _, ok := data["email_font_family"]; !ok || strings.TrimSpace(data["email_font_family"]) == "" {
		data["email_font_family"] = emailRenderLocaleFontFamily(normalizedLocale)
	}
	if _, ok := data["email_font_stylesheet_url"]; !ok || strings.TrimSpace(data["email_font_stylesheet_url"]) == "" {
		data["email_font_stylesheet_url"] = buildEmailFontStylesheetURL(normalizedLocale)
	}

	normalizeEmailRecipientAliases(data)

	return data
}

func normalizeEmailRecipientAliases(data map[string]string) {
	if data == nil {
		return
	}

	primaryEmail := firstNonEmptyEmailValue(
		data["recipient_email"],
		data["identity_email"],
		data["to"],
	)
	if primaryEmail != "" {
		for _, key := range []string{"recipient_email", "identity_email", "to"} {
			if strings.TrimSpace(data[key]) == "" {
				data[key] = primaryEmail
			}
		}
	}

	primaryName := firstNonEmptyTextValue(
		data["name"],
		data["recipient_name"],
		data["identity_name"],
	)
	if primaryName == "" && primaryEmail != "" {
		primaryName = fallbackNameFromEmail(primaryEmail)
	}
	if primaryName == "" {
		return
	}

	for _, key := range []string{"name", "recipient_name", "identity_name"} {
		if strings.TrimSpace(data[key]) == "" {
			data[key] = primaryName
		}
	}
}

func firstNonEmptyTextValue(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyEmailValue(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func fallbackNameFromEmail(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return ""
	}
	localPart, _, found := strings.Cut(email, "@")
	if !found {
		return ""
	}
	localPart = strings.TrimSpace(localPart)
	if localPart == "" {
		return ""
	}
	return localPart
}

func normalizeEmailRenderLocale(locale string) string {
	return localization.NormalizeWithDefault(locale, defaultEmailRenderLocale)
}

func emailRenderLocaleDirection(locale string) string {
	switch strings.ToLower(normalizeEmailRenderLocale(locale)) {
	case "ar":
		return "rtl"
	default:
		return "ltr"
	}
}

func emailRenderLocaleFontProfile(locale string) (profile string, han string) {
	switch strings.ToLower(normalizeEmailRenderLocale(locale)) {
	case localization.LocaleKorean:
		return "ko", "Geul Sans Han TC"
	case localization.LocaleJapanese:
		return "ja", "Geul Sans Han JP"
	case strings.ToLower(localization.LocaleChineseSimplified):
		return "zh-hans", "Geul Sans Han SC"
	case strings.ToLower(localization.LocaleChineseTraditional):
		return "zh-hant", "Geul Sans Han TC"
	default:
		return "base", ""
	}
}

func emailRenderLocaleFontFamily(locale string) string {
	if _, han := emailRenderLocaleFontProfile(locale); han != "" {
		return "'Geul Sans', '" + han + "', 'Geul Emoji', sans-serif"
	}
	return "'Geul Sans', 'Geul Emoji', sans-serif"
}

func buildEmailFontStylesheetURL(locale string) string {
	profile, _ := emailRenderLocaleFontProfile(locale)
	return emailFontBaseURL + "/geul-" + profile + ".css"
}

func cloneEmailRenderData(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

// resolveEmailLogoURL returns the ready email logo URL for a slot, or nil when
// the slot is unset or unusable so the other theme can stand in.
func resolveEmailLogoURL(ctx context.Context, db *gorm.DB, cdnDomain string, logoFileID *string) *string {
	if logoFileID == nil || strings.TrimSpace(*logoFileID) == "" {
		return nil
	}
	asset, _, err := mediaasset.NewLifecycle(db, cdnDomain).
		ReadyBrandingAssetRef(ctx, strings.TrimSpace(*logoFileID), mediaasset.BrandingKindEmailImage)
	if err != nil {
		slog.Warn("Failed to resolve ready email logo asset", "fileId", *logoFileID, "error", err)
		return nil
	}
	if asset.GetMimeType() == "" || asset.GetUrl() == "" {
		return nil
	}
	url := asset.GetUrl()
	return &url
}
