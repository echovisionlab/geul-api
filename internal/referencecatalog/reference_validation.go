package referencecatalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// Reference kinds bound name and slug by their column length: varchar(100) for
// genre, style and category, varchar(50) for format and tag.
const (
	referenceValueMaxLength      = 100
	shortReferenceValueMaxLength = 50
)

func normalizeReferenceName(value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errs.Required("name")
	}
	if utf8.RuneCountInString(value) > maxLength {
		return "", errs.InvalidArgument("name", fmt.Sprintf("must be at most %d characters", maxLength))
	}
	return value, nil
}

func normalizeReferenceSlug(value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errs.Required("slug")
	}
	if utf8.RuneCountInString(value) > maxLength {
		return "", errs.InvalidArgument("slug", fmt.Sprintf("must be at most %d characters", maxLength))
	}
	if err := validateSlugWithoutSlash(value); err != nil {
		return "", err
	}
	return value, nil
}

func classifyReferenceUniqueViolation(resource string, name string, slug string, err error) error {
	if !dberrors.IsUniqueViolation(err) {
		return errs.Internal(err)
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case resource + "_name_key":
			return errs.WithCause(errs.AlreadyExists(resource, "name", name), err)
		case resource + "_slug_key":
			return errs.WithCause(errs.SlugAlreadyExists(resource, slug), err)
		}
	}

	return errs.WithCause(errs.AlreadyExistsMsg(fmt.Sprintf("%s with the same name or slug already exists", resource)), err)
}
