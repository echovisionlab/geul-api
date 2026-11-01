package programevent

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/google/uuid"
)

// SeedTargetLocale creates a target locale seeded from the source through
// store, as the integration tests set up translated events.
func SeedTargetLocale(ctx context.Context, store Store, eventID, locale string, expectedRevision, contributor uuid.UUID) (bool, string, error) {
	documentID, err := loadProgramEventContentDocumentID(ctx, store, eventID, NoLock)
	if err != nil {
		return false, "", err
	}
	output, err := applyProgramEventTargetLocaleMutation(ctx, store, programEventTargetLocaleMutationInput{
		EventID: eventID, DocumentID: documentID, Locale: locale,
		Batch: contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: expectedRevision,
			ContributorMemberIDs: []uuid.UUID{contributor},
		},
		ExpectedDocumentRevision: expectedRevision,
		Policy:                   targetlocale.Policy{AllowCreate: true, SeedSource: true},
		Now:                      time.Now().UTC(),
		Fence:                    programEventContentDocumentFence(eventID, func(context.Context, Store) error { return nil }),
	})
	return output.LocaleCreated, output.TargetRevision, err
}
