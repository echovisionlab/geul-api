package sitesettings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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

// textSettings are the stored text settings, keyed by setting.
var textSettings = map[string]func(*Settings) *string{
	"site_title":       func(s *Settings) *string { return &s.SiteTitle },
	"company_name":     func(s *Settings) *string { return &s.CompanyName },
	"company_address":  func(s *Settings) *string { return &s.CompanyAddress },
	"tax_id":           func(s *Settings) *string { return &s.TaxID },
	"legal_email":      func(s *Settings) *string { return &s.LegalEmail },
	"contact_email":    func(s *Settings) *string { return &s.ContactEmail },
	"primary_color":    func(s *Settings) *string { return &s.PrimaryColor },
	"meta_description": func(s *Settings) *string { return &s.MetaDescription },
}

// optionalTextSettings are the nullable text settings: File, Page and Menu
// references and the analytics identifier. An empty value reads as unset.
var optionalTextSettings = map[string]func(*Settings) **string{
	"logo_full_light_file_id":       func(s *Settings) **string { return &s.LogoFullLightFileID },
	"logo_full_dark_file_id":        func(s *Settings) **string { return &s.LogoFullDarkFileID },
	"logo_symbol_light_file_id":     func(s *Settings) **string { return &s.LogoSymbolLightFileID },
	"logo_symbol_dark_file_id":      func(s *Settings) **string { return &s.LogoSymbolDarkFileID },
	"logo_email_light_file_id":      func(s *Settings) **string { return &s.LogoEmailLightFileID },
	"logo_email_dark_file_id":       func(s *Settings) **string { return &s.LogoEmailDarkFileID },
	"favicon_file_id":               func(s *Settings) **string { return &s.FaviconFileID },
	"site_og_background_file_id":    func(s *Settings) **string { return &s.SiteOgBackgroundFileID },
	"privacy_og_background_file_id": func(s *Settings) **string { return &s.PrivacyOgBackgroundFileID },
	"terms_og_background_file_id":   func(s *Settings) **string { return &s.TermsOgBackgroundFileID },
	"homepage_page_id":              func(s *Settings) **string { return &s.HomepagePageID },
	"menu_header_id":                func(s *Settings) **string { return &s.MenuHeaderID },
	"menu_secondary_id":             func(s *Settings) **string { return &s.MenuSecondaryID },
	"menu_footer_id":                func(s *Settings) **string { return &s.MenuFooterID },
	"menu_avatar_dropdown_id":       func(s *Settings) **string { return &s.MenuAvatarDropdownID },
	"google_analytics_id":           func(s *Settings) **string { return &s.GoogleAnalyticsID },
}

func isSiteSettingMenuReference(key string) bool {
	switch key {
	case "menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id":
		return true
	default:
		return false
	}
}

func siteSettingReferenceValue(settings *Settings, key string) *string {
	if key != "homepage_page_id" && !isSiteSettingMenuReference(key) {
		return nil
	}
	return *optionalTextSettings[key](settings)
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

func applyOgImageConfigSection(settings *Settings, section string, value siteSettingValue) error {
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
func settingValue(settings *Settings, key string) (siteSettingValue, bool) {
	value, known, _ := decodedSettingValue(settings, key)
	return value, known
}

// decodedSettingValue is settingValue with the error that replaced an
// undecodable stored value by its fallback.
func decodedSettingValue(settings *Settings, key string) (siteSettingValue, bool, error) {
	if field, ok := textSettings[key]; ok {
		return *field(settings), true, nil
	}
	if field, ok := optionalTextSettings[key]; ok {
		return optionalString(*field(settings)), true, nil
	}
	switch key {
	case "social_links":
		value, err := jsonColumnToMap(settings.SocialLinks, true)
		return value, true, err
	case "default_comments_enabled":
		return settings.DefaultCommentsEnabled, true, nil
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

func applySettingValue(settings *Settings, key string, value siteSettingValue) error {
	switch key {
	case "legal_email", "contact_email", "primary_color", "google_analytics_id":
		return applyValidatedTextSetting(settings, key, value)
	case "social_links":
		v, err := parseJSONObjectBytes(value, false)
		if err != nil {
			return err
		}
		settings.SocialLinks = v
	case "default_comments_enabled":
		v, err := parseBool(value)
		if err != nil {
			return err
		}
		settings.DefaultCommentsEnabled = v
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
		return applyTextSetting(settings, key, value)
	}
	return nil
}

// applyTextSetting stores a text or optional text setting that has no format
// of its own.
func applyTextSetting(settings *Settings, key string, value siteSettingValue) error {
	if field, ok := textSettings[key]; ok {
		parsed, err := parseString(value)
		if err != nil {
			return err
		}
		*field(settings) = parsed
		return nil
	}
	if field, ok := optionalTextSettings[key]; ok {
		parsed, err := parseOptionalString(value)
		if err != nil {
			return err
		}
		*field(settings) = parsed
		return nil
	}
	return fmt.Errorf("invalid setting key: %s", key)
}

// applyValidatedTextSetting stores a text setting whose value has a format.
func applyValidatedTextSetting(settings *Settings, key string, value siteSettingValue) error {
	if key == "google_analytics_id" {
		v, err := parseOptionalString(value)
		if err != nil {
			return err
		}
		if v != nil && !siteSettingAnalyticsPattern.MatchString(*v) {
			return fmt.Errorf("google_analytics_id must be a valid Google Analytics identifier")
		}
		settings.GoogleAnalyticsID = v
		return nil
	}
	parsed, err := parseString(value)
	if err != nil {
		return err
	}
	if key == "primary_color" && !siteSettingHexColorPattern.MatchString(parsed) {
		return fmt.Errorf("primary_color must be a six-digit hex color")
	}
	if key != "primary_color" {
		if err := validateSiteSettingEmail(key, parsed); err != nil {
			return err
		}
	}
	*textSettings[key](settings) = parsed
	return nil
}
