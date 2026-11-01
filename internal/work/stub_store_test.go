package work

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// stubStore answers the Store methods a test sets; any other method panics
// on the nil embedded Store.
type stubStore struct {
	Store
	preferredLocale    func(memberID string) (*string, error)
	defaultLocale      func() (string, error)
	nicknames          func(memberIDs []string) (map[string]string, error)
	lockSourceLocale   func(workID string) (string, error)
	lockedLocaleText   func(workID, locale string) (LocaleMetadata, bool, error)
	upsertSourceLocale func(workID, locale string, title, summary *string) error
	lockDocumentRoot   func(documentID uuid.UUID) (Root, error)
	sourceLocale       func(workID string) (string, error)
}

func (s stubStore) PreferredLocale(_ context.Context, memberID string) (*string, error) {
	return s.preferredLocale(memberID)
}

func (s stubStore) DefaultLocale(context.Context) (string, error) { return s.defaultLocale() }

func (s stubStore) ContributorNicknames(_ context.Context, memberIDs []string) (map[string]string, error) {
	return s.nicknames(memberIDs)
}

func (s stubStore) LockSourceLocale(_ context.Context, workID string) (string, error) {
	return s.lockSourceLocale(workID)
}

func (s stubStore) LockedLocaleText(_ context.Context, workID, locale string) (LocaleMetadata, bool, error) {
	return s.lockedLocaleText(workID, locale)
}

func (s stubStore) UpsertSourceLocale(_ context.Context, workID, locale string, title, summary *string, _ time.Time) error {
	return s.upsertSourceLocale(workID, locale, title, summary)
}

func (s stubStore) LockDocumentRoot(_ context.Context, documentID uuid.UUID) (Root, error) {
	return s.lockDocumentRoot(documentID)
}

func (s stubStore) SourceLocale(_ context.Context, workID string) (string, error) {
	return s.sourceLocale(workID)
}
