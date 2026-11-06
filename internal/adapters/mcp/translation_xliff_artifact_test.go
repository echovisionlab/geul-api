package mcp

import (
	"strings"
	"testing"

	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

func TestXLIFFImportEncodingPreservesEmptyAffectedUnitArray(t *testing.T) {
	encoded, err := encodeXLIFFImport(translationapp.XLIFFImport{TargetRevision: "revision-a"})
	if err != nil {
		t.Fatalf("encodeXLIFFImport() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"unit_ids":[]`) {
		t.Fatalf("encodeXLIFFImport() = %s", encoded)
	}
}
