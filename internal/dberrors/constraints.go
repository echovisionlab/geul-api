// Package dberrors classifies database-driver constraint failures without
// coupling domain packages to a particular driver.
package dberrors

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// IsForeignKeyViolation reports whether err represents a foreign-key failure
// from one of the database drivers supported by the API.
func IsForeignKeyViolation(err error) bool {
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return true
	}

	const (
		sqliteConstraintForeignKey = 787
		sqliteConstraintTrigger    = 1811
	)
	var sqliteErr interface {
		error
		Code() int
	}
	if !errors.As(err, &sqliteErr) {
		return false
	}
	if sqliteErr.Code() == sqliteConstraintForeignKey {
		return true
	}
	return sqliteErr.Code() == sqliteConstraintTrigger &&
		strings.Contains(strings.ToLower(sqliteErr.Error()), "foreign key constraint failed")
}

// IsUniqueViolation reports whether err represents a unique-constraint
// violation from PostgreSQL or a supported driver wrapper.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "SQLSTATE 23505")
}
