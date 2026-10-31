// Package pathslug holds the rule every public slug shares: it is one URL
// path segment. Length, alphabet and uniqueness stay with each domain.
package pathslug

import (
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// Validate rejects a slug that would span more than one path segment.
func Validate(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}
