package application

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTranslationProviderConfigReusesExistingLLMSecretAndDefaults(t *testing.T) {
	t.Parallel()

	inputPrice := 0.12
	outputPrice := 0.45
	maxContextTokens := int32(128_000)
	maxOutputTokens := int32(4096)
	temperature := 0.35
	existing := &Provider{Type: ProviderTypeLLM}
	require.NoError(t, existing.SetConfig(&LLMConfig{
		APIKey:                        "stored-key",
		Preset:                        LLMPresetOpenAICompatible,
		APIBaseURL:                    "https://llm.example.com/v1",
		Model:                         "stored-model",
		InputTokenPriceUSDPerMillion:  &inputPrice,
		OutputTokenPriceUSDPerMillion: &outputPrice,
		MaxContextTokens:              &maxContextTokens,
		MaxOutputTokens:               &maxOutputTokens,
		SupportsJSONMode:              true,
		Temperature:                   &temperature,
	}))

	cfg, err := buildTranslationProviderConfig(t.Context(),
		ProviderTypeLLM,
		&LLMConfigInput{SupportsJSONMode: false},
		nil,
		existing,
	)
	require.NoError(t, err)

	llmCfg, ok := cfg.(*LLMConfig)
	require.True(t, ok)
	assert.Equal(t, "stored-key", llmCfg.APIKey)
	assert.Equal(t, LLMPresetOpenAICompatible, llmCfg.Preset)
	assert.Equal(t, "https://llm.example.com/v1", llmCfg.APIBaseURL)
	assert.Equal(t, "stored-model", llmCfg.Model)
	assert.Equal(t, &inputPrice, llmCfg.InputTokenPriceUSDPerMillion)
	assert.Equal(t, &outputPrice, llmCfg.OutputTokenPriceUSDPerMillion)
	assert.Equal(t, &maxContextTokens, llmCfg.MaxContextTokens)
	assert.Equal(t, &maxOutputTokens, llmCfg.MaxOutputTokens)
	assert.False(t, llmCfg.SupportsJSONMode)
	assert.Equal(t, &temperature, llmCfg.Temperature)
}

func TestBuildTranslationProviderConfigReusesDeepLSecretAndDefaultsBaseURL(t *testing.T) {
	t.Parallel()

	existing := &Provider{Type: ProviderTypeDeepL}
	require.NoError(t, existing.SetConfig(&DeepLConfig{
		APIKey: "stored-deepl-key",
	}))

	cfg, err := buildTranslationProviderConfig(t.Context(),
		ProviderTypeDeepL,
		nil,
		&DeepLConfigInput{},
		existing,
	)
	require.NoError(t, err)

	deeplCfg, ok := cfg.(*DeepLConfig)
	require.True(t, ok)
	assert.Equal(t, "stored-deepl-key", deeplCfg.APIKey)
	assert.Equal(t, translation.DefaultDeepLAPIBaseURL, deeplCfg.APIBaseURL)
}

func TestBuildTranslationProviderConfigRejectsMissingConfigAndUnknownType(t *testing.T) {
	t.Parallel()

	_, err := buildTranslationProviderConfig(t.Context(), ProviderTypeLLM, nil, nil, nil)
	require.ErrorContains(t, err, "LLM config is required")

	_, err = buildTranslationProviderConfig(t.Context(), ProviderTypeDeepL, nil, nil, nil)
	require.ErrorContains(t, err, "DeepL config is required")

	_, err = buildTranslationProviderConfig(t.Context(), "unknown", nil, nil, nil)
	require.ErrorContains(t, err, "unknown translation provider type")
}

func TestTranslationProviderProtoConversionMasksSecrets(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_001_000, 0).UTC()
	inputPrice := 0.2
	maxOutputTokens := int32(8192)
	temperature := 0.1
	provider := &Provider{
		ID:        "provider-1",
		Name:      "Primary LLM",
		Type:      ProviderTypeLLM,
		IsActive:  true,
		Priority:  5,
		CreatedAt: now,
		UpdatedAt: now.Add(time.Minute),
	}
	require.NoError(t, provider.SetConfig(&LLMConfig{
		APIKey:                       "secret-key",
		Preset:                       LLMPresetOpenAICompatible,
		APIBaseURL:                   "https://llm.example.com/v1",
		Model:                        "model-a",
		InputTokenPriceUSDPerMillion: &inputPrice,
		MaxOutputTokens:              &maxOutputTokens,
		SupportsJSONMode:             true,
		Temperature:                  &temperature,
	}))

	masked := providerMessage(t.Context(), provider)
	require.NotNil(t, masked)
	assert.Equal(t, "provider-1", masked.Id)
	assert.Equal(t, managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM, masked.Type)
	assert.Equal(t, int32(5), masked.Priority)
	require.NotNil(t, masked.CreatedAt)
	require.NotNil(t, masked.UpdatedAt)
	llmMasked := masked.GetLlmConfig()
	require.NotNil(t, llmMasked)
	assert.Empty(t, llmMasked.ApiKey)
	assert.Equal(t, "https://llm.example.com/v1", llmMasked.GetApiBaseUrl())
	assert.Equal(t, "model-a", llmMasked.Model)
	assert.True(t, llmMasked.SupportsJsonMode)
	assert.Equal(t, &inputPrice, llmMasked.InputTokenPriceUsdPerMillion)
	assert.Equal(t, &maxOutputTokens, llmMasked.MaxOutputTokens)
	assert.Equal(t, &temperature, llmMasked.Temperature)
}

func TestTranslationProviderProtoConversionHandlesDeepLAndUnknown(t *testing.T) {
	t.Parallel()

	deepl := &Provider{
		ID:        "provider-2",
		Name:      "DeepL",
		Type:      ProviderTypeDeepL,
		CreatedAt: time.Unix(1_700_001_100, 0).UTC(),
		UpdatedAt: time.Unix(1_700_001_200, 0).UTC(),
	}
	require.NoError(t, deepl.SetConfig(&DeepLConfig{
		APIKey:     "deepl-secret",
		APIBaseURL: "https://api-free.deepl.com",
	}))

	masked := providerMessage(t.Context(), deepl)
	require.NotNil(t, masked.GetDeeplConfig())
	assert.Empty(t, masked.GetDeeplConfig().ApiKey)
	assert.Equal(t, "https://api-free.deepl.com", masked.GetDeeplConfig().GetApiBaseUrl())

	unknown := providerMessage(t.Context(), &Provider{
		Type:      "unknown",
		CreatedAt: time.Unix(1_700_001_300, 0).UTC(),
		UpdatedAt: time.Unix(1_700_001_400, 0).UTC(),
	})
	assert.Equal(
		t,
		managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_UNSPECIFIED,
		unknown.Type,
	)
	assert.Nil(t, unknown.Config)
}

func TestTranslationProviderResolverAndConversionHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		ProviderTypeLLM,
		providerTypeFromMessage(managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM),
	)
	assert.Equal(
		t,
		managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_DEEPL,
		providerTypeMessage(ProviderTypeDeepL),
	)
	assert.Empty(t, providerTypeFromMessage(managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_UNSPECIFIED))
	assert.Empty(t, providerTypeFromMessage(managev1.TranslationProviderType(99)))
	assert.Equal(
		t,
		managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE,
		llmPresetMessage(LLMPresetOpenAICompatible),
	)
	assert.Empty(
		t,
		llmPresetFromMessage(managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED),
	)
	// An unknown preset neither passes as a stored preset nor inherits one.
	unknownPreset := llmPresetFromMessage(managev1.TranslationLLMProviderPreset(99))
	assert.NotEmpty(t, unknownPreset)
	assert.Empty(t, knownLLMPreset(unknownPreset))
	assert.Equal(
		t,
		managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED,
		llmPresetMessage("unknown"),
	)

	value := 0.75
	value32 := float32PtrFromFloat64Ptr(&value)
	require.NotNil(t, value32)
	assert.Equal(t, float32(0.75), *value32)
	assert.Nil(t, float32PtrFromFloat64Ptr(nil))

	provider := &Provider{}
	assert.Same(t, provider, translationProviderExistingConfig(provider, false))
	assert.Nil(t, translationProviderExistingConfig(provider, true))

}

func TestTranslationGeneratorFromProviderRejectsInvalidConfigs(t *testing.T) {
	t.Parallel()

	_, err := newTranslationGeneratorFromProvider(nil)
	require.ErrorIs(t, err, errTranslationProviderUnavailable)

	_, err = newTranslationGeneratorFromProvider(&Provider{Type: "unknown"})
	require.ErrorContains(t, err, "unsupported translation provider type")

	badJSON := &Provider{
		Type:   ProviderTypeLLM,
		Config: []byte(`{`),
	}
	_, err = newTranslationGeneratorFromProvider(badJSON)
	require.Error(t, err)

	badPreset := &Provider{Type: ProviderTypeLLM}
	require.NoError(t, badPreset.SetConfig(&LLMConfig{
		APIKey: "test-key",
		Preset: "unknown",
		Model:  "model-a",
	}))
	_, err = newTranslationGeneratorFromProvider(badPreset)
	require.ErrorContains(t, err, "unsupported LLM provider preset")

	badDeepL := &Provider{Type: ProviderTypeDeepL}
	require.NoError(t, badDeepL.SetConfig(&DeepLConfig{
		APIKey:     "test-key",
		APIBaseURL: "not a url",
	}))
	_, err = newTranslationGeneratorFromProvider(badDeepL)
	require.ErrorContains(t, err, "invalid DeepL API base URL")
}

func TestTranslationLLMTextProviderBuildsGeminiAndOpenAICompatible(t *testing.T) {
	t.Parallel()

	geminiProvider, err := newTranslationLLMTextProvider(&LLMConfig{
		APIKey: "test-key",
		Preset: LLMPresetGemini,
	})
	require.NoError(t, err)
	assert.Equal(t, "gemini", geminiProvider.ProviderName())
	assert.Equal(t, defaultTranslationLLMGeminiModel, geminiProvider.ModelName())

	temperature := 0.2
	maxOutputTokens := int32(2048)
	openAIProvider, err := newTranslationLLMTextProvider(&LLMConfig{
		APIKey:           "test-key",
		Preset:           LLMPresetOpenAICompatible,
		APIBaseURL:       "https://llm.example.com/v1",
		Model:            "translation-model",
		SupportsJSONMode: true,
		Temperature:      &temperature,
		MaxOutputTokens:  &maxOutputTokens,
	})
	require.NoError(t, err)
	assert.Equal(t, "openai-compatible", openAIProvider.ProviderName())
	assert.Equal(t, "translation-model", openAIProvider.ModelName())

	_, err = newTranslationLLMTextProvider(nil)
	require.ErrorContains(t, err, "LLM config is required")

	_, err = newTranslationLLMTextProvider(&LLMConfig{})
	require.ErrorContains(t, err, "LLM API key is required")
}
