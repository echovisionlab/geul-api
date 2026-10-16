package email

// RenderError carries non-recipient context to delivery failure logs while
// preserving the original error chain.
type RenderError struct {
	LayoutID string
	Locale   string
	Err      error
}

func (e *RenderError) Error() string { return e.Err.Error() }
func (e *RenderError) Unwrap() error { return e.Err }
