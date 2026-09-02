package mcp

import (
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
	if err := validateStableUnitHandleSet([]string{"block-a/content"}); err != nil {
		t.Fatalf("validateStableUnitHandleSet() error = %v", err)
	}
	for _, handles := range [][]string{{"blocks/2/content"}, {"block-a/content", "block-a/content"}} {
		if err := validateStableUnitHandleSet(handles); err == nil {
			t.Fatalf("validateStableUnitHandleSet(%v) succeeded", handles)
		}
	}
}
