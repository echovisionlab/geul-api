package emaildelivery

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/sitelogo"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
)

// Email logo template variables. logo_email_url is the light logo (dark when no
// light logo is set); logo_email_dark_url is the dark logo (light when unset).
const (
	emailLogoURLVariable     = "logo_email_url"
	emailLogoDarkURLVariable = "logo_email_dark_url"
)

const defaultEmailRenderLocale = localization.LocaleEnglish

// emailFontBaseURL serves geul-web's Geul font system with public CORS, so mail
// clients can load it from any origin. Each profile alias carries the Han forms
// of its locale; Arabic and Thai are part of Geul Sans.
const emailFontBaseURL = "https://static.rawsector.com/fonts"

// BuildEmailRenderData merges explicit template data with site-level defaults,
// which store reads from the site settings. site_origin is always projected
// from runtime configuration and cannot be overridden by template input or
// persisted settings.
func BuildEmailRenderData(
	ctx context.Context,
	store Store,
	cdnDomain string,
	siteOrigin string,
	requestedLocale string,
	input map[string]string,
) map[string]string {
	return buildEmailRenderDataWithDefaults(
		siteOrigin,
		requestedLocale,
		input,
		loadEmailRenderDefaults(ctx, store, cdnDomain),
	)
}

// loadEmailRenderDefaults reads the site defaults, or nil when they cannot be
// read; an email then renders without them.
func loadEmailRenderDefaults(ctx context.Context, store Store, cdnDomain string) *RenderDefaults {
	defaults, err := store.EmailRenderDefaults(ctx, cdnDomain)
	if err != nil {
		if !errors.Is(err, ErrSiteSettingsNotFound) {
			slog.WarnContext(ctx, "Failed to load site settings for email defaults", failurecause.Attr(err))
		}
		return nil
	}
	return &defaults
}

func buildEmailRenderDataWithDefaults(
	siteOrigin string,
	requestedLocale string,
	input map[string]string,
	settings *RenderDefaults,
) map[string]string {
	data := cloneEmailRenderData(input)

	if settings != nil {
		if strings.TrimSpace(data["site_name"]) == "" && strings.TrimSpace(settings.SiteTitle) != "" {
			data["site_name"] = strings.TrimSpace(settings.SiteTitle)
		}
		for variable, url := range map[string]*string{
			emailLogoURLVariable:     settings.Logos.Resolve(sitelogo.Light),
			emailLogoDarkURLVariable: settings.Logos.Resolve(sitelogo.Dark),
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
	trusted := sitevariable.Context{SiteURL: strings.TrimRight(strings.TrimSpace(siteOrigin), "/")}
	if settings != nil {
		trusted.SiteTitle, trusted.CompanyName = settings.SiteTitle, settings.CompanyName
		trusted.LegalEmail, trusted.ContactEmail = settings.LegalEmail, settings.ContactEmail
	}
	for key, value := range trusted.Values() {
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
