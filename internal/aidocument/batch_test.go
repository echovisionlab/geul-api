package aidocument

import (
	"context"
	"errors"
	"testing"
)

// batchPort is a fakePort that also reads together.
type batchPort struct {
	*fakePort
	authorized []DocumentReference
	builds     int
	err        error
}

func (port *batchPort) LoadBatch(_ context.Context, authorized func(DocumentReference) bool, requests []LoadRequest) ([]DeferredDocument, error) {
	if port.err != nil {
		return nil, port.err
	}
	documents := make([]DeferredDocument, len(requests))
	for index, request := range requests {
		if authorized(request.Document.Reference) {
			port.authorized = append(port.authorized, request.Document.Reference)
		}
		documents[index] = func() (Document, error) {
			port.builds++
			return port.document, nil
		}
	}
	return documents, nil
}

func batchRequest(locale Locale) ReadRequest {
	return ReadRequest{Document: DocumentIdentity{Domain: DomainPost, Reference: "doc-handle"}, Locale: locale, Mode: ReadOutline, Limit: DefaultOutlineReadLimit}
}

func batchOf(request ReadRequest) []LoadRequest {
	return []LoadRequest{{Document: request.Document, Locale: request.Locale}}
}

func everyDocument(DocumentReference) bool { return true }

func TestABatchBuildsADocumentOnceAndAnswersItsReads(t *testing.T) {
	port := &batchPort{fakePort: &fakePort{document: testDocument("ko", true)}}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := batchRequest("ko")

	batch := service.OpenBatch(t.Context(), everyDocument, batchOf(request))
	if port.builds != 0 {
		t.Fatalf("opening built %d documents", port.builds)
	}
	first, err := batch.Read(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := service.Read(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.Read(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if port.builds != 1 {
		t.Fatalf("builds = %d", port.builds)
	}
	if len(first.Nodes) == 0 || len(first.Nodes) != len(direct.Nodes) {
		t.Fatalf("batch nodes = %d, direct nodes = %d", len(first.Nodes), len(direct.Nodes))
	}
}

func TestABatchRefusesWhatItWasNotOpenedWith(t *testing.T) {
	service, err := NewService(&batchPort{fakePort: &fakePort{document: testDocument("ko", true)}})
	if err != nil {
		t.Fatal(err)
	}
	batch := service.OpenBatch(t.Context(), everyDocument, batchOf(batchRequest("ko")))

	if _, err := batch.Read(t.Context(), batchRequest("en")); err == nil {
		t.Fatal("a locale outside the batch was read")
	}
}

// The checks of a single read hold for a batched one.
func TestABatchRejectsADocumentOtherThanTheOneAsked(t *testing.T) {
	service, err := NewService(&batchPort{fakePort: &fakePort{document: testDocument("ko", true)}})
	if err != nil {
		t.Fatal(err)
	}
	request := batchRequest("en")

	if _, err := service.OpenBatch(t.Context(), everyDocument, batchOf(request)).Read(t.Context(), request); err == nil {
		t.Fatal("a document of another locale was answered")
	}
}

func TestABatchThatCouldNotBeReadFailsEachDocument(t *testing.T) {
	failure := errors.New("snapshot failed")
	service, err := NewService(&batchPort{fakePort: &fakePort{}, err: failure})
	if err != nil {
		t.Fatal(err)
	}
	request := batchRequest("ko")

	if _, err := service.OpenBatch(t.Context(), everyDocument, batchOf(request)).Read(t.Context(), request); !errors.Is(err, failure) {
		t.Fatalf("err = %v", err)
	}
}

// A domain that cannot read together keeps its own per-document authorization.
func TestAPortWithoutBatchesReadsEachDocument(t *testing.T) {
	port := &fakePort{document: testDocument("ko", true)}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := batchRequest("ko")

	batch := service.OpenBatch(t.Context(), func(DocumentReference) bool {
		t.Fatal("a port that authorizes each read has no use for the scope")
		return false
	}, batchOf(request))
	if _, err := batch.Read(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if port.loadCalls != 1 {
		t.Fatalf("loads = %d", port.loadCalls)
	}
}
