package page

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
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

	version, err := (&PageService{}).toProtoPageVersion(&model.PageVersion{
		ID: "page-version-1", Version: 3, Title: &title, Summary: &summary,
		ContentSnapshot:      contentSnapshot,
		ContributorMemberIDs: []string{"member-2", "member-1", "member-2"},
		CreatedAt:            createdAt,
	}, map[string]string{"member-1": "Mina", "member-2": "Jules"})
	require.NoError(t, err)
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
