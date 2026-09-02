package aidocumentadapter

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This adapter may depend only on the typed protobuf and application models;
// editor/storage-native document formats must not gain a transport shortcut.
func TestAdapterSourceExcludesEditorNativeFormats(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := [][]byte{
		[]byte("ht" + "ml"),
		[]byte("tip" + "tap"),
		[]byte("prose" + "mirror"),
		[]byte("y" + "js"),
		[]byte("xli" + "ff"),
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		source = bytes.ToLower(source)
		for _, token := range forbidden {
			if bytes.Contains(source, token) {
				t.Fatalf("%s contains forbidden editor-native format token %q", name, token)
			}
		}
	}
}
