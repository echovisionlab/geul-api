package sitevariablemigrate

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Private operator exports stay outside the repository. This diagnostic reports
// structural failures only; neither identifiers nor authored values are logged.
func TestContactPrivateHistoryCorpus(t *testing.T) {
	path := os.Getenv("GEUL_CONTACT_HISTORY_CORPUS")
	if path == "" {
		t.Skip("operator-only private corpus diagnostic")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private history corpus")
	}
	var rows []struct {
		Table    string          `json:"table"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if json.Unmarshal(data, &rows) != nil || len(rows) == 0 {
		t.Fatal("invalid private history corpus envelope")
	}
	kinds := map[string]string{"page_version": "page", "post_version": "post", "work_version": "work"}
	converted := 0
	for index, row := range rows {
		kind, ok := kinds[row.Table]
		if !ok {
			t.Errorf("row=%d category=unsupported_table", index)
			continue
		}
		var envelope map[string]json.RawMessage
		var document map[string]json.RawMessage
		var fingerprint string
		if json.Unmarshal(row.Snapshot, &envelope) != nil || json.Unmarshal(envelope["document"], &document) != nil || json.Unmarshal(document["blockCatalogFingerprint"], &fingerprint) != nil || !corpusFingerprintPattern.MatchString(fingerprint) {
			t.Errorf("row=%d category=invalid_document_identity", index)
			continue
		}
		after, err := ContactSnapshotJSON(row.Snapshot, kind)
		if err != nil {
			t.Errorf("row=%d catalog=%s category=%s", index, fingerprint, corpusFailureCategory(err))
			// Probe the current shape separately without granting this catalog
			// migration permission. Only the production conversion above counts.
			document["blockCatalogFingerprint"], _ = json.Marshal(contactTargetCatalog)
			envelope["document"], _ = json.Marshal(document)
			probe, _ := json.Marshal(envelope)
			if _, probeErr := ContactSnapshotJSON(probe, kind); probeErr != nil {
				t.Logf("row=%d target_shape=%s", index, corpusFailureCategory(probeErr))
			}
			continue
		}
		repeated, err := ContactSnapshotJSON(after, kind)
		if err != nil || string(after) != string(repeated) {
			t.Errorf("row=%d catalog=%s category=non_idempotent_conversion", index, fingerprint)
			continue
		}
		converted++
	}
	t.Logf("history_rows=%d converted=%d target_catalog=%s", len(rows), converted, contactTargetCatalog)
}

var corpusFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var corpusFieldPathPattern = regexp.MustCompile(`^\$(?:\.[A-Za-z_][A-Za-z0-9_]*|\[[0-9]+\])*$`)
var corpusUnknownFieldPattern = regexp.MustCompile(`unknown field "([A-Za-z_][A-Za-z0-9_]*)"`)

func corpusFailureCategory(err error) string {
	var validation *contentv1.ContentBlockValidationError
	if errors.As(err, &validation) {
		path := "$"
		if corpusFieldPathPattern.MatchString(validation.Path) {
			path = validation.Path
		}
		return "validation/" + validation.Code + "/" + path
	}
	if field := corpusUnknownFieldPattern.FindStringSubmatch(err.Error()); field != nil {
		return "unknown_field/" + field[1]
	}
	if strings.Contains(err.Error(), "unreviewed stored catalog") {
		return "unreviewed_catalog"
	}
	return "invalid_snapshot_shape"
}
