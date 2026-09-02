package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranslationCommandsDoNotReintroduceGenericAuthorizationHelpers(t *testing.T) {
	t.Parallel()

	legacyNames := []string{
		"requireTranslation" + "MutationAuthorityWithDB",
		"requireTranslation" + "EntityEdit",
		"WithTranslationJob" + "SpiceDB",
		"RequireTranslationJob" + "ApplyRoot",
		"RequireLegalJob" + "Root",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("find translation application files: %v", err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, legacyName := range legacyNames {
			if strings.Contains(string(contents), legacyName) {
				t.Fatalf("%s retains obsolete generic translation authorization %s", path, legacyName)
			}
		}
	}
}
