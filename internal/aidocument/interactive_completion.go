package aidocument

import "context"

type interactiveCompletionContextKey struct{}

// WithInteractivePostCommitCompletion is a legacy context marker.
// Deprecated: content.updated now commits in the owning transaction regardless
// of this marker; the optional post-commit relay never replaces durability.
func WithInteractivePostCommitCompletion(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveCompletionContextKey{}, true)
}

// WithInteractiveFallbackSignal clears the legacy marker.
// Deprecated: post-commit content.updated publishing is no longer supported.
func WithInteractiveFallbackSignal(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveCompletionContextKey{}, false)
}

// InteractivePostCommitCompletionOwnsSignal reads the legacy marker.
// Deprecated: publishers must not use it to suppress transactional events.
func InteractivePostCommitCompletionOwnsSignal(ctx context.Context) bool {
	owned, _ := ctx.Value(interactiveCompletionContextKey{}).(bool)
	return owned
}
