package referencecatalog

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func validateSlugWithoutSlash(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "SQLSTATE 23505")
}
