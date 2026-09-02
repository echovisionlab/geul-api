package aidocument

import "context"

type interactiveCompletionContextKey struct{}

// WithInteractivePostCommitCompletion marks one MCP or in-editor AI Apply.
// Its direct domain content.updated signal is replaced after commit by either
// the interactive Collab relay or one exact fallback signal.
func WithInteractivePostCommitCompletion(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveCompletionContextKey{}, true)
}

// WithInteractiveFallbackSignal allows the post-commit fallback through the
// same publisher after the marked owning-domain Apply has returned.
func WithInteractiveFallbackSignal(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveCompletionContextKey{}, false)
}

// InteractivePostCommitCompletionOwnsSignal reports whether the current Apply
// must suppress its direct domain content.updated producer.
func InteractivePostCommitCompletionOwnsSignal(ctx context.Context) bool {
	owned, _ := ctx.Value(interactiveCompletionContextKey{}).(bool)
	return owned
}
