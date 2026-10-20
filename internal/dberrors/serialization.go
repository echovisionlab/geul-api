package dberrors

import (
	"context"
	"errors"
)

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

// SerializationRetryAttempts bounds RetrySerializationFailure. A repeatable
// read transaction that loses a race to a committed writer succeeds on the
// next attempt unless writes keep landing on the same rows.
const SerializationRetryAttempts = 3

// RetrySerializationFailure runs an idempotent read transaction again when it
// fails with a serialization failure. The writer that caused the conflict has
// committed, so the next attempt reads its result. run must reset any state it
// assigns, and must not be used for transactions with side effects.
func RetrySerializationFailure(ctx context.Context, run func() error) error {
	for attempt := 1; ; attempt++ {
		err := run()
		if err == nil || attempt >= SerializationRetryAttempts || !IsSerializationFailure(err) || ctx.Err() != nil {
			return err
		}
	}
}
