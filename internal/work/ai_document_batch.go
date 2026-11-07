package work

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
	LockWork(ctx context.Context, workID string, lock RowLock) (Work, error)
	SourceLocale(ctx context.Context, workID string) (string, error)
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	LocaleMetadata(ctx context.Context, workID, locale string, forUpdate bool) (LocaleMetadata, bool, error)
}

// AIDocumentBatchRequest names one Work locale of a batched read.
type AIDocumentBatchRequest struct {
	WorkID string
	Locale string
}

// LoadAIDocumentBatch reads many Work locales in one database snapshot and defers
// building each state, so a broken Work fails alone. authorized answers from what
// the caller resolved once for the whole batch, because checking each Work is the
// cost this read exists to avoid.
func (s *Service) LoadAIDocumentBatch(
	ctx context.Context,
	authorized func(workID string) bool,
	requests []AIDocumentBatchRequest,
) ([]func() (AIDocumentState, error), error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated || principal.MemberID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	var workIDs []string
	for _, request := range requests {
		if authorized(request.WorkID) {
			workIDs = append(workIDs, request.WorkID)
		}
	}
	var rows AIDocumentBatchRows
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		rows, err = store.AIDocumentBatchRows(ctx, workIDs)
		return err
	}); err != nil {
		return nil, err
	}
	states := make([]func() (AIDocumentState, error), len(requests))
	for index, request := range requests {
		states[index] = func() (AIDocumentState, error) {
			if err := validateWorkAIDocumentIdentity(request.WorkID, request.Locale); err != nil {
				return AIDocumentState{}, err
			}
			root, documentID, err := loadWorkAIDocumentRoot(ctx, rows, request.WorkID, Unlocked)
			if err != nil {
				return AIDocumentState{}, err
			}
			return loadAIDocumentStateAfterAuthorization(ctx, rows, root, documentID, request.Locale, principal.MemberID.String())
		}
	}
	return states, nil
}

// AIDocumentBatchRows holds the rows of a batch's Works. A Work the caller may not
// read is absent, which reads as not found. All of them come from one snapshot, so
// they need no row locks.
type AIDocumentBatchRows struct {
	Works     map[string]Work
	Locales   map[string]map[string]LocaleMetadata
	Snapshots map[uuid.UUID]contentblock.DeferredSnapshot
}

func (rows AIDocumentBatchRows) LockWork(_ context.Context, workID string, _ RowLock) (Work, error) {
	work, found := rows.Works[workID]
	if !found {
		return Work{}, ErrWorkNotFound
	}
	return work, nil
}

func (rows AIDocumentBatchRows) SourceLocale(_ context.Context, workID string) (string, error) {
	work, found := rows.Works[workID]
	if !found {
		return "", ErrWorkNotFound
	}
	return work.SourceLocale, nil
}

func (rows AIDocumentBatchRows) LockedContentSnapshot(_ context.Context, documentID uuid.UUID, _ string) (contentblock.Snapshot, error) {
	snapshot, found := rows.Snapshots[documentID]
	if !found {
		return contentblock.Snapshot{}, contentblock.ErrDocumentNotFound
	}
	return snapshot()
}

func (rows AIDocumentBatchRows) LocaleMetadata(_ context.Context, workID, locale string, _ bool) (LocaleMetadata, bool, error) {
	row, found := rows.Locales[workID][locale]
	return row, found, nil
}
