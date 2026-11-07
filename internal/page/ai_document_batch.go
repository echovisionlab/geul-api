package page

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// aiDocumentStateReads is what an AI document state is built from: a transaction's
// store, or the rows of a batch.
type aiDocumentStateReads interface {
	FindPage(ctx context.Context, locator Locator, value string, lock RowLock) (Page, error)
	DocumentRoot(ctx context.Context, pageID string, lock RowLock) (DocumentRoot, error)
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	Locale(ctx context.Context, pageID, locale string, lock RowLock) (Locale, bool, error)
}

// LoadAIDocumentBatch reads many Page locales in one database snapshot and defers
// building each state, so a broken Page fails alone. authorized answers from what
// the caller resolved once for the whole batch, because checking each Page is the
// cost this read exists to avoid.
func (s *Service) LoadAIDocumentBatch(
	ctx context.Context,
	authorized func(pageID string) bool,
	requests []LoadAIDocumentQuery,
) ([]func() (AIDocumentState, error), error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.MemberID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	var pageIDs []string
	for _, request := range requests {
		if authorized(request.PageID) {
			pageIDs = append(pageIDs, request.PageID)
		}
	}
	var rows AIDocumentBatchRows
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.AIDocumentBatchRows(ctx, pageIDs)
		return err
	}); err != nil {
		return nil, err
	}
	states := make([]func() (AIDocumentState, error), len(requests))
	for index, request := range requests {
		states[index] = func() (AIDocumentState, error) {
			if err := validatePageAIDocumentIdentity(request.PageID, request.Locale); err != nil {
				return AIDocumentState{}, err
			}
			root := lockedAIDocumentRoot{principal: principal}
			var err error
			if root.page, root.documentID, err = loadPageAIDocumentRoot(ctx, rows, request.PageID, Unlocked); err != nil {
				return AIDocumentState{}, err
			}
			return loadAIDocumentStateAfterAuthorization(ctx, rows, root, request.Locale)
		}
	}
	return states, nil
}

// AIDocumentBatchRows holds the rows of a batch's Pages. A Page the caller may not
// read is absent, which reads as not found. All of them come from one snapshot, so
// they need no row locks.
type AIDocumentBatchRows struct {
	Pages     map[string]Page
	Locales   map[string]map[string]Locale
	Snapshots map[uuid.UUID]contentblock.DeferredSnapshot
}

func (rows AIDocumentBatchRows) FindPage(_ context.Context, _ Locator, pageID string, _ RowLock) (Page, error) {
	page, found := rows.Pages[pageID]
	if !found {
		return Page{}, ErrPageNotFound
	}
	return page, nil
}

func (rows AIDocumentBatchRows) DocumentRoot(_ context.Context, pageID string, _ RowLock) (DocumentRoot, error) {
	page, found := rows.Pages[pageID]
	if !found {
		return DocumentRoot{}, ErrPageNotFound
	}
	root := DocumentRoot{ID: page.ID, SourceLocale: page.SourceLocale}
	if page.ContentDocumentID != nil {
		documentID, err := uuid.Parse(*page.ContentDocumentID)
		if err != nil {
			return DocumentRoot{}, err
		}
		root.ContentDocumentID = &documentID
	}
	return root, nil
}

func (rows AIDocumentBatchRows) LockedContentSnapshot(_ context.Context, documentID uuid.UUID, _ string) (contentblock.Snapshot, error) {
	snapshot, found := rows.Snapshots[documentID]
	if !found {
		return contentblock.Snapshot{}, contentblock.ErrDocumentNotFound
	}
	return snapshot()
}

func (rows AIDocumentBatchRows) Locale(_ context.Context, pageID, locale string, _ RowLock) (Locale, bool, error) {
	row, found := rows.Locales[pageID][locale]
	return row, found, nil
}
