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

// Result leaves out the documents the caller may not read. NextOffset alone counts
// them, because the index pages through candidates before they are read: it shows
// that more candidates exist past a page, so stepping the offset through a query
// with over ReadLimit candidates reveals how many, though never which.
type Result struct {
	Matches    []Match
	Scanned    int
	Unreadable int
	NextOffset *int
}

// Service runs searches.
type Service struct {
	lists     Lister
	documents Reader
	index     Index
}

func NewService(lists Lister, documents Reader, index Index) (*Service, error) {
	switch {
	case lists == nil:
		return nil, errors.New("document search service needs the document lists")
	case documents == nil:
		return nil, errors.New("document search service needs a document reader")
	case index == nil:
		return nil, errors.New("document search service needs an index")
	}
	return &Service{lists: lists, documents: documents, index: index}, nil
}

// Search reads up to ReadLimit documents from the query's offset.
func (service *Service) Search(ctx context.Context, query Query) (Result, error) {
	if query.Exhaustive {
		return service.searchLists(ctx, query)
	}
	return service.searchIndex(ctx, query)
}

func (service *Service) searchIndex(ctx context.Context, query Query) (Result, error) {
	candidates, err := service.index.Candidates(ctx, IndexQuery{
		Text: query.Text, DocumentTypes: query.DocumentTypes, Locales: query.Locales, Limit: ReadLimit + 1, Offset: query.Offset,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrIndexUnavailable, err)
	}
	documents := candidates[:min(len(candidates), ReadLimit)]
	result := service.matchDocuments(ctx, documents, query)
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
	result := service.matchDocuments(ctx, documents, query)
	result.Unreadable += unreadableLists
	if next := query.Offset + len(documents); int64(next) < total {
		result.NextOffset = &next
	}
	return result, nil
}

// matchDocuments reads documents as the caller, a few at a time.
func (service *Service) matchDocuments(ctx context.Context, documents []Document, query Query) Result {
	matched := make([]documentMatches, len(documents))
	var group errgroup.Group
	group.SetLimit(readWorkers)
	for index, document := range documents {
		group.Go(func() error {
			matched[index] = service.matchDocument(ctx, document, query)
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

func (service *Service) matchDocument(ctx context.Context, document Document, query Query) documentMatches {
	locales := query.Locales
	if len(locales) == 0 {
		locales = []core.Locale{document.SourceLocale}
	}
	var found documentMatches
	for _, locale := range locales {
		match := Match{Document: document}
		if len(query.Locales) > 1 {
			match.Locale = locale
		}
		if err := service.readMatchingBlocks(ctx, &match, locale, query); err != nil {
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
	// see it: the index returns candidates the caller may not.
	if found.readable && !found.failed && len(found.matches) == 0 && document.titleOrSlugContains(query.Text) {
		found.matches = append(found.matches, Match{Document: document})
	}
	return found
}

// readMatchingBlocks follows next cursors, so MoreBlocks counts every block past
// the limit.
func (service *Service) readMatchingBlocks(ctx context.Context, match *Match, locale core.Locale, query Query) error {
	request := core.ReadRequest{
		Document: core.DocumentIdentity{Domain: match.Document.DocumentType, Reference: match.Document.DocumentID},
		Locale:   locale, Mode: core.ReadOutline, Query: query.Text, Limit: core.DefaultOutlineReadLimit,
	}
	for {
		projection, err := service.documents.Read(ctx, request)
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
