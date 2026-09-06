package label

import (
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
)

// normalizeLabelDocumentLocale is the exact-locale boundary for Label
// collaboration and DCDP. Persistence and the generated room contract use
// one canonical supported locale spelling; aliases and whitespace are not
// silently rewritten into a different room identity.
func normalizeLabelDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}
