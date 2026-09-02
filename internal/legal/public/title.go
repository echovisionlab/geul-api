package public

import (
	"strings"

	"github.com/echovisionlab/geul-api/internal/publiccontent"
)

func resolveCanonicalLegalPublicTitle(
	fallbackTitle string,
	localization *publiccontent.Selection,
) string {
	if localization != nil && localization.Title != nil && strings.TrimSpace(*localization.Title) != "" {
		return strings.TrimSpace(*localization.Title)
	}
	return strings.TrimSpace(fallbackTitle)
}
