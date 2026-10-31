package referencecatalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/pathslug"
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
		return "", domainerr.Required("name")
	}
	if utf8.RuneCountInString(value) > maxLength {
		return "", domainerr.InvalidField("name", fmt.Sprintf("must be at most %d characters", maxLength))
	}
	return value, nil
}

func normalizeReferenceSlug(value string, maxLength int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", domainerr.Required("slug")
	}
	if utf8.RuneCountInString(value) > maxLength {
		return "", domainerr.InvalidField("slug", fmt.Sprintf("must be at most %d characters", maxLength))
	}
	if err := pathslug.Validate(value); err != nil {
		return "", err
	}
	return value, nil
}

func classifyReferenceUniqueViolation(resource string, name string, slug string, err error) error {
	if !dberrors.IsUniqueViolation(err) {
		return err
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case resource + "_name_key":
			return domainerr.Duplicate(resource, "name", name).WithCause(err)
		case resource + "_slug_key":
			return domainerr.Duplicate(resource, "slug", slug).WithCause(err)
		}
	}

	return domainerr.New(domainerr.AlreadyExists, "", resource+" with the same name or slug already exists").WithCause(err)
}
