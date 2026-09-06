package release

import (
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func validateSlugWithoutSlash(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}
