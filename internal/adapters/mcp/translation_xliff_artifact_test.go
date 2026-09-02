package mcp

import (
	"strings"
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestXLIFFImportEncodingPreservesEmptyAffectedUnitArray(t *testing.T) {
	encoded, err := encodeXLIFFImport(&managev1.ImportEntityTranslationXLIFFResponse{TargetRevision: "revision-a"})
	if err != nil {
		t.Fatalf("encodeXLIFFImport() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"u":[]`) {
		t.Fatalf("encodeXLIFFImport() = %s", encoded)
	}
}
