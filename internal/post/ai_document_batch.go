package post

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
	AIDocumentRoot(ctx context.Context, postID string, lock RowLock) (AIDocumentRoot, error)
	SourceLocale(ctx context.Context, postID string) (string, error)
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	LocaleText(ctx context.Context, postID, locale string, forUpdate bool) (AIDocumentLocaleMetadata, bool, error)
	OptionalLocaleMetadata(ctx context.Context, postID, locale string, forUpdate bool) (LocaleMetadata, bool, error)
	TaxonomyIDs(ctx context.Context, postID string) (categoryIDs, tagIDs []string, err error)
}

// AIDocumentBatchRequest names one Post locale of a batched read.
type AIDocumentBatchRequest struct {
	PostID string
	Locale string
}

// LoadAIDocumentStateBatch reads many Post locales in one database snapshot and
// defers building each state, so a broken Post fails alone. authorized answers from
// what the caller resolved once for the whole batch, because checking each Post is
// the cost this read exists to avoid.
func (s *Service) LoadAIDocumentStateBatch(
	ctx context.Context,
	authorized func(postID string) bool,
	requests []AIDocumentBatchRequest,
) ([]func() (AIDocumentState, error), error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || !principal.Onboarded || principal.Banned || principal.MemberID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	var postIDs []string
	for _, request := range requests {
		if authorized(request.PostID) {
			postIDs = append(postIDs, request.PostID)
		}
	}
	var rows AIDocumentBatchRows
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.AIDocumentBatchRows(ctx, postIDs)
		return err
	}); err != nil {
		return nil, err
	}
	states := make([]func() (AIDocumentState, error), len(requests))
	for index, request := range requests {
		states[index] = func() (AIDocumentState, error) {
			locale, err := validatePostAIDocumentIdentity(request.PostID, request.Locale)
			if err != nil {
				return AIDocumentState{}, err
			}
			root, err := loadPostAIDocumentRoot(ctx, rows, request.PostID, Unlocked)
			if err != nil {
				return AIDocumentState{}, err
			}
			return loadAIDocumentStateAfterAuthorization(ctx, rows, root, locale, principal.MemberID.String())
		}
	}
	return states, nil
}

// AIDocumentBatchRoot is a batched Post's root row.
type AIDocumentBatchRoot struct {
	AIDocumentRoot
	SourceLocale string
}

// AIDocumentBatchRows holds the rows of a batch's Posts. A Post the caller may not
// read is absent, which reads as not found. All of them come from one snapshot, so
// they need no row locks.
type AIDocumentBatchRows struct {
	Roots       map[string]AIDocumentBatchRoot
	Locales     map[string]map[string]LocaleMetadata
	CategoryIDs map[string][]string
	TagIDs      map[string][]string
	Snapshots   map[uuid.UUID]contentblock.DeferredSnapshot
}

func (rows AIDocumentBatchRows) AIDocumentRoot(_ context.Context, postID string, _ RowLock) (AIDocumentRoot, error) {
	root, found := rows.Roots[postID]
	if !found {
		return AIDocumentRoot{}, ErrPostNotFound
	}
	return root.AIDocumentRoot, nil
}

func (rows AIDocumentBatchRows) SourceLocale(_ context.Context, postID string) (string, error) {
	root, found := rows.Roots[postID]
	if !found {
		return "", ErrPostNotFound
	}
	return root.SourceLocale, nil
}

func (rows AIDocumentBatchRows) LockedContentSnapshot(_ context.Context, documentID uuid.UUID, _ string) (contentblock.Snapshot, error) {
	snapshot, found := rows.Snapshots[documentID]
	if !found {
		return contentblock.Snapshot{}, contentblock.ErrDocumentNotFound
	}
	return snapshot()
}

func (rows AIDocumentBatchRows) LocaleText(_ context.Context, postID, locale string, _ bool) (AIDocumentLocaleMetadata, bool, error) {
	row, found := rows.Locales[postID][locale]
	return AIDocumentLocaleMetadata{Title: row.Title, Summary: row.Summary}, found, nil
}

func (rows AIDocumentBatchRows) OptionalLocaleMetadata(_ context.Context, postID, locale string, _ bool) (LocaleMetadata, bool, error) {
	row, found := rows.Locales[postID][locale]
	return row, found, nil
}

func (rows AIDocumentBatchRows) TaxonomyIDs(_ context.Context, postID string) ([]string, []string, error) {
	return rows.CategoryIDs[postID], rows.TagIDs[postID], nil
}
