// Package documentsearch answers "which document says this" across every document
// type. The index or the owning lists name the documents; reading each one's
// outline as the caller decides what it may see and where the text is.
package documentsearch

import (
	"context"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

// ReadLimit is the most documents one Search reads; Result.NextOffset continues.
const ReadLimit = 100

// Document names a document a search may read. It says nothing about whether the
// caller may see it or exactly where the text is: reading it does.
type Document struct {
	DocumentType core.Domain
	DocumentID   core.DocumentReference
	SourceLocale core.Locale
	Title        string
	Slug         *string
}

// IndexQuery asks for one page of the documents that may hold Text, in the order
// of DocumentTypes. Without Locales it means each document's source locale.
type IndexQuery struct {
	Text          string
	DocumentTypes []core.Domain
	Locales       []core.Locale
	Limit         int
	Offset        int
}

// Index narrows a search to the documents that may hold the text, so its cost
// follows the matches rather than the size of the site. It answers for no one:
// each candidate is still read as the caller.
type Index interface {
	Candidates(ctx context.Context, query IndexQuery) ([]Document, error)
}

// DocumentPage is one page of a document type's list and the size of the list.
type DocumentPage struct {
	Documents []Document
	Total     int64
}

// Lister lists the documents of one type that the caller may see.
type Lister interface {
	ListSearchableDocuments(ctx context.Context, documentType core.Domain, limit, offset int) (DocumentPage, error)
}

// Reader reads a document as the caller, wrapping ErrAccessRefused when the caller
// may not read it.
type Reader interface {
	Read(context.Context, core.ReadRequest) (core.Projection, error)
}
