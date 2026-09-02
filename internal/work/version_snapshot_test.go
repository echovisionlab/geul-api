package work

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkVersionProtoFormatting(t *testing.T) {
	createdAt := time.Unix(1_700_000_000, 0).UTC()
	title := "Snapshot title"
	summary := "Snapshot summary"
	snapshot, err := EncodeVersionContentSnapshot(
		"en", &title, &summary,
		&contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			Locale:                  "en", Base: &contentv1.RichTextBlockGraph{},
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en"},
		},
	)
	require.NoError(t, err)
	version, err := (&WorkService{}).toProtoWorkVersion(&model.WorkVersion{
		ID: "work-version-1", Version: 4, Title: &title, Summary: &summary,
		ContentSnapshot:      snapshot,
		ContributorMemberIDs: []string{"member-2", "member-1", "member-2"},
		CreatedAt:            createdAt,
	}, map[string]string{"member-1": "Mina", "member-2": "Jules"})
	require.NoError(t, err)
	digest := sha256.Sum256(snapshot)
	require.Equal(t, fmt.Sprintf("%x", digest[:]), version.CanonicalHash)
	require.Equal(t, []string{"member-1", "member-2"}, []string{
		version.Contributors[0].MemberId,
		version.Contributors[1].MemberId,
	})
}

func TestNormalizeContributorMemberIDs(t *testing.T) {
	require.Equal(t, []string{"member-1", "member-2"}, []string(
		normalizeContributorMemberIDs([]string{" member-2 ", "", "member-1", "member-2"}),
	))
	require.Empty(t, normalizeContributorMemberIDs([]string{"", "   "}))
}

func TestVersionContentSnapshotRoundTrip(t *testing.T) {
	title := "Title"
	summary := "Summary"
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: "catalog",
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		Locale:                  "en",
		Base:                    &contentv1.RichTextBlockGraph{},
		LocaleOverlay:           &contentv1.RichTextLocaleOverlay{Locale: "en"},
	}

	encoded, err := EncodeVersionContentSnapshot("en", &title, &summary, document)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"schemaVersion": 1,
		"sourceLocale": "en",
		"title": "Title",
		"summary": "Summary",
		"document": {
			"blockCatalogFingerprint": "catalog",
			"profile": "RICH_TEXT_PROFILE_WORK",
			"locale": "en",
			"base": {},
			"localeOverlay": {"locale": "en"}
		}
	}`, string(encoded))

	snapshot, decoded, err := DecodeVersionContentSnapshot(encoded)
	require.NoError(t, err)
	require.Equal(t, "en", snapshot.SourceLocale)
	require.Equal(t, title, *snapshot.Title)
	require.Equal(t, summary, *snapshot.Summary)
	require.Equal(t, document, decoded)
}

func TestDecodeVersionContentSnapshotFailsClosed(t *testing.T) {
	tests := []string{
		`{"schemaVersion":2,"sourceLocale":"en","title":null,"summary":null,"document":{}}`,
		`{"schemaVersion":1,"sourceLocale":"en","title":null,"summary":null,"document":{},"extra":true}`,
		`{"schemaVersion":1,"sourceLocale":"en","title":null,"summary":null,"document":{"profile":"RICH_TEXT_PROFILE_POST","locale":"en"}}`,
		`{"schemaVersion":1,"sourceLocale":"en","title":null,"summary":null,"document":{"profile":"RICH_TEXT_PROFILE_WORK","locale":"ko"}}`,
	}

	for _, data := range tests {
		_, _, err := DecodeVersionContentSnapshot([]byte(data))
		require.Error(t, err)
	}
}
