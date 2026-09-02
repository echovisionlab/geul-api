package contentblock

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCloneBatchDoesNotShareMutableState(t *testing.T) {
	parentID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	blockID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	batch := Batch{
		Upserts:  []BaseBlock{{ID: blockID, ParentID: &parentID, SharedData: []byte(`{"layout":"wide"}`)}},
		Reorders: []Reorder{{BlockID: blockID, ParentID: &parentID}},
		LocaleGroups: []LocaleMutationGroup{{
			Locale:  "ko",
			Upserts: []LocaleBlockUpdate{{BlockID: blockID, LocalizedData: []byte(`{"text":"before"}`)}},
		}},
		validatedBaseReferences: map[uuid.UUID][]FileReference{
			blockID: {{AllowedMIMETypes: []string{"image/jpeg"}, AllowedMIMEPrefixes: []string{"image/"}}},
		},
		validatedProfile: "compact",
	}

	cloned := CloneBatch(batch)
	*batch.Upserts[0].ParentID = uuid.Nil
	batch.Upserts[0].SharedData[0] = 'x'
	*batch.Reorders[0].ParentID = uuid.Nil
	batch.LocaleGroups[0].Upserts[0].LocalizedData[0] = 'x'
	batch.validatedBaseReferences[blockID][0].AllowedMIMETypes[0] = "text/plain"
	batch.validatedBaseReferences[blockID][0].AllowedMIMEPrefixes[0] = "text/"

	require.Equal(t, uuid.MustParse("10000000-0000-0000-0000-000000000001"), *cloned.Upserts[0].ParentID)
	require.JSONEq(t, `{"layout":"wide"}`, string(cloned.Upserts[0].SharedData))
	require.Equal(t, uuid.MustParse("10000000-0000-0000-0000-000000000001"), *cloned.Reorders[0].ParentID)
	require.JSONEq(t, `{"text":"before"}`, string(cloned.LocaleGroups[0].Upserts[0].LocalizedData))
	require.Equal(t, "compact", cloned.validatedProfile)
	require.Equal(t, []string{"image/jpeg"}, cloned.validatedBaseReferences[blockID][0].AllowedMIMETypes)
	require.Equal(t, []string{"image/"}, cloned.validatedBaseReferences[blockID][0].AllowedMIMEPrefixes)
}

func TestSeedTargetLocaleBatchUsesSourceThenAppliesRequestedChanges(t *testing.T) {
	firstID := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	secondID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	deletedID := uuid.MustParse("10000000-0000-0000-0000-000000000003")
	snapshot := Snapshot{
		Blocks: []BaseBlock{
			{ID: firstID, Kind: "paragraph"},
			{ID: secondID, Kind: "paragraph"},
			{ID: deletedID, Kind: "paragraph"},
		},
		LocaleOverlays: []LocaleOverlay{{
			Locale: "en",
			Blocks: []LocaleBlockUpdate{
				{BlockID: firstID, LocalizedData: []byte(`{"text":"source one"}`)},
				{BlockID: secondID, LocalizedData: []byte(`{"text":"source two"}`)},
				{BlockID: deletedID, LocalizedData: []byte(`{"text":"source deleted"}`)},
			},
		}},
	}
	incoming := Batch{LocaleGroups: []LocaleMutationGroup{{
		Locale: "ko",
		Upserts: []LocaleBlockUpdate{{
			BlockID: firstID, ExpectedKind: "paragraph", LocalizedData: []byte(`{"text":"target one"}`),
		}},
		Deletes: []uuid.UUID{deletedID},
	}}}

	seeded, err := SeedTargetLocaleBatch(incoming, snapshot, "en", "ko")
	require.NoError(t, err)
	require.Len(t, seeded.LocaleGroups, 1)
	require.Equal(t, "ko", seeded.LocaleGroups[0].Locale)
	require.Equal(t, []uuid.UUID{secondID, firstID}, []uuid.UUID{
		seeded.LocaleGroups[0].Upserts[0].BlockID,
		seeded.LocaleGroups[0].Upserts[1].BlockID,
	})
	require.JSONEq(t, `{"text":"source two"}`, string(seeded.LocaleGroups[0].Upserts[0].LocalizedData))
	require.JSONEq(t, `{"text":"target one"}`, string(seeded.LocaleGroups[0].Upserts[1].LocalizedData))
	require.Equal(t, "en", snapshot.LocaleOverlays[0].Locale)
	require.Len(t, incoming.LocaleGroups[0].Deletes, 1)
}

func TestSeedTargetLocaleBatchRejectsSourceOverlayForUnknownBlock(t *testing.T) {
	unknownID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	_, err := SeedTargetLocaleBatch(Batch{}, Snapshot{
		LocaleOverlays: []LocaleOverlay{{
			Locale: "en",
			Blocks: []LocaleBlockUpdate{{BlockID: unknownID}},
		}},
	}, "en", "ko")

	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
