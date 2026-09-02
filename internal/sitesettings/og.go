package sitesettings

import (
	"reflect"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// OGInvalidation is the minimal regeneration scope produced by a settings
// mutation. Multiple flags may be set for one atomic batch.
type OGInvalidation struct {
	All     bool
	Site    bool
	Content bool
	Privacy bool
	Terms   bool
}

// ClassifyOGInvalidation maps actual Site Settings changes to their affected
// OG projections.
func ClassifyOGInvalidation(before, after *model.SiteSettings, keys []string) OGInvalidation {
	var result OGInvalidation
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		beforeValue, beforeKnown := settingValue(before, key)
		afterValue, afterKnown := settingValue(after, key)
		if !beforeKnown || !afterKnown || reflect.DeepEqual(beforeValue, afterValue) {
			continue
		}
		switch key {
		case "site_title", "primary_color", "logo_light_file_id":
			result.All = true
		case "site_og_background_file_id":
			result.Site = true
		case "privacy_og_background_file_id":
			result.Privacy = true
		case "terms_og_background_file_id":
			result.Terms = true
		case "og_image_config":
			beforeConfig, _ := beforeValue.(structured.Fields)
			afterConfig, _ := afterValue.(structured.Fields)
			if !reflect.DeepEqual(beforeConfig["home"], afterConfig["home"]) {
				result.Site = true
			}
			if !reflect.DeepEqual(beforeConfig["content"], afterConfig["content"]) {
				result.Content = true
			}
		case "og_image_config.home":
			result.Site = true
		case "og_image_config.content":
			result.Content = true
		}
	}
	return result
}
