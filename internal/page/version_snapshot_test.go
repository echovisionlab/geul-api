package page

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPageVersionProtoFormatting(t *testing.T) {
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	title := "Snapshot title"
	summary := "Snapshot summary"
	contentSnapshot := requireVersionContentSnapshot(t, "en", &title, &summary)
	digest := sha256.Sum256(contentSnapshot)

	view, err := versionView(Version{
		ID: "page-version-1", Version: 3, Title: &title, Summary: &summary,
		ContentSnapshot:      contentSnapshot,
		ContributorMemberIDs: []string{"member-2", "member-1", "member-2"},
		CreatedAt:            createdAt,
	}, map[string]string{"member-1": "Mina", "member-2": "Jules"})
	require.NoError(t, err)
	list, err := VersionListMessage(VersionList{Versions: []VersionView{view}})
	require.NoError(t, err)
	version := list.Versions[0]
	require.Equal(t, "page-version-1", version.Id)
	require.Equal(t, int32(3), version.Version)
	require.Equal(t, "en", version.SourceLocale)
	require.Equal(t, &title, version.Title)
	require.Equal(t, &summary, version.Summary)
	require.Equal(t, fmt.Sprintf("%x", digest[:]), version.CanonicalHash)
	require.Equal(t, []string{"member-1", "member-2"}, []string{
		version.Contributors[0].MemberId,
		version.Contributors[1].MemberId,
	})
}

func TestVersionContentSnapshotUsesTypedDocumentEnvelope(t *testing.T) {
	t.Parallel()

	encoded := requireVersionContentSnapshot(t, "en", nil, nil)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	assert.ElementsMatch(t, []string{
		"schemaVersion",
		"sourceLocale",
		"title",
		"summary",
		"document",
	}, mapKeys(fields))
	assert.JSONEq(t, "null", string(fields["title"]))
	assert.JSONEq(t, "null", string(fields["summary"]))

	decoded, err := DecodeVersionContentSnapshot(encoded)
	require.NoError(t, err)
	assert.Nil(t, decoded.Title)
	assert.Nil(t, decoded.Summary)
	assert.Equal(t, "en", decoded.Document.GetLocale())
	assert.Equal(t, "en", decoded.Document.GetLocaleOverlay().GetLocale())
}

func TestVersionContentSnapshotRequiresMigratedCatalog(t *testing.T) {
	encoded := requireVersionContentSnapshot(t, "en", nil, nil)
	for _, fingerprint := range []string{"de190638c6014cc2ac80efce5f747b30d20cc024f3d0820c87ba6c1f1a56e16e", "2db3b07d50a139a670f68e3cd045063d20475a102acbee7852066b1929b7172b", "7ee996e5a23685d3b981268c3dd6608d8f006f5b62dbec5fb35e1b4d0532cf49", "ffc51f7192e406fe25fcb5383428be94e74a52db83c666b550ca38d4db88dc85", "stale-catalog"} {
		require.NotEqual(t, fingerprint, contentv1.ContentBlockCatalogFingerprint)
		stored := bytes.ReplaceAll(encoded, []byte(contentv1.ContentBlockCatalogFingerprint), []byte(fingerprint))
		_, err := DecodeVersionContentSnapshot(stored)
		require.ErrorContains(t, err, "fingerprint")
	}
}

func TestVersionContentSnapshotCanonicalizesJSONBRoundTripForCheckpointDedupe(t *testing.T) {
	t.Parallel()

	title := "Checkpoint title"
	summary := "Checkpoint summary"
	canonical := requireVersionContentSnapshot(t, "en", &title, &summary)

	var jsonbValue map[string]any
	require.NoError(t, json.Unmarshal(canonical, &jsonbValue))
	jsonbRoundTrip, err := json.MarshalIndent(jsonbValue, "", "  ")
	require.NoError(t, err)
	require.False(t, bytes.Equal(canonical, jsonbRoundTrip))

	decoded, err := DecodeVersionContentSnapshot(jsonbRoundTrip)
	require.NoError(t, err)
	reencoded, err := EncodeVersionContentSnapshot(
		decoded.SourceLocale,
		decoded.Title,
		decoded.Summary,
		decoded.Document,
	)
	require.NoError(t, err)
	require.True(t, bytes.Equal(canonical, reencoded))
}

func TestVersionContentSnapshotRejectsUnknownEnvelopeAndDocumentFields(t *testing.T) {
	t.Parallel()

	base := requireVersionContentSnapshot(t, "en", nil, nil)
	var missing map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(base, &missing))
	delete(missing, "title")
	missingTitle, err := json.Marshal(missing)
	require.NoError(t, err)
	_, err = DecodeVersionContentSnapshot(missingTitle)
	require.ErrorContains(t, err, `missing "title"`)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(base, &fields))
	fields["legacyContent"] = json.RawMessage(`{}`)
	unknownEnvelope, err := json.Marshal(fields)
	require.NoError(t, err)
	_, err = DecodeVersionContentSnapshot(unknownEnvelope)
	require.ErrorContains(t, err, "unknown fields")

	fields = nil
	require.NoError(t, json.Unmarshal(base, &fields))
	var document map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["document"], &document))
	document["legacyContent"] = json.RawMessage(`{}`)
	fields["document"], err = json.Marshal(document)
	require.NoError(t, err)
	unknownDocument, err := json.Marshal(fields)
	require.NoError(t, err)
	_, err = DecodeVersionContentSnapshot(unknownDocument)
	require.ErrorContains(t, err, "unknown field")

	fields = nil
	require.NoError(t, json.Unmarshal(base, &fields))
	document = nil
	require.NoError(t, json.Unmarshal(fields["document"], &document))
	document["blockCatalogFingerprint"] = json.RawMessage(`"stale-catalog"`)
	fields["document"], err = json.Marshal(document)
	require.NoError(t, err)
	staleCatalogDocument, err := json.Marshal(fields)
	require.NoError(t, err)
	_, err = DecodeVersionContentSnapshot(staleCatalogDocument)
	require.ErrorContains(t, err, "fingerprint")
}

func requireVersionContentSnapshot(t *testing.T, sourceLocale string, title, summary *string) []byte {
	t.Helper()
	snapshot, err := EncodeVersionContentSnapshot(sourceLocale, title, summary, &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Locale:                  sourceLocale,
		Base:                    &contentv1.PageSectionGraph{},
		LocaleOverlay: &contentv1.PageLocaleOverlay{
			Locale: sourceLocale,
		},
	})
	require.NoError(t, err)
	return snapshot
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestStaleCatalogSnapshotKeepsEarlierCheckpointsAsHistory(t *testing.T) {
	t.Parallel()
	// Checkpoints written before a block catalog change carry the earlier fingerprint
	// and must not be decoded against the current catalog when the next edit checkpoints.
	assert.True(t, staleCatalogSnapshot([]byte(`{"document":{"blockCatalogFingerprint":"732d62ad"}}`)))
	assert.False(t, staleCatalogSnapshot([]byte(`{"document":{"blockCatalogFingerprint":"`+contentv1.ContentBlockCatalogFingerprint+`"}}`)))
	assert.False(t, staleCatalogSnapshot([]byte(`{"document":{}}`)))
	assert.False(t, staleCatalogSnapshot([]byte(`not json`)))
}
