package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIntegrationPathsFromPackageDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/echovisionlab/geul-api\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "feature")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	for relative, want := range map[string]string{
		"../..":       root,
		"../../media": filepath.Join(root, "media"),
		"../../../../infra/spicedb/schema.generated.zed": filepath.Join(filepath.Dir(root), "geul-identity", "config", "spicedb", "schema.generated.zed"),
	} {
		if got := appIntegrationRepoPath(relative); got != want {
			t.Errorf("path(%q) = %s, want %s", relative, got, want)
		}
	}
	if got := AppIntegrationSpiceDBSchemaPath(root); got != appIntegrationRepoPath("../../../../infra/spicedb/schema.generated.zed") {
		t.Errorf("schema paths disagree: %s", got)
	}
}

func TestIntegrationModuleRootRejectsOtherModule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/unrelated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := appIntegrationModuleRoot(root); err == nil {
		t.Fatal("accepted unrelated module")
	}
}
