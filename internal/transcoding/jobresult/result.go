// Package jobresult describes whether a failed command may be retried.
package jobresult

import "errors"

type retryableError struct{ cause error }
type terminalError struct{ cause error }

func (e *retryableError) Error() string { return e.cause.Error() }
func (e *retryableError) Unwrap() error { return e.cause }
func (e *terminalError) Error() string  { return e.cause.Error() }
func (e *terminalError) Unwrap() error  { return e.cause }

// Retry marks an operation that may be safely attempted again because no
// terminal result was confirmed.
func Retry(err error) error {
	if err == nil {
		return nil
	}
	return &retryableError{cause: err}
}

// IsRetry reports whether an operation should be attempted again.
func IsRetry(err error) bool {
	var target *retryableError
	return errors.As(err, &target)
}

// Terminal marks a failure whose durable result was confirmed.
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &terminalError{cause: err}
}

// IsTerminal reports whether a durable terminal result was confirmed.
func IsTerminal(err error) bool {
	var target *terminalError
	return errors.As(err, &target)
}
