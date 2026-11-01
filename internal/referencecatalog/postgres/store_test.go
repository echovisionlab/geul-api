package referencecatalogpostgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

func TestReferenceTakenNamesTheConstraintThatRefusedTheWrite(t *testing.T) {
	for constraint, want := range map[string]error{
		"genre_name_key":  referencecatalog.ErrNameTaken,
		"genre_slug_key":  referencecatalog.ErrSlugTaken,
		"genre_other_key": referencecatalog.ErrReferenceTaken,
		"format_name_key": referencecatalog.ErrReferenceTaken,
	} {
		violation := fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505", ConstraintName: constraint})
		err := referenceTaken("genre", violation)
		require.ErrorIs(t, err, want, constraint)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "the driver error stays in the chain")
		require.Equal(t, violation.Error(), err.Error())
	}

	other := errors.New("connection reset")
	require.Same(t, other, referenceTaken("genre", other))
}

func TestClientNameTakenRecognizesAnyUniqueViolation(t *testing.T) {
	err := clientNameTaken(&pgconn.PgError{Code: "23505", ConstraintName: "client_name_key"})
	require.ErrorIs(t, err, referencecatalog.ErrClientNameTaken)
	require.NotErrorIs(t, clientNameTaken(&pgconn.PgError{Code: "23503"}), referencecatalog.ErrClientNameTaken)
}
