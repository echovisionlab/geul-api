package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

type batchedRegistryPort struct {
	registryPort
	requests []core.LoadRequest
	err      error
}

func (port *batchedRegistryPort) LoadBatch(_ context.Context, _ func(core.DocumentReference) bool, requests []core.LoadRequest) ([]core.DeferredDocument, error) {
	port.requests = requests
	if port.err != nil {
		return nil, port.err
	}
	documents := make([]core.DeferredDocument, len(requests))
	for index, request := range requests {
		documents[index] = func() (core.Document, error) {
			return core.Document{Identity: request.Document, Locale: request.Locale}, nil
		}
	}
	return documents, nil
}

func TestRegistryKeepsEachRequestsPlaceAcrossDomains(t *testing.T) {
	posts := &batchedRegistryPort{}
	registrations := completeRegistryRegistrations(core.DomainPost, posts)
	pages := &registryPort{document: core.Document{Identity: core.DocumentIdentity{Domain: core.DomainPage, Reference: "page"}, Locale: "en"}}
	for index := range registrations {
		if registrations[index].Domain == core.DomainPage {
			registrations[index].Port = pages
		}
	}
	registry, err := NewRegistry(registrations...)
	if err != nil {
		t.Fatal(err)
	}
	requests := []core.LoadRequest{
		{Document: core.DocumentIdentity{Domain: core.DomainPost, Reference: "first"}, Locale: "en"},
		{Document: core.DocumentIdentity{Domain: core.DomainPage, Reference: "page"}, Locale: "en"},
		{Document: core.DocumentIdentity{Domain: core.DomainPost, Reference: "second"}, Locale: "ko"},
	}

	documents, err := registry.LoadBatch(t.Context(), func(core.DocumentReference) bool { return true }, requests)
	if err != nil {
		t.Fatal(err)
	}

	for index, request := range requests {
		document, err := documents[index]()
		if err != nil {
			t.Fatal(err)
		}
		if document.Identity != request.Document || document.Locale != request.Locale {
			t.Fatalf("documents[%d] = %+v, want %+v", index, document.Identity, request)
		}
	}
	if len(posts.requests) != 2 {
		t.Fatalf("the Post port read %d requests together", len(posts.requests))
	}
}

// One domain's failure leaves another's documents to be read.
func TestRegistryFailsABatchOnlyForItsDomain(t *testing.T) {
	failure := errors.New("post snapshot failed")
	registrations := completeRegistryRegistrations(core.DomainPost, &batchedRegistryPort{err: failure})
	registry, err := NewRegistry(registrations...)
	if err != nil {
		t.Fatal(err)
	}
	requests := []core.LoadRequest{
		{Document: core.DocumentIdentity{Domain: core.DomainPost, Reference: "post"}, Locale: "en"},
		{Document: core.DocumentIdentity{Domain: "unknown", Reference: "x"}, Locale: "en"},
	}

	documents, err := registry.LoadBatch(t.Context(), func(core.DocumentReference) bool { return true }, requests)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := documents[0](); !errors.Is(err, failure) {
		t.Fatalf("post err = %v", err)
	}
	if _, err := documents[1](); err == nil {
		t.Fatal("an unknown domain was read")
	}
}
