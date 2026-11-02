package targetlocale

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/translation"
)

var (
	documentID = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c35")
	revision   = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c36")
	firstBlock = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c37")
	lastBlock  = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c38")
)

func lockedState(targetExists bool) State {
	state := State{
		Entity: "Work",
		Snapshot: contentblock.Snapshot{
			Document:     contentblock.Document{ID: documentID, Revision: revision},
			SourceLocale: "en",
			Blocks: []contentblock.BaseBlock{
				{ID: lastBlock, Kind: "paragraph"},
				{ID: firstBlock, Kind: "paragraph"},
			},
			LocaleOverlays: []contentblock.LocaleOverlay{{
				Locale: "en",
				Blocks: []contentblock.LocaleBlockUpdate{
					{BlockID: lastBlock, LocalizedData: []byte(`{"text":"last"}`)},
					{BlockID: firstBlock, LocalizedData: []byte(`{"text":"first"}`)},
				},
			}},
		},
		SourceLocale: "en",
	}
	if targetExists {
		state.TargetExists, state.TargetRevision = true, "tr1_current"
	}
	return state
}

func emptyBatch() contentblock.Batch {
	return contentblock.Batch{DocumentID: documentID, ExpectedRevision: revision}
}

func TestAdmitChecksBatchBeforeRevisionAndCreation(t *testing.T) {
	stale := "tr1_stale"
	invalid := emptyBatch()
	invalid.Deletes = []uuid.UUID{firstBlock}
	_, err := Policy{}.Admit(lockedState(true), "ko", invalid, &stale)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid batch with stale revision error = %v, want the batch rejection", err)
	}

	_, err = Policy{}.Admit(lockedState(true), "ko", emptyBatch(), &stale)
	var conflict *translation.TargetRevisionConflict
	if !errors.As(err, &conflict) || conflict.CurrentDocumentRevision != revision.String() {
		t.Fatalf("stale target revision error = %v, want a conflict carrying the document revision", err)
	}

	_, err = Policy{}.Admit(lockedState(false), "ko", emptyBatch(), nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("absent target without AllowCreate error = %v", err)
	}
}

func TestAdmitRevisionModes(t *testing.T) {
	if _, err := (Policy{}).Admit(lockedState(true), "ko", emptyBatch(), nil); err == nil {
		t.Fatal("observed mode accepted a present target without its revision")
	}
	if _, err := (Policy{RevisionMode: RevisionCurrent}).Admit(lockedState(true), "ko", emptyBatch(), nil); err != nil {
		t.Fatalf("current mode rejected the current target: %v", err)
	}
	current := "tr1_current"
	if _, err := (Policy{}).Admit(lockedState(true), "ko", emptyBatch(), &current); err != nil {
		t.Fatalf("observed mode rejected the observed revision: %v", err)
	}
}

func TestAdmitSeedsOnlyACreatedLocale(t *testing.T) {
	seeded, err := Policy{AllowCreate: true, SeedSource: true}.Admit(lockedState(false), "ko", emptyBatch(), nil)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(seeded.LocaleGroups) != 1 || seeded.LocaleGroups[0].Locale != "ko" || len(seeded.LocaleGroups[0].Upserts) != 2 {
		t.Fatalf("seeded groups = %+v, want both source values copied to ko", seeded.LocaleGroups)
	}

	unseeded, err := Policy{AllowCreate: true}.Admit(lockedState(false), "ko", emptyBatch(), nil)
	if err != nil || len(unseeded.LocaleGroups) != 0 {
		t.Fatalf("unseeded create = (%+v, %v)", unseeded.LocaleGroups, err)
	}

	current := "tr1_current"
	existing, err := Policy{AllowCreate: true, SeedSource: true}.Admit(lockedState(true), "ko", emptyBatch(), &current)
	if err != nil || len(existing.LocaleGroups) != 0 {
		t.Fatalf("existing target was seeded: (%+v, %v)", existing.LocaleGroups, err)
	}
}

func TestAdmitCopiesTheBatch(t *testing.T) {
	batch := emptyBatch()
	batch.LocaleGroups = []contentblock.LocaleMutationGroup{{
		Locale:  "ko",
		Upserts: []contentblock.LocaleBlockUpdate{{BlockID: firstBlock, LocalizedData: []byte(`{}`)}},
	}}
	current := "tr1_current"
	admitted, err := Policy{}.Admit(lockedState(true), "ko", batch, &current)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	admitted.LocaleGroups[0].Upserts[0].LocalizedData[0] = 'x'
	if string(batch.LocaleGroups[0].Upserts[0].LocalizedData) != `{}` {
		t.Fatal("Admit() returned a batch sharing the caller's payload")
	}
}

func TestDeleteGroupsRemovesEveryStoredValueInStableOrder(t *testing.T) {
	state := lockedState(false)
	groups := DeleteGroups(state.Snapshot, "en")
	if len(groups) != 1 || groups[0].Locale != "en" {
		t.Fatalf("groups = %+v", groups)
	}
	if got := groups[0].Deletes; len(got) != 2 || got[0] != firstBlock || got[1] != lastBlock {
		t.Fatalf("deletes = %v, want [%s %s]", got, firstBlock, lastBlock)
	}
	if groups := DeleteGroups(state.Snapshot, "ko"); groups != nil {
		t.Fatalf("absent locale groups = %+v, want nil", groups)
	}
}

func TestSnapshotHasLocaleTreatsAnOverlayAsPresent(t *testing.T) {
	snapshot := contentblock.Snapshot{LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "ko"}}}
	if !SnapshotHasLocale(snapshot, "ko") {
		t.Fatal("an overlay without values was treated as missing")
	}
	if SnapshotHasLocale(snapshot, "fr") {
		t.Fatal("another locale was treated as present")
	}
}

func TestWriteTimeUsesDatabasePrecision(t *testing.T) {
	at := time.Date(2026, 11, 1, 1, 2, 3, 456789123, time.FixedZone("KST", 9*60*60))
	if got, want := WriteTime(at), time.Date(2026, 10, 31, 16, 2, 3, 456789000, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("WriteTime() = %v, want %v", got, want)
	}
	if WriteTime(time.Time{}).IsZero() {
		t.Fatal("WriteTime() kept a zero time")
	}
}
