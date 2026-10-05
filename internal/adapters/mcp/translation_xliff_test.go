package mcp

import (
	"fmt"
	"strings"
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestTranslationInterchangeModesHaveOneBidirectionalVocabulary(t *testing.T) {
	for name, mode := range translationInterchangeModes {
		parsed, err := translationInterchangeMode(name)
		if err != nil || parsed != mode {
			t.Fatalf("translationInterchangeMode(%q) = %v, %v", name, parsed, err)
		}
		compact, err := compactInterchangeMode(mode)
		if err != nil || compact != name {
			t.Fatalf("compactInterchangeMode(%v) = %q, %v", mode, compact, err)
		}
	}
	if _, err := compactInterchangeMode(managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_UNSPECIFIED); err == nil {
		t.Fatal("compactInterchangeMode(unspecified) succeeded")
	}
}

func TestStableUnitHandleSetValidationIsSharedByImportAndExport(t *testing.T) {
	if err := validateStableUnitHandleSet([]string{"block-a/content", "item:123:label", "item:menu.v1:label", "item:menu[stable]:label"}); err != nil {
		t.Fatalf("validateStableUnitHandleSet() error = %v", err)
	}
	for _, handles := range [][]string{{""}, {" block-a/content"}, {strings.Repeat("a", 257)}, {"block-a/content", "block-a/content"}} {
		if err := validateStableUnitHandleSet(handles); err == nil {
			t.Fatalf("validateStableUnitHandleSet(%v) succeeded", handles)
		}
	}
}

func TestXLIFFExportSelectionEnforcesAdvertisedBound(t *testing.T) {
	handles := make([]string, 1001)
	for index := range handles {
		handles[index] = fmt.Sprintf("item:item-%d:label", index)
	}
	patch := managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH
	if err := validateXLIFFSelection(patch, handles[:1000]); err != nil {
		t.Fatalf("maximum selection rejected: %v", err)
	}
	if err := validateXLIFFSelection(patch, handles); err == nil {
		t.Fatal("selection above the advertised maximum was accepted")
	}
	// Replace imports may affect more than the patch export selection maximum.
	if _, err := encodeXLIFFImport(&managev1.ImportEntityTranslationXLIFFResponse{
		TargetRevision: "revision-a", Changed: true, AffectedUnitHandles: handles,
	}); err != nil {
		t.Fatalf("successful complete import response was bounded as a selection: %v", err)
	}
}
