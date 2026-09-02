package referencecatalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

const referenceValueMaxLength = 100

func normalizeReferenceName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errs.Required("name")
	}
	if utf8.RuneCountInString(value) > referenceValueMaxLength {
		return "", errs.InvalidArgument("name", fmt.Sprintf("must be at most %d characters", referenceValueMaxLength))
	}
	return value, nil
}

func normalizeReferenceSlug(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errs.Required("slug")
	}
	if utf8.RuneCountInString(value) > referenceValueMaxLength {
		return "", errs.InvalidArgument("slug", fmt.Sprintf("must be at most %d characters", referenceValueMaxLength))
	}
	if err := validateSlugWithoutSlash(value); err != nil {
		return "", err
	}
	return value, nil
}

func classifyReferenceUniqueViolation(resource string, name string, slug string, err error) error {
	if !isUniqueViolation(err) {
		return errs.Internal(err)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case resource + "_name_key":
			return errs.AlreadyExists(resource, "name", name)
		case resource + "_slug_key":
			return errs.SlugAlreadyExists(resource, slug)
		}
	}

	return errs.AlreadyExistsMsg(fmt.Sprintf("%s with the same name or slug already exists", resource))
}
