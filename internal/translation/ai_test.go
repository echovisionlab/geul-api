package translation

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type typedNilAIProvider struct{}

type rawResponseAIProvider struct{ text string }

type rawResponseAISession struct{ text string }

func (*typedNilAIProvider) GenerateText(context.Context, llm.GenerationRequest) (string, error) {
	return "", nil
}

func (*typedNilAIProvider) StartSession(context.Context, llm.SessionSpec) (llm.Session, error) {
	return nil, nil
}

func (*typedNilAIProvider) ProviderName() string { return "typed-nil" }

func (*typedNilAIProvider) ModelName() string { return "typed-nil" }

func (p rawResponseAIProvider) GenerateText(context.Context, llm.GenerationRequest) (string, error) {
	return p.text, nil
}

func (p rawResponseAIProvider) StartSession(context.Context, llm.SessionSpec) (llm.Session, error) {
	return rawResponseAISession(p), nil
}

func (rawResponseAIProvider) ProviderName() string { return "raw-test" }

func (rawResponseAIProvider) ModelName() string { return "raw-test-model" }

func (s rawResponseAISession) GenerateText(context.Context, llm.SessionTurn) (string, error) {
	return s.text, nil
}

func (rawResponseAISession) Close(context.Context) error { return nil }

func TestNewAIGeneratorRejectsTypedNilProvider(t *testing.T) {
	t.Parallel()
	var provider *typedNilAIProvider
	require.Panics(t, func() { NewAIGenerator(provider) })
}

func TestAIGeneratorRetainsExactInvalidRawResponse(t *testing.T) {
	t.Parallel()
	rawBody := "not-json-provider-output"
	request := testXLIFFRequest(
		testProfile("post", "en", "ko", false, nil),
		XLIFFGroup{ID: "body", TranslationUnit: []XLIFFUnit{{ID: "u1", Source: "Hello"}}},
	)
	response, err := NewAIGenerator(rawResponseAIProvider{text: rawBody}).Translate(context.Background(), request)
	require.ErrorIs(t, err, ErrProviderResponseInvalid)
	require.NotNil(t, response)
	require.NotNil(t, response.Raw)
	require.Equal(t, "application/json", response.Raw.MediaType())
	require.Equal(t, []byte(rawBody), response.Raw.Body())
}

func TestAISystemPromptEmphasizesOriginalNameWithOptionalParentheses(t *testing.T) {
	t.Parallel()

	prompt := translationAISystemPrompt(ProviderRequest{
		RequestID:   "job-name-prompt-1",
		OperationID: "operation-name-prompt",
		Profile: GenerationProfile{
			SourceLocale:   "en",
			TargetLocale:   "ko",
			MIMEType:       "text/plain",
			PreserveMarkup: false,
			ProtectedTerms: []string{"Garcia Martinez"},
			ContentKind:    ContentKindEditorial,
			TargetRegister: RegisterNeutralPlain,
			RegisterPolicy: RegisterPolicyTargetDefault,
			StyleInstructions: []string{
				"Use neutral written Korean plain style ending in -다 or -한다; " +
					"do not mix in polite -습니다 or -요 endings unless the content is direct user guidance.",
			},
		},
	})

	assert.Contains(t, prompt, "keep the original source-language form")
	assert.Contains(t, prompt, "keep the original source-language form in the translation. Only if")
	assert.Contains(t, prompt, "Original Name (Localized Name)")
	assert.Contains(t, prompt, "keep the original source form first")
	assert.Contains(t, prompt, "Preserve literal newline characters within each unit")
	assert.Contains(t, prompt, "Target register: neutral_plain")
	assert.Contains(t, prompt, "Use neutral written Korean plain style")
}

func TestParseTranslationProviderResponseRejectsRawContractViolations(t *testing.T) {
	t.Parallel()

	request := testXLIFFRequest(
		testProfile("email_template", "en", "ko", false, nil),
		XLIFFGroup{ID: "entity:meta", TranslationUnit: []XLIFFUnit{
			{ID: "entity:subject", Source: "Hello {{name}} {{name}}"},
			{ID: "entity:body", Source: "Welcome"},
		}},
	)
	tests := []struct {
		name      string
		response  string
		errorText string
	}{
		{
			name: "unknown unit",
			response: translationJSONResponse("entity:meta",
				translationJSONUnit{UnitID: "entity:subject", TranslatedInline: "안녕하세요 {{name}} {{name}}"},
				translationJSONUnit{UnitID: "entity:body", TranslatedInline: "환영합니다"},
				translationJSONUnit{UnitID: "entity:unknown", TranslatedInline: "알 수 없음"},
			),
			errorText: "unknown units: entity:unknown",
		},
		{
			name: "duplicate unit",
			response: translationJSONResponse("entity:meta",
				translationJSONUnit{UnitID: "entity:subject", TranslatedInline: "안녕하세요 {{name}} {{name}}"},
				translationJSONUnit{UnitID: "entity:subject", TranslatedInline: "안녕 {{name}} {{name}}"},
				translationJSONUnit{UnitID: "entity:body", TranslatedInline: "환영합니다"},
			),
			errorText: "duplicate units: entity:subject",
		},
		{
			name: "missing unit",
			response: translationJSONResponse("entity:meta",
				translationJSONUnit{UnitID: "entity:subject", TranslatedInline: "안녕하세요 {{name}} {{name}}"},
			),
			errorText: "missing units: entity:body",
		},
		{
			name: "placeholder cardinality",
			response: translationJSONResponse("entity:meta",
				translationJSONUnit{UnitID: "entity:subject", TranslatedInline: "안녕하세요 {{name}} {{other}}"},
				translationJSONUnit{UnitID: "entity:body", TranslatedInline: "환영합니다"},
			),
			errorText: "placeholder mismatch units: entity:subject",
		},
		{
			name: "empty target",
			response: translationJSONResponse("entity:meta",
				translationJSONUnit{UnitID: "entity:subject", TranslatedInline: ""},
				translationJSONUnit{UnitID: "entity:body", TranslatedInline: "환영합니다"},
			),
			errorText: `XLIFF unit "entity:subject" target is empty`,
		},
		{
			name: "changed group",
			response: translationJSONResponse("other:group",
				translationJSONUnit{UnitID: "entity:subject", TranslatedInline: "안녕하세요 {{name}} {{name}}"},
				translationJSONUnit{UnitID: "entity:body", TranslatedInline: "환영합니다"},
			),
			errorText: `XLIFF group "entity:meta" changed`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseTranslationProviderResponse(request, tt.response)
			require.ErrorIs(t, err, ErrProviderResponseInvalid)
			require.NotContains(t, err.Error(), tt.errorText, "provider response detail must not cross the bounded failure boundary")
		})
	}
}

func TestParseTranslationProviderResponseNormalizesOnlyAfterValidation(t *testing.T) {
	t.Parallel()

	request := testXLIFFRequest(
		testProfile("page", "en", "fr", false, nil),
		XLIFFGroup{ID: "entity:meta", TranslationUnit: []XLIFFUnit{{ID: "entity:title", Source: "Hello"}}},
	)
	response, err := parseTranslationProviderResponse(
		request,
		translationJSONResponse("entity:meta", translationJSONUnit{UnitID: "entity:title", TranslatedInline: "Bonjour"}),
	)
	require.NoError(t, err)
	require.Equal(t, "Bonjour", XLIFFTargets(response.Document)["entity:title"].TranslatedText)
}

func TestParseTranslationProviderResponseKeepsExplicitEmptySourceUnit(t *testing.T) {
	t.Parallel()
	request := testXLIFFRequest(
		testProfile("post", "en", "ko", false, nil),
		XLIFFGroup{ID: "body", TranslationUnit: []XLIFFUnit{{ID: "block:empty", Source: ""}}},
	)
	response, err := parseTranslationProviderResponse(
		request,
		translationJSONResponse("body", translationJSONUnit{UnitID: "block:empty", TranslatedInline: ""}),
	)
	require.NoError(t, err)
	target := XLIFFTargets(response.Document)["block:empty"]
	require.Equal(t, "", target.TranslatedText)
}

func TestParseTranslationProviderResponseRejectsTextOutsideStyledRun(t *testing.T) {
	t.Parallel()
	request := testXLIFFRequest(testProfile("post", "en", "ko", false, nil), XLIFFGroup{
		ID: "body", TranslationUnit: []XLIFFUnit{{
			ID: "u1", Source: "Hello", OriginalData: []XLIFFOriginalData{{ID: "d1"}, {ID: "d2"}},
			SourceInline: []XLIFFInline{{Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d1", DataRefEnd: "d2", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "Hello"}}}},
		}},
	})
	_, err := parseTranslationProviderResponse(request, translationJSONResponse("body", translationJSONUnit{
		UnitID: "u1", TranslatedInline: `안녕<pc id="r1" dataRefStart="d1" dataRefEnd="d2"></pc>`,
	}))
	require.ErrorIs(t, err, ErrProviderResponseInvalid)
}

func TestNormalizeProtectedTermsPreservesExactSpellings(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"React Native", "react native"}, NormalizeProtectedTerms([]string{
		" React Native ", "React Native", "react native", " ",
	}))
}

func translationJSONResponse(groupID string, units ...translationJSONUnit) string {
	return fmt.Sprintf(`{"bundles":[{"bundle_id":%q,"units":%s}]}`, groupID, mustJSON(units))
}

func mustJSON(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(body)
}
