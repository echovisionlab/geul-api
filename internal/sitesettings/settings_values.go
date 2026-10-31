package sitesettings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/model"
)

var defaultSettings = siteSettingObject{
	"site_title":                    "",
	"company_name":                  "",
	"company_address":               "",
	"tax_id":                        "",
	"legal_email":                   "",
	"contact_email":                 "",
	"social_links":                  siteSettingObject{},
	"logo_full_light_file_id":       nil,
	"logo_full_dark_file_id":        nil,
	"logo_symbol_light_file_id":     nil,
	"logo_symbol_dark_file_id":      nil,
	"logo_email_light_file_id":      nil,
	"logo_email_dark_file_id":       nil,
	"favicon_file_id":               nil,
	"site_og_background_file_id":    nil,
	"privacy_og_background_file_id": nil,
	"terms_og_background_file_id":   nil,
	"primary_color":                 "#b02d23",
	"default_comments_enabled":      true,
	"homepage_page_id":              nil,
	"menu_header_id":                nil,
	"menu_secondary_id":             nil,
	"menu_footer_id":                nil,
	"menu_avatar_dropdown_id":       nil,
	"meta_description":              "",
	"google_analytics_id":           nil,
	"og_image_config":               nil,
	"og_image_config.home":          nil,
	"og_image_config.content":       nil,
}

var (
	siteSettingEmailPattern     = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	siteSettingHexColorPattern  = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	siteSettingAnalyticsPattern = regexp.MustCompile(`^(G|UA|GT)-[A-Z0-9]+$`)
)

func isSiteSettingMenuReference(key string) bool {
	switch key {
	case "menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id":
		return true
	default:
		return false
	}
}

func siteSettingReferenceValue(settings *model.SiteSettings, key string) *string {
	switch key {
	case "homepage_page_id":
		return settings.HomepagePageID
	case "menu_header_id":
		return settings.MenuHeaderID
	case "menu_secondary_id":
		return settings.MenuSecondaryID
	case "menu_footer_id":
		return settings.MenuFooterID
	case "menu_avatar_dropdown_id":
		return settings.MenuAvatarDropdownID
	default:
		return nil
	}
}

func validateSiteSettingEmail(key, value string) error {
	if value == "" || siteSettingEmailPattern.MatchString(value) {
		return nil
	}
	return fmt.Errorf("%s must be a valid email address", key)
}

// jsonColumnToMap decodes a stored JSON object column. An undecodable column
// yields the empty fallback together with the decode error, so a read that
// serves the fallback can log it.
func jsonColumnToMap(raw []byte, emptyDefault bool) (siteSettingObject, error) {
	if len(raw) == 0 {
		if emptyDefault {
			return siteSettingObject{}, nil
		}
		return nil, nil
	}

	var result siteSettingObject
	if err := json.Unmarshal(raw, &result); err != nil {
		if emptyDefault {
			return siteSettingObject{}, err
		}
		return nil, err
	}

	if result == nil && emptyDefault {
		return siteSettingObject{}, nil
	}

	return result, nil
}

func jsonColumnToStruct(raw []byte) (*structpb.Struct, error) {
	m, err := jsonColumnToMap(raw, false)
	if err != nil || len(m) == 0 {
		return nil, err
	}
	return structpb.NewStruct(m)
}

func optionalString(v *string) siteSettingValue {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

func parseString(value siteSettingValue) (string, error) {
	if value == nil {
		return "", nil
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("expected string")
	}
	return str, nil
}

func parseOptionalString(value siteSettingValue) (*string, error) {
	if value == nil {
		return nil, nil
	}

	str, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("expected string or null")
	}
	if str == "" {
		return nil, nil
	}
	return &str, nil
}

func parseBool(value siteSettingValue) (bool, error) {
	if value == nil {
		return false, nil
	}

	v, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("expected boolean")
	}
	return v, nil
}

func parseJSONObjectBytes(value siteSettingValue, nullable bool) ([]byte, error) {
	if value == nil {
		if nullable {
			return nil, nil
		}
		return []byte("{}"), nil
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON object: %w", err)
	}

	var object siteSettingObject
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, fmt.Errorf("expected JSON object")
	}

	if object == nil {
		if nullable {
			return nil, nil
		}
		return []byte("{}"), nil
	}

	return payload, nil
}

func parseOgImageConfigRoot(raw []byte) (siteSettingObject, error) {
	root := siteSettingObject{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil || root == nil {
			return nil, fmt.Errorf("stored og_image_config must be a JSON object")
		}
	}
	for _, section := range []string{"home", "content"} {
		value, exists := root[section]
		if !exists || value == nil {
			// Empty sections intentionally mean "use renderer defaults". Keeping
			// both keys present also preserves the public config shape.
			root[section] = siteSettingObject{}
			continue
		}
		object, ok := value.(siteSettingObject)
		if !ok {
			return nil, fmt.Errorf("stored og_image_config.%s must be a JSON object", section)
		}
		root[section] = object
	}
	return root, nil
}

func applyOgImageConfigSection(settings *model.SiteSettings, section string, value siteSettingValue) error {
	payload, err := parseJSONObjectBytes(value, false)
	if err != nil {
		return fmt.Errorf("og_image_config.%s: %w", section, err)
	}
	var object siteSettingObject
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return fmt.Errorf("og_image_config.%s must be a JSON object", section)
	}
	root, err := parseOgImageConfigRoot(settings.OGImageConfig)
	if err != nil {
		return err
	}
	root[section] = object
	settings.OGImageConfig, err = json.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal og_image_config: %w", err)
	}
	return nil
}

// settingValue returns the comparable value of a setting. Comparisons treat an
// undecodable stored value as its fallback; GetSetting, which serves the
// value, logs the decode error.
func settingValue(settings *model.SiteSettings, key string) (siteSettingValue, bool) {
	value, known, _ := decodedSettingValue(settings, key)
	return value, known
}

// decodedSettingValue is settingValue with the error that replaced an
// undecodable stored value by its fallback.
func decodedSettingValue(settings *model.SiteSettings, key string) (siteSettingValue, bool, error) {
	switch key {
	case "site_title":
		return settings.SiteTitle, true, nil
	case "company_name":
		return settings.CompanyName, true, nil
	case "company_address":
		return settings.CompanyAddress, true, nil
	case "tax_id":
		return settings.TaxID, true, nil
	case "legal_email":
		return settings.LegalEmail, true, nil
	case "contact_email":
		return settings.ContactEmail, true, nil
	case "social_links":
		value, err := jsonColumnToMap(settings.SocialLinks, true)
		return value, true, err
	case "logo_full_light_file_id":
		return optionalString(settings.LogoFullLightFileID), true, nil
	case "logo_full_dark_file_id":
		return optionalString(settings.LogoFullDarkFileID), true, nil
	case "logo_symbol_light_file_id":
		return optionalString(settings.LogoSymbolLightFileID), true, nil
	case "logo_symbol_dark_file_id":
		return optionalString(settings.LogoSymbolDarkFileID), true, nil
	case "logo_email_light_file_id":
		return optionalString(settings.LogoEmailLightFileID), true, nil
	case "logo_email_dark_file_id":
		return optionalString(settings.LogoEmailDarkFileID), true, nil
	case "favicon_file_id":
		return optionalString(settings.FaviconFileID), true, nil
	case "site_og_background_file_id":
		return optionalString(settings.SiteOgBackgroundFileID), true, nil
	case "privacy_og_background_file_id":
		return optionalString(settings.PrivacyOgBackgroundFileID), true, nil
	case "terms_og_background_file_id":
		return optionalString(settings.TermsOgBackgroundFileID), true, nil
	case "primary_color":
		return settings.PrimaryColor, true, nil
	case "default_comments_enabled":
		return settings.DefaultCommentsEnabled, true, nil
	case "homepage_page_id":
		return optionalString(settings.HomepagePageID), true, nil
	case "menu_header_id":
		return optionalString(settings.MenuHeaderID), true, nil
	case "menu_secondary_id":
		return optionalString(settings.MenuSecondaryID), true, nil
	case "menu_footer_id":
		return optionalString(settings.MenuFooterID), true, nil
	case "menu_avatar_dropdown_id":
		return optionalString(settings.MenuAvatarDropdownID), true, nil
	case "meta_description":
		return settings.MetaDescription, true, nil
	case "google_analytics_id":
		return optionalString(settings.GoogleAnalyticsID), true, nil
	case "og_image_config":
		value, err := jsonColumnToMap(settings.OGImageConfig, false)
		return value, true, err
	case "og_image_config.home", "og_image_config.content":
		root, err := parseOgImageConfigRoot(settings.OGImageConfig)
		if err != nil {
			return nil, true, err
		}
		section := strings.TrimPrefix(key, "og_image_config.")
		return root[section], true, nil
	default:
		return nil, false, nil
	}
}

func (s *SiteSettingService) applySettingValue(settings *model.SiteSettings, key string, value siteSettingValue) error {
	switch key {
	case "site_title", "company_name", "company_address", "tax_id", "meta_description":
		return applyRequiredStringSiteSetting(settings, key, value)
	case "legal_email", "contact_email":
		return applyEmailSiteSetting(settings, key, value)
	case "social_links":
		v, err := parseJSONObjectBytes(value, false)
		if err != nil {
			return err
		}
		settings.SocialLinks = v
	case "logo_full_light_file_id", "logo_full_dark_file_id",
		"logo_symbol_light_file_id", "logo_symbol_dark_file_id",
		"logo_email_light_file_id", "logo_email_dark_file_id",
		"favicon_file_id", "site_og_background_file_id", "privacy_og_background_file_id",
		"terms_og_background_file_id", "homepage_page_id", "menu_header_id",
		"menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id":
		return applyOptionalStringSiteSetting(settings, key, value)
	case "primary_color":
		v, err := parseString(value)
		if err != nil {
			return err
		}
		if !siteSettingHexColorPattern.MatchString(v) {
			return fmt.Errorf("primary_color must be a six-digit hex color")
		}
		settings.PrimaryColor = v
	case "default_comments_enabled":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		settings.DefaultCommentsEnabled = v
	case "google_analytics_id":
		v, err := parseOptionalString(value)
		if err != nil {
			return err
		}
		if v != nil && !siteSettingAnalyticsPattern.MatchString(*v) {
			return fmt.Errorf("google_analytics_id must be a valid Google Analytics identifier")
		}
		settings.GoogleAnalyticsID = v
	case "og_image_config":
		v, err := parseJSONObjectBytes(value, true)
		if err != nil {
			return err
		}
		settings.OGImageConfig = v
	case "og_image_config.home":
		return applyOgImageConfigSection(settings, "home", value)
	case "og_image_config.content":
		return applyOgImageConfigSection(settings, "content", value)
	default:
		return fmt.Errorf("invalid setting key: %s", key)
	}

	return nil
}

func applyRequiredStringSiteSetting(settings *model.SiteSettings, key string, value siteSettingValue) error {
	parsed, err := parseString(value)
	if err != nil {
		return err
	}
	switch key {
	case "site_title":
		settings.SiteTitle = parsed
	case "company_name":
		settings.CompanyName = parsed
	case "company_address":
		settings.CompanyAddress = parsed
	case "tax_id":
		settings.TaxID = parsed
	case "meta_description":
		settings.MetaDescription = parsed
	}
	return nil
}

func applyEmailSiteSetting(settings *model.SiteSettings, key string, value siteSettingValue) error {
	parsed, err := parseString(value)
	if err != nil {
		return err
	}
	if err := validateSiteSettingEmail(key, parsed); err != nil {
		return err
	}
	switch key {
	case "legal_email":
		settings.LegalEmail = parsed
	case "contact_email":
		settings.ContactEmail = parsed
	}
	return nil
}

func applyOptionalStringSiteSetting(settings *model.SiteSettings, key string, value siteSettingValue) error {
	parsed, err := parseOptionalString(value)
	if err != nil {
		return err
	}
	switch key {
	case "logo_full_light_file_id":
		settings.LogoFullLightFileID = parsed
	case "logo_full_dark_file_id":
		settings.LogoFullDarkFileID = parsed
	case "logo_symbol_light_file_id":
		settings.LogoSymbolLightFileID = parsed
	case "logo_symbol_dark_file_id":
		settings.LogoSymbolDarkFileID = parsed
	case "logo_email_light_file_id":
		settings.LogoEmailLightFileID = parsed
	case "logo_email_dark_file_id":
		settings.LogoEmailDarkFileID = parsed
	case "favicon_file_id":
		settings.FaviconFileID = parsed
	case "site_og_background_file_id":
		settings.SiteOgBackgroundFileID = parsed
	case "privacy_og_background_file_id":
		settings.PrivacyOgBackgroundFileID = parsed
	case "terms_og_background_file_id":
		settings.TermsOgBackgroundFileID = parsed
	case "homepage_page_id":
		settings.HomepagePageID = parsed
	case "menu_header_id":
		settings.MenuHeaderID = parsed
	case "menu_secondary_id":
		settings.MenuSecondaryID = parsed
	case "menu_footer_id":
		settings.MenuFooterID = parsed
	case "menu_avatar_dropdown_id":
		settings.MenuAvatarDropdownID = parsed
	}
	return nil
}
