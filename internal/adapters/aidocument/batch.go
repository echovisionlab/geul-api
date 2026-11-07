package aidocumentadapter

import (
	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

// deferProjections pairs each request with its domain state, projected when the
// document is first read.
func deferProjections[S any](
	requests []core.LoadRequest,
	states []func() (S, error),
	project func(core.DocumentIdentity, core.Locale, S) (core.Document, error),
) []core.DeferredDocument {
	documents := make([]core.DeferredDocument, len(requests))
	for index, request := range requests {
		documents[index] = func() (core.Document, error) {
			state, err := states[index]()
			if err != nil {
				return core.Document{}, err
			}
			return project(request.Document, request.Locale, state)
		}
	}
	return documents
}
