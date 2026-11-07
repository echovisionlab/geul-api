package aidocument

import (
	"context"
	"errors"
	"sync"
)

// LoadRequest names one document locale of a batched read.
type LoadRequest struct {
	Document DocumentIdentity
	Locale   Locale
}

// DeferredDocument builds a document whose rows a batch has read. A document that
// cannot be built, or that the caller may not read, fails alone.
type DeferredDocument func() (Document, error)

// BatchPort is a domain port that reads many documents in one database snapshot, at
// a cost that does not grow by statements per document. authorized answers from what
// the caller resolved once for the whole batch. The result has one entry per request.
type BatchPort interface {
	LoadBatch(ctx context.Context, authorized func(DocumentReference) bool, requests []LoadRequest) ([]DeferredDocument, error)
}

// LoadBatch reads requests through port's BatchPort when it has one, and otherwise
// defers each to Load, which authorizes that document itself. It never fails as a
// whole: a batch that could not be read fails each of its documents.
func LoadBatch(ctx context.Context, port DomainPort, authorized func(DocumentReference) bool, requests []LoadRequest) []DeferredDocument {
	documents := make([]DeferredDocument, len(requests))
	batch, batched := port.(BatchPort)
	if !batched {
		for index, request := range requests {
			documents[index] = func() (Document, error) {
				return port.Load(ctx, request.Document, request.Locale)
			}
		}
		return documents
	}
	loaded, err := batch.LoadBatch(ctx, authorized, requests)
	if err != nil {
		for index := range requests {
			documents[index] = func() (Document, error) { return Document{}, err }
		}
		return documents
	}
	return loaded
}

// Reader reads a document as Service.Read does.
type Reader interface {
	Read(context.Context, ReadRequest) (Projection, error)
}

// batch answers reads from the documents one OpenBatch loaded.
type batch struct {
	documents map[LoadRequest]DeferredDocument
}

// OpenBatch reads requests together and answers reads of exactly those documents. It
// builds a document on its first read, once, so concurrent readers spread that work.
func (s *Service) OpenBatch(ctx context.Context, authorized func(DocumentReference) bool, requests []LoadRequest) Reader {
	loaded := LoadBatch(ctx, s.port, authorized, requests)
	opened := batch{documents: make(map[LoadRequest]DeferredDocument, len(requests))}
	for index, request := range requests {
		opened.documents[request] = sync.OnceValues(func() (Document, error) {
			document, err := loaded[index]()
			if err != nil {
				return Document{}, err
			}
			if err := validateLoadedDocument(document, request.Document, request.Locale); err != nil {
				return Document{}, err
			}
			return document, nil
		})
	}
	return opened
}

func (opened batch) Read(ctx context.Context, request ReadRequest) (Projection, error) {
	if err := validateReadRequest(request); err != nil {
		return Projection{}, &InputError{Message: err.Error()}
	}
	load, found := opened.documents[LoadRequest{Document: request.Document, Locale: request.Locale}]
	if !found {
		return Projection{}, errors.New("AI document batch does not hold this document and locale")
	}
	document, err := load()
	if err != nil {
		return Projection{}, err
	}
	return readDocument(ctx, document, request)
}
