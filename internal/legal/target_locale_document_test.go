package legal

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
)

func TestCanonicalLegalLocaleRequiresExactCanonicalValue(t *testing.T) {
	locale, err := canonicalLegalLocale("zh-TW")
	if err != nil || locale != "zh-TW" {
		t.Fatalf("canonical locale = (%q, %v)", locale, err)
	}
	for _, input := range []string{"KO", "ko-KR", "ko_KR", " ko ", "zh-Hant", "es-MX"} {
		if _, err := canonicalLegalLocale(input); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("canonicalLegalLocale(%q) error = %v", input, err)
		}
	}
}

func TestSeedTargetLocaleBatchPreservesStableSourceLeaves(t *testing.T) {
	documentID, revision, firstID, secondID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: documentID, Revision: revision},
		SourceLocale: "en",
		Blocks: []contentblock.BaseBlock{
			{ID: firstID, Kind: "paragraph"},
			{ID: secondID, Kind: "paragraph"},
		},
		LocaleOverlays: []contentblock.LocaleOverlay{{
			Locale: "en",
			Blocks: []contentblock.LocaleBlockUpdate{
				{BlockID: firstID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"content":[{"text":"source"}]}`)},
				{BlockID: secondID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"content":[]}`)},
			},
		}},
	}
	incoming := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: revision,
		LocaleGroups: []contentblock.LocaleMutationGroup{{
			Locale: "ko",
			Upserts: []contentblock.LocaleBlockUpdate{{
				BlockID: firstID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"content":[{"text":"target"}]}`),
			}},
		}},
	}

	seeded, err := contentblock.SeedTargetLocaleBatch(incoming, snapshot, snapshot.SourceLocale, "ko")
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded.LocaleGroups) != 1 || len(seeded.LocaleGroups[0].Upserts) != 2 {
		t.Fatalf("seeded locale groups = %#v", seeded.LocaleGroups)
	}
	byID := make(map[uuid.UUID]contentblock.LocaleBlockUpdate)
	for _, block := range seeded.LocaleGroups[0].Upserts {
		byID[block.BlockID] = block
	}
	if string(byID[firstID].LocalizedData) != `{"content":[{"text":"target"}]}` {
		t.Fatalf("incoming target did not override source seed: %s", byID[firstID].LocalizedData)
	}
	if string(byID[secondID].LocalizedData) != `{"content":[]}` {
		t.Fatalf("explicit empty source leaf was not seeded: %s", byID[secondID].LocalizedData)
	}
}
