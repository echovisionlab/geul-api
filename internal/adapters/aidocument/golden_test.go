package aidocumentadapter

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata")

// assertGoldenJSON compares value, encoded as indented JSON, with
// testdata/<name>. Run the test with -update to accept a deliberate change.
func assertGoldenJSON(t *testing.T, name string, value any) {
	t.Helper()
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	require.NoError(t, encoder.Encode(value))

	path := filepath.Join("testdata", filepath.FromSlash(name))
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, encoded.Bytes(), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden file is missing; run the test with -update")
	require.Equal(t, string(want), encoded.String(), "golden mismatch; run the test with -update if the change is deliberate")
}
