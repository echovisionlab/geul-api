package emaildelivery

import (
	"context"
	"log/slog"
	"maps"
	"strings"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"gorm.io/gorm"
)

type emailSiteSettings struct {
	SiteTitle       string  `gorm:"column:site_title"`
	LogoEmailFileID *string `gorm:"column:logo_email_file_id"`
}

const defaultEmailRenderLocale = localization.LocaleEnglish
const emailNotoFontStylesheetPath = "/fonts/css2?family=Noto+Sans:wght@100..900&family=Noto+Sans+Arabic:wght@100..900&family=Noto+Sans+KR:wght@100..900&family=Noto+Sans+JP:wght@100..900&family=Noto+Sans+SC:wght@100..900&family=Noto+Sans+TC:wght@100..900&family=Noto+Sans+HK:wght@100..900&family=Noto+Sans+Mono:wght@100..900&family=Noto+Color+Emoji&display=swap"

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
	settings, resolvedLogoURL := loadEmailRenderDefaults(ctx, db, cdnDomain, input)
	return buildEmailRenderDataWithDefaults(
		cdnDomain,
		siteOrigin,
		requestedLocale,
		input,
		settings,
		resolvedLogoURL,
	)
}

func loadEmailRenderDefaults(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	input map[string]string,
) (*emailSiteSettings, *string) {
	if db == nil {
		return nil, nil
	}
	var settings emailSiteSettings
	err := db.WithContext(ctx).
		Table("site_settings").
		Select("site_title, logo_email_file_id").
		Where("id = 1").
		Take(&settings).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			slog.Warn("Failed to load site settings for email defaults", "error", err)
		}
		return nil, nil
	}
	if strings.TrimSpace(input["logo_email_url"]) != "" || settings.LogoEmailFileID == nil {
		return &settings, nil
	}
	logoURL := resolveEmailLogoURL(ctx, db, cdnDomain, *settings.LogoEmailFileID)
	return &settings, &logoURL
}

func buildEmailRenderDataWithDefaults(
	cdnDomain string,
	siteOrigin string,
	requestedLocale string,
	input map[string]string,
	settings *emailSiteSettings,
	resolvedLogoURL *string,
) map[string]string {
	data := cloneEmailRenderData(input)

	if settings != nil {
		if strings.TrimSpace(data["site_name"]) == "" && strings.TrimSpace(settings.SiteTitle) != "" {
			data["site_name"] = strings.TrimSpace(settings.SiteTitle)
		}
		if strings.TrimSpace(data["logo_email_url"]) == "" && resolvedLogoURL != nil {
			data["logo_email_url"] = *resolvedLogoURL
		}
	}

	data["site_origin"] = strings.TrimRight(strings.TrimSpace(siteOrigin), "/")

	// Prevent unresolved placeholders in rendered emails.
	if _, ok := data["site_name"]; !ok {
		data["site_name"] = ""
	}
	if _, ok := data["logo_email_url"]; !ok {
		data["logo_email_url"] = ""
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
		data["email_font_stylesheet_url"] = buildEmailFontStylesheetURL(cdnDomain)
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

func emailRenderLocaleFontFamily(locale string) string {
	switch strings.ToLower(normalizeEmailRenderLocale(locale)) {
	case localization.LocaleKorean:
		return "'Noto Sans KR', 'Noto Sans', 'Noto Color Emoji', sans-serif"
	case localization.LocaleJapanese:
		return "'Noto Sans JP', 'Noto Sans', 'Noto Color Emoji', sans-serif"
	case strings.ToLower(localization.LocaleChineseSimplified):
		return "'Noto Sans SC', 'Noto Sans', 'Noto Color Emoji', sans-serif"
	case strings.ToLower(localization.LocaleChineseTraditional):
		return "'Noto Sans TC', 'Noto Sans', 'Noto Color Emoji', sans-serif"
	case localization.LocaleArabic:
		return "'Noto Sans Arabic', 'Noto Sans', 'Noto Color Emoji', sans-serif"
	default:
		return "'Noto Sans', 'Noto Color Emoji', sans-serif"
	}
}

func buildEmailFontStylesheetURL(cdnDomain string) string {
	cdnDomain = strings.TrimSpace(cdnDomain)
	cdnDomain = strings.TrimRight(cdnDomain, "/")
	if cdnDomain == "" {
		return emailNotoFontStylesheetPath
	}
	return cdnDomain + emailNotoFontStylesheetPath
}

func cloneEmailRenderData(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

func resolveEmailLogoURL(ctx context.Context, db *gorm.DB, cdnDomain, logoFileID string) string {
	logoFileID = strings.TrimSpace(logoFileID)
	if logoFileID == "" {
		return ""
	}

	asset, err := mediaasset.NewLifecycle(db, cdnDomain).
		ReadyAssetRefForSourceFile(ctx, logoFileID, "email_image")
	if err != nil {
		slog.Warn("Failed to resolve ready email logo asset", "fileId", logoFileID, "error", err)
		return ""
	}
	if asset.GetMimeType() == "" || asset.GetUrl() == "" {
		return ""
	}
	return asset.GetUrl()
}
