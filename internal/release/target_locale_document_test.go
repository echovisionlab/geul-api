package release

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

func TestValidateReleaseTargetBatchRejectsInteractiveDeletes(t *testing.T) {
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	batch := contentblock.Batch{
		DocumentID:       documentID,
		ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{
			Locale:  "en",
			Deletes: []uuid.UUID{blockID},
		}},
	}

	err := validateReleaseTargetBatch(batch, documentID, revision, "en", false)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("interactive locale delete error = %v", err)
	}
	if err := validateReleaseTargetBatch(batch, documentID, revision, "en", true); err != nil {
		t.Fatalf("authoritative replacement locale delete rejected: %v", err)
	}
}

func TestReleaseSnapshotContainsLocaleTreatsEmptyOverlayAsPresent(t *testing.T) {
	if !releaseSnapshotContainsLocale(contentblock.Snapshot{
		LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "ko"}},
	}, "ko") {
		t.Fatal("empty target overlay was treated as missing")
	}
	if releaseSnapshotContainsLocale(contentblock.Snapshot{
		LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "ko"}},
	}, "fr") {
		t.Fatal("different target locale was treated as present")
	}
}

func TestValidateReleaseTargetBatchAllowsExplicitEmptyUpsert(t *testing.T) {
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	batch := contentblock.Batch{
		DocumentID:       documentID,
		ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{
			Locale: "en",
			Upserts: []contentblock.LocaleBlockUpdate{{
				BlockID: blockID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"content":[]}`),
			}},
		}},
	}

	if err := validateReleaseTargetBatch(batch, documentID, revision, "en", false); err != nil {
		t.Fatalf("explicit empty locale upsert rejected: %v", err)
	}
}

func TestValidateReleaseTargetBatchRejectsSharedAndCrossLocaleMutation(t *testing.T) {
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	tests := []struct {
		name  string
		batch contentblock.Batch
	}{
		{
			name: "shared delete",
			batch: contentblock.Batch{
				DocumentID: documentID, ExpectedRevision: revision, Deletes: []uuid.UUID{blockID},
			},
		},
		{
			name: "other locale",
			batch: contentblock.Batch{
				DocumentID: documentID, ExpectedRevision: revision,
				LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ja"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateReleaseTargetBatch(test.batch, documentID, revision, "en", false); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestValidateReleaseCreditNotesPreservesTargetExplicitEmpty(t *testing.T) {
	creditID := uuid.NewString()
	values := []*intrav1.ReleaseCreditNote{{CreditId: creditID, Note: ""}}

	notes, err := validateReleaseCreditNotes(values, []string{creditID}, true)
	if err != nil || notes[creditID] != "" {
		t.Fatalf("target explicit-empty credit note = (%v, %v)", notes, err)
	}
	if _, err := validateReleaseCreditNotes(values, []string{creditID}, false); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("source empty credit note error = %v", err)
	}
}
