package contentblock

import (
	"encoding/json"
	"sort"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
)

const (
	presencePageVideoID     = "20000000-0000-4000-8000-000000000181"
	presencePageImmersiveID = "20000000-0000-4000-8000-000000000182"
	presencePageUnitID      = "20000000-0000-4000-8000-000000000183"
)

func TestPresentPageLocaleValuesIncludesSectionAndImmersiveExplicitEmptyLeaves(t *testing.T) {
	snapshot := Snapshot{
		Document: Document{Profile: "page"},
		Blocks: []BaseBlock{
			{ID: uuid.MustParse(presencePageVideoID), Kind: "external-video", SharedData: json.RawMessage(`{"externalVideo":{"props":{"uri":"https://example.test/video"}}}`)},
			{ID: uuid.MustParse(presencePageImmersiveID), Kind: "immersive-scene", SharedData: json.RawMessage(`{"immersiveScene":{"props":{},"units":[{"id":"` + presencePageUnitID + `","props":{}}]},"settings":{}}`)},
		},
		LocaleOverlays: []LocaleOverlay{{
			Locale: "en",
			Blocks: []LocaleBlockUpdate{
				{BlockID: uuid.MustParse(presencePageVideoID), LocalizedData: json.RawMessage(`{"externalVideo":{"props":{"caption":""}}}`)},
				{BlockID: uuid.MustParse(presencePageImmersiveID), LocalizedData: json.RawMessage(`{"immersiveScene":{"props":{},"units":[{"unitId":"` + presencePageUnitID + `","props":{"title":"","text":""}}]}}`)},
			},
		}},
	}

	targets, err := PresentPageLocaleValues(snapshot, "en")
	if err != nil {
		t.Fatal(err)
	}
	want := []*managev1.AIDocumentFieldTarget{
		pageSectionLocaleValueTarget(presencePageVideoID, "caption"),
		pageImmersiveLocaleValueTarget(presencePageImmersiveID, presencePageUnitID, "text"),
		pageImmersiveLocaleValueTarget(presencePageImmersiveID, presencePageUnitID, "title"),
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
	for index := range want {
		if localeValueTargetKey(targets[index]) != localeValueTargetKey(want[index]) {
			t.Fatalf("target %d = %s, want %s", index, localeValueTargetKey(targets[index]), localeValueTargetKey(want[index]))
		}
	}

	missing, err := PresentPageLocaleValues(snapshot, "ko")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing locale projected source fallback values: %#v", missing)
	}
}

func TestRestorePageAffectedLocaleValuesPreservesPageSectionAndImmersiveDefaults(t *testing.T) {
	storage := contentv1.ContentStorageMutationBatch{
		LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{{
			Locale: "en",
			Upserts: []contentv1.ContentStorageLocaleUpsert{
				{BlockID: presencePageVideoID, ExpectedKind: "external-video", LocalizedData: []byte(`{"externalVideo":{"props":{}}}`)},
				{BlockID: presencePageImmersiveID, ExpectedKind: "immersive-scene", LocalizedData: []byte(`{"immersiveScene":{"props":{},"units":[{"unitId":"` + presencePageUnitID + `","props":{}}]}}`)},
			},
		}},
	}
	values := []*managev1.AIDocumentFieldTarget{
		pageSectionLocaleValueTarget(presencePageVideoID, "caption"),
		pageImmersiveLocaleValueTarget(presencePageImmersiveID, presencePageUnitID, "title"),
		pageImmersiveLocaleValueTarget(presencePageImmersiveID, presencePageUnitID, "text"),
	}
	sort.Slice(values, func(left, right int) bool {
		return localeValueTargetKey(values[left]) < localeValueTargetKey(values[right])
	})

	if err := RestorePageAffectedLocaleValues("en", &storage, values); err != nil {
		t.Fatal(err)
	}
	video := decodedLocalePayloadForTest(t, storage.LocaleGroups[0].Upserts[0].LocalizedData, "externalVideo")
	videoProps := video["props"].(map[string]any)
	if caption, present := videoProps["caption"]; !present || caption != "" {
		t.Fatalf("explicit empty caption was not restored: %#v", videoProps)
	}
	immersive := decodedLocalePayloadForTest(t, storage.LocaleGroups[0].Upserts[1].LocalizedData, "immersiveScene")
	unit := immersive["units"].([]any)[0].(map[string]any)
	unitProps := unit["props"].(map[string]any)
	if title, present := unitProps["title"]; !present || title != "" {
		t.Fatalf("explicit empty immersive title was not restored: %#v", unitProps)
	}
	if text, present := unitProps["text"]; !present || text != "" {
		t.Fatalf("explicit empty immersive text was not restored: %#v", unitProps)
	}
}

func TestRestorePageAffectedLocaleValuesRejectsNonLeafPageTarget(t *testing.T) {
	storage := contentv1.ContentStorageMutationBatch{
		LocaleGroups: []contentv1.ContentStorageLocaleMutationGroup{{
			Locale: "en",
			Upserts: []contentv1.ContentStorageLocaleUpsert{{
				BlockID: presencePageVideoID, ExpectedKind: "external-video", LocalizedData: []byte(`{"externalVideo":{"props":{}}}`),
			}},
		}},
	}
	err := RestorePageAffectedLocaleValues("en", &storage, []*managev1.AIDocumentFieldTarget{
		localeValueTarget(presencePageVideoID, pageLocaleDataField, fieldPath("props")),
	})
	if err == nil {
		t.Fatal("non-leaf Page target was accepted")
	}
}

func decodedLocalePayloadForTest(t *testing.T, data []byte, kind string) map[string]any {
	t.Helper()
	root, err := decodeJSONAnyObject(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := root[kind].(map[string]any)
	if !ok {
		t.Fatalf("payload %s is missing: %#v", kind, root)
	}
	return payload
}
