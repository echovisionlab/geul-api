package translation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAIResponseRestoresPlaceholderReferencesFromPlainSource(t *testing.T) {
	t.Parallel()
	request := testXLIFFRequest(testProfile("email_template", "en", "tr", false, nil), XLIFFGroup{
		ID: "meta", TranslationUnit: []XLIFFUnit{{ID: "subject", Source: "Passkey added to {{site.title}}"}},
	})
	request.Document.File.ID = "email_template:test"
	payload, err := buildTranslationAIUserPrompt(request)
	require.NoError(t, err)
	var encoded translationAIRequestPayload
	require.NoError(t, json.Unmarshal([]byte(payload), &encoded))
	inline := encoded.Bundles[0].Units[0].SourceInline
	response, err := parseTranslationProviderResponse(request, translationJSONResponse("meta", translationJSONUnit{
		UnitID: "subject", TranslatedInline: strings.TrimPrefix(inline, "Passkey added to ") + " için geçiş anahtarı eklendi",
	}))
	require.NoError(t, err)
	require.Equal(t, `Passkey added to <ph id="p1"></ph>`, inline)
	unit := response.Document.File.Groups[0].TranslationUnit[0]
	require.Equal(t, "{{site.title}} için geçiş anahtarı eklendi", *unit.Target)
	require.Equal(t, "d1", unit.TargetInline[0].DataRef)
	require.Equal(t, "no", unit.TargetInline[0].CanDelete)
	require.True(t, ValidateResponse(request.Document, *response).Passed)
	require.Empty(t, request.Document.File.Groups[0].TranslationUnit[0].OriginalData, "request remains immutable")
}

func TestAIResponseRestoresStyledProtectedContentWithoutModelOwnedReferences(t *testing.T) {
	t.Parallel()
	request := testXLIFFRequest(testProfile("post", "en", "tr", false, []string{"rawsector"}), XLIFFGroup{
		ID: "body", TranslationUnit: []XLIFFUnit{{
			ID: "u1", Source: "Hello rawsector",
			OriginalData: []XLIFFOriginalData{{ID: "start", Value: ""}, {ID: "end", Value: ""}, {ID: "brand", Value: "rawsector"}},
			SourceInline: []XLIFFInline{{Kind: XLIFFInlinePairedCode, ID: "style", DataRefStart: "start", DataRefEnd: "end", CanDelete: "no", Children: []XLIFFInline{
				{Kind: XLIFFInlineText, Text: "Hello "}, {Kind: XLIFFInlinePlaceholder, ID: "name", DataRef: "brand", CanCopy: "no", CanDelete: "no"},
			}}},
		}},
	})
	response, err := parseTranslationProviderResponse(request, translationJSONResponse("body", translationJSONUnit{
		UnitID: "u1", TranslatedInline: `<pc id="style">Merhaba <ph id="name"></ph></pc>`,
	}))
	require.NoError(t, err)
	unit := response.Document.File.Groups[0].TranslationUnit[0]
	require.Equal(t, "Merhaba rawsector", *unit.Target)
	require.Equal(t, "start", unit.TargetInline[0].DataRefStart)
	require.Equal(t, "brand", unit.TargetInline[0].Children[1].DataRef)

	for _, inline := range []string{
		`<pc id="style">Merhaba rawsector</pc>`,
		`<pc id="style">Merhaba <ph id="other"></ph></pc>`,
		`<pc id="style">Merhaba <ph id="name"></ph><ph id="name"></ph></pc>`,
		`<pc id="style">Merhaba </pc><ph id="name"></ph>`,
		`<pc id="style">Merhaba <pc id="name"></pc></pc>`,
		`<pc id="style" dataRefStart="start" dataRefEnd="end">Merhaba <ph id="name"></ph></pc>`,
	} {
		_, err := parseTranslationProviderResponse(request, translationJSONResponse("body", translationJSONUnit{UnitID: "u1", TranslatedInline: inline}))
		require.ErrorIs(t, err, ErrProviderResponseInvalid, inline)
	}
}

func TestAIResponseMapsReorderedGroupsAndUnitsByExactIdentity(t *testing.T) {
	t.Parallel()
	request := testXLIFFRequest(testProfile("post", "en", "fr", false, nil),
		XLIFFGroup{ID: "meta", TranslationUnit: []XLIFFUnit{{ID: "title", Source: "Hello"}, {ID: "summary", Source: "World"}}},
		XLIFFGroup{ID: "body", TranslationUnit: []XLIFFUnit{{ID: "text", Source: "Thanks"}}},
	)
	response, err := parseTranslationProviderResponse(request, mustJSON(translationJSONEnvelope{Bundles: []translationJSONBundle{
		{BundleID: "body", Units: []translationJSONUnit{{UnitID: "text", TranslatedInline: "Merci"}}},
		{BundleID: "meta", Units: []translationJSONUnit{{UnitID: "summary", TranslatedInline: "Monde"}, {UnitID: "title", TranslatedInline: "Bonjour"}}},
	}}))
	require.NoError(t, err)
	require.Equal(t, "meta", response.Document.File.Groups[0].ID)
	require.Equal(t, "title", response.Document.File.Groups[0].TranslationUnit[0].ID)
	require.Equal(t, "Bonjour", *response.Document.File.Groups[0].TranslationUnit[0].Target)

	for _, bundles := range [][]translationJSONBundle{
		{{BundleID: "meta", Units: []translationJSONUnit{{UnitID: "title", TranslatedInline: "Bonjour"}, {UnitID: "text", TranslatedInline: "Merci"}}}, {BundleID: "body", Units: []translationJSONUnit{{UnitID: "summary", TranslatedInline: "Monde"}}}},
		{{BundleID: "meta"}, {BundleID: "meta"}},
	} {
		_, err := parseTranslationProviderResponse(request, mustJSON(translationJSONEnvelope{Bundles: bundles}))
		require.ErrorIs(t, err, ErrProviderResponseInvalid)
	}
}
