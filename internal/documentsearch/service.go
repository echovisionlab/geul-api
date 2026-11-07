package documentsearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"golang.org/x/sync/errgroup"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// readWorkers bounds the outline reads running at once.
const readWorkers = 8

// ErrIndexUnavailable reports an index that could not answer. A search does not
// fall back to reading every document by itself, because its offset means another
// order there and a page in progress would lose documents.
var ErrIndexUnavailable = errors.New("document search index unavailable")

// ErrAccessRefused is what a Reader wraps when the caller may not read a document.
var ErrAccessRefused = errors.New("document read refused")

// Query is one search. Without Locales it reads each document's source locale.
// With Exhaustive it reads the owning lists instead of the index, and Offset counts
// through those lists.
type Query struct {
	Text          string
	DocumentTypes []core.Domain
	Locales       []core.Locale
	BlockLimit    int
	Offset        int
	Exhaustive    bool
}

// MatchedBlock is one block holding the text.
type MatchedBlock struct {
	Block   core.BlockID
	Preview string
}

// Match is a document holding the text. Locale is set when several were searched.
// Blocks is empty when only its title or slug matched.
type Match struct {
	Document   Document
	Locale     core.Locale
	Blocks     []MatchedBlock
	MoreBlocks int
}

// Result leaves out the documents the caller may not read. An index search pages
// only through the caller's Scope, so NextOffset shows nothing past it.
type Result struct {
	Matches    []Match
	Scanned    int
	Unreadable int
	NextOffset *int
}

// Service runs searches.
type Service struct {
	lists     Lister
	documents Documents
	index     Index
	scopes    Scopes
}

func NewService(lists Lister, documents Documents, index Index, scopes Scopes) (*Service, error) {
	switch {
	case lists == nil:
		return nil, errors.New("document search service needs the document lists")
	case documents == nil:
		return nil, errors.New("document search service needs a document reader")
	case index == nil:
		return nil, errors.New("document search service needs an index")
	case scopes == nil:
		return nil, errors.New("document search service needs the caller's scope")
	}
	return &Service{lists: lists, documents: documents, index: index, scopes: scopes}, nil
}

// Search reads up to ReadLimit documents from the query's offset.
func (service *Service) Search(ctx context.Context, query Query) (Result, error) {
	if query.Exhaustive {
		return service.searchLists(ctx, query)
	}
	return service.searchIndex(ctx, query)
}

func (service *Service) searchIndex(ctx context.Context, query Query) (Result, error) {
	scope, err := service.scopes.Resolve(ctx, query.DocumentTypes)
	if err != nil {
		return Result{}, err
	}
	if scope.empty() {
		return Result{Matches: []Match{}}, nil
	}
	candidates, err := service.index.Candidates(ctx, IndexQuery{
		Text: query.Text, DocumentTypes: query.DocumentTypes, Locales: query.Locales, Scope: scope, Limit: ReadLimit + 1, Offset: query.Offset,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrIndexUnavailable, err)
	}
	documents := candidates[:min(len(candidates), ReadLimit)]
	batch := service.documents.OpenBatch(ctx, scope, loadRequests(documents, query))
	result := matchDocuments(ctx, batch, documents, query)
	if len(candidates) > ReadLimit {
		next := query.Offset + len(documents)
		result.NextOffset = &next
	}
	return result, nil
}

// searchLists reads the types' lists as one sequence that the offset indexes. A
// list a caller may not read is left out and counted rather than failing the rest
// of the site's text.
func (service *Service) searchLists(ctx context.Context, query Query) (Result, error) {
	skip := query.Offset
	unreadableLists := 0
	var total int64
	var documents []Document
	for _, documentType := range query.DocumentTypes {
		remaining := ReadLimit - len(documents)
		// With nothing left to read, this listing still adds to the total.
		listed, err := service.lists.ListSearchableDocuments(ctx, documentType, max(remaining, 1), skip)
		if err != nil {
			if len(query.DocumentTypes) == 1 {
				return Result{}, err
			}
			unreadableLists++
			slog.WarnContext(ctx, "document search skipped a document type", failurecause.Attr(err), "domain", string(documentType))
			continue
		}
		total += listed.Total
		if int64(skip) >= listed.Total {
			skip -= int(listed.Total)
			continue
		}
		skip = 0
		if remaining > 0 {
			documents = append(documents, listed.Documents...)
		}
	}
	result := matchDocuments(ctx, service.documents, documents, query)
	result.Unreadable += unreadableLists
	if next := query.Offset + len(documents); int64(next) < total {
		result.NextOffset = &next
	}
	return result, nil
}

// loadRequests is every document locale a search of documents reads.
func loadRequests(documents []Document, query Query) []core.LoadRequest {
	requests := make([]core.LoadRequest, 0, len(documents))
	for _, document := range documents {
		for _, locale := range document.searchedLocales(query) {
			requests = append(requests, core.LoadRequest{
				Document: core.DocumentIdentity{Domain: document.DocumentType, Reference: document.DocumentID}, Locale: locale,
			})
		}
	}
	return requests
}

func (document Document) searchedLocales(query Query) []core.Locale {
	if len(query.Locales) == 0 {
		return []core.Locale{document.SourceLocale}
	}
	return query.Locales
}

// matchDocuments reads documents as the caller, a few at a time.
func matchDocuments(ctx context.Context, reader Reader, documents []Document, query Query) Result {
	matched := make([]documentMatches, len(documents))
	var group errgroup.Group
	group.SetLimit(readWorkers)
	for index, document := range documents {
		group.Go(func() error {
			matched[index] = matchDocument(ctx, reader, document, query)
			return nil
		})
	}
	_ = group.Wait()
	result := Result{Matches: []Match{}}
	for _, document := range matched {
		result.Matches = append(result.Matches, document.matches...)
		if document.readable {
			result.Scanned++
		}
		if document.failed {
			result.Unreadable++
		}
	}
	return result
}

// documentMatches is what reading one document found. A document that is neither
// readable nor failed is one the caller may not see.
type documentMatches struct {
	matches  []Match
	readable bool
	failed   bool
}

func matchDocument(ctx context.Context, reader Reader, document Document, query Query) documentMatches {
	var found documentMatches
	for _, locale := range document.searchedLocales(query) {
		match := Match{Document: document}
		if len(query.Locales) > 1 {
			match.Locale = locale
		}
		if err := readMatchingBlocks(ctx, reader, &match, locale, query); err != nil {
			if !errors.Is(err, ErrAccessRefused) {
				found.failed = true
				slog.WarnContext(ctx, "document search skipped a document", failurecause.Attr(err), "domain", string(document.DocumentType), "document_id", string(document.DocumentID), "locale", string(locale))
			}
			continue
		}
		found.readable = true
		if len(match.Blocks) != 0 {
			found.matches = append(found.matches, match)
		}
	}
	// The name is the document's own text only once a read has shown the caller may
	// see it: an exhaustive search lists first, and a Scope may have gone stale.
	if found.readable && !found.failed && len(found.matches) == 0 && document.titleOrSlugContains(query.Text) {
		found.matches = append(found.matches, Match{Document: document})
	}
	return found
}

// readMatchingBlocks follows next cursors, so MoreBlocks counts every block past
// the limit.
func readMatchingBlocks(ctx context.Context, reader Reader, match *Match, locale core.Locale, query Query) error {
	request := core.ReadRequest{
		Document: core.DocumentIdentity{Domain: match.Document.DocumentType, Reference: match.Document.DocumentID},
		Locale:   locale, Mode: core.ReadOutline, Query: query.Text, Limit: core.DefaultOutlineReadLimit,
	}
	for {
		projection, err := reader.Read(ctx, request)
		if err != nil {
			return err
		}
		for _, node := range projection.Nodes {
			if len(match.Blocks) == query.BlockLimit {
				match.MoreBlocks++
				continue
			}
			match.Blocks = append(match.Blocks, MatchedBlock{Block: node.ID, Preview: projection.Previews[node.ID]})
		}
		if projection.Next == nil {
			return nil
		}
		request.Cursor = *projection.Next
	}
}

func (document Document) titleOrSlugContains(text string) bool {
	needle := strings.ToLower(text)
	return strings.Contains(strings.ToLower(document.Title), needle) ||
		(document.Slug != nil && strings.Contains(strings.ToLower(*document.Slug), needle))
}
