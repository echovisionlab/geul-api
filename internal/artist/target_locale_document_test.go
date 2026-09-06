package artist

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
)

func TestValidateArtistTargetBatchRejectsSharedStructureAndOtherLocales(t *testing.T) {
	documentID, revision, blockID := uuid.New(), uuid.New(), uuid.New()
	base := contentblock.Batch{DocumentID: documentID, ExpectedRevision: revision}
	tests := []struct {
		name  string
		batch contentblock.Batch
	}{
		{name: "shared delete", batch: contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision, Deletes: []uuid.UUID{blockID},
		}},
		{name: "other locale", batch: contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ja"}},
		}},
		{name: "target unset", batch: contentblock.Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{blockID}}},
		}},
	}
	if err := validateArtistTargetBatchAuthority(base, documentID, revision, "ko", false); err != nil {
		t.Fatalf("empty target batch rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateArtistTargetBatchAuthority(test.batch, documentID, revision, "ko", false); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("validateArtistTargetBatchAuthority() error = %v", err)
			}
		})
	}
	providerDelete := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko", Deletes: []uuid.UUID{blockID}}},
	}
	if err := validateArtistTargetBatchAuthority(
		providerDelete, documentID, revision, "ko", true,
	); err != nil {
		t.Fatalf("provider replacement delete rejected: %v", err)
	}
}

func TestNormalizeArtistDocumentLocaleRequiresExactCanonicalValue(t *testing.T) {
	if locale, err := normalizeArtistDocumentLocale("ko"); err != nil || locale != "ko" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, locale := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := normalizeArtistDocumentLocale(locale); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("normalizeArtistDocumentLocale(%q) error = %v", locale, err)
		}
	}
}
