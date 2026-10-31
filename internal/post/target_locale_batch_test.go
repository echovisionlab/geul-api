package post

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	tb "github.com/echovisionlab/geul-api/internal/testutil/targetbatchtest"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

func TestValidatePostTargetBatchOutcomes(t *testing.T) {
	identity := tb.Invalid("batch: Post target document identity and revision must match the locked state")
	graph := tb.Rejected(tb.NonSourceStructureForbidden, "Post target locale cannot mutate the shared Block graph or File ownership")
	tb.Run(t, tb.Shared(postEntityName), []tb.Case{
		{Kind: tb.DocumentMismatch, Want: identity},
		{Kind: tb.RevisionMismatch, Want: identity},
		{Kind: tb.GraphUpsert, Want: graph},
		{Kind: tb.GraphDelete, Want: graph},
		{Kind: tb.GraphReorder, Want: graph},
		{Kind: tb.MultipleLocaleGroups, Want: tb.Rejected(tb.RoomLocaleMismatch, "Post target locale contains multiple locale groups")},
		{Kind: tb.LocaleMismatch, Want: tb.Rejected(tb.RoomLocaleMismatch, "Post target mutation must match the authorized locale")},
		{Kind: tb.LocaleDelete, Want: tb.Rejected(tb.NonSourceStructureForbidden, "Post target patch cannot unset locale Blocks; write explicit empty values instead")},
		{Kind: tb.LocaleDeleteAllowed, Want: tb.Accepted()},
		{Kind: tb.LocaleUpsert, Want: tb.Accepted()},
		{Kind: tb.Empty, Want: tb.Accepted()},
	})
}

// The storage form is checked before conversion, so it must answer each
// rejected shape exactly like the batch form.
func TestValidatePostTargetStorageMatchesBatchOutcomes(t *testing.T) {
	documentID, blockID := uuid.New(), uuid.New()
	revision := documentID
	cases := []struct {
		name    string
		storage contentv1.ContentStorageMutationBatch
		batch   contentblock.Batch
	}{
		{
			name:    "graph delete",
			storage: contentv1.ContentStorageMutationBatch{Deletes: []string{blockID.String()}},
			batch:   contentblock.Batch{Deletes: []uuid.UUID{blockID}},
		},
		{
			name: "multiple locale groups",
			storage: contentv1.ContentStorageMutationBatch{LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{
				{Locale: "ko"}, {Locale: "ko"},
			}},
			batch: contentblock.Batch{LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko"}, {Locale: "ko"}}},
		},
		{
			name:    "locale mismatch",
			storage: contentv1.ContentStorageMutationBatch{LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{{Locale: "ja"}}},
			batch:   contentblock.Batch{LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ja"}}},
		},
		{
			name: "locale delete",
			storage: contentv1.ContentStorageMutationBatch{LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{
				{Locale: "ko", Deletes: []string{blockID.String()}},
			}},
			batch: contentblock.Batch{LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{blockID}}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.storage.ExpectedRevision = revision.String()
			tc.batch.DocumentID, tc.batch.ExpectedRevision = documentID, revision
			fromStorage := tb.Describe(t, validatePostTargetStorage(&tc.storage, revision, "ko", false))
			fromBatch := tb.Describe(t, contentblock.ValidateTargetLocaleBatch(postEntityName, tc.batch, documentID, revision, "ko", false))
			if fromStorage != fromBatch {
				t.Fatalf("storage outcome = %+v, batch outcome = %+v", fromStorage, fromBatch)
			}
		})
	}
}
