package dberrors

import "errors"

// IsSerializationFailure reports whether err wraps a database-driver error
// whose SQLSTATE is PostgreSQL's serialization-failure code.
func IsSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	var stateError interface {
		error
		SQLState() string
	}
	return errors.As(err, &stateError) && stateError.SQLState() == "40001"
}
