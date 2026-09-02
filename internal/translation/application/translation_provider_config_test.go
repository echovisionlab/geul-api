package application

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
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
	existing := &model.TranslationProviderConfig{Type: model.TranslationProviderTypeLLM}
	require.NoError(t, existing.SetConfig(&model.LLMTranslationProviderConfig{
		APIKey:                        "stored-key",
		Preset:                        model.TranslationLLMProviderPresetOpenAICompatible,
		APIBaseURL:                    "https://llm.example.com/v1",
		Model:                         "stored-model",
		InputTokenPriceUSDPerMillion:  &inputPrice,
		OutputTokenPriceUSDPerMillion: &outputPrice,
		MaxContextTokens:              &maxContextTokens,
		MaxOutputTokens:               &maxOutputTokens,
		SupportsJSONMode:              true,
		Temperature:                   &temperature,
	}))

	cfg, err := buildTranslationProviderConfig(
		model.TranslationProviderTypeLLM,
		&managev1.LLMTranslationProviderConfig{
			SupportsJsonMode: false,
		},
		nil,
		existing,
	)
	require.NoError(t, err)

	llmCfg, ok := cfg.(*model.LLMTranslationProviderConfig)
	require.True(t, ok)
	assert.Equal(t, "stored-key", llmCfg.APIKey)
	assert.Equal(t, model.TranslationLLMProviderPresetOpenAICompatible, llmCfg.Preset)
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

	existing := &model.TranslationProviderConfig{Type: model.TranslationProviderTypeDeepL}
	require.NoError(t, existing.SetConfig(&model.DeepLTranslationProviderConfig{
		APIKey: "stored-deepl-key",
	}))

	cfg, err := buildTranslationProviderConfig(
		model.TranslationProviderTypeDeepL,
		nil,
		&managev1.DeepLTranslationProviderConfig{},
		existing,
	)
	require.NoError(t, err)

	deeplCfg, ok := cfg.(*model.DeepLTranslationProviderConfig)
	require.True(t, ok)
	assert.Equal(t, "stored-deepl-key", deeplCfg.APIKey)
	assert.Equal(t, translation.DefaultDeepLAPIBaseURL, deeplCfg.APIBaseURL)
}

func TestBuildTranslationProviderConfigRejectsMissingConfigAndUnknownType(t *testing.T) {
	t.Parallel()

	_, err := buildTranslationProviderConfig(model.TranslationProviderTypeLLM, nil, nil, nil)
	require.ErrorContains(t, err, "LLM config is required")

	_, err = buildTranslationProviderConfig(model.TranslationProviderTypeDeepL, nil, nil, nil)
	require.ErrorContains(t, err, "DeepL config is required")

	_, err = buildTranslationProviderConfig("unknown", nil, nil, nil)
	require.ErrorContains(t, err, "unknown translation provider type")
}

func TestTranslationProviderProtoConversionMasksSecrets(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_001_000, 0).UTC()
	inputPrice := 0.2
	maxOutputTokens := int32(8192)
	temperature := 0.1
	provider := &model.TranslationProviderConfig{
		ID:        "provider-1",
		Name:      "Primary LLM",
		Type:      model.TranslationProviderTypeLLM,
		IsActive:  true,
		Priority:  5,
		CreatedAt: now,
		UpdatedAt: now.Add(time.Minute),
	}
	require.NoError(t, provider.SetConfig(&model.LLMTranslationProviderConfig{
		APIKey:                       "secret-key",
		Preset:                       model.TranslationLLMProviderPresetOpenAICompatible,
		APIBaseURL:                   "https://llm.example.com/v1",
		Model:                        "model-a",
		InputTokenPriceUSDPerMillion: &inputPrice,
		MaxOutputTokens:              &maxOutputTokens,
		SupportsJSONMode:             true,
		Temperature:                  &temperature,
	}))

	masked := toProtoTranslationProvider(provider, false)
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

	withSecrets := toProtoTranslationProvider(provider, true)
	require.NotNil(t, withSecrets.GetLlmConfig())
	assert.Equal(t, "secret-key", withSecrets.GetLlmConfig().ApiKey)
}

func TestTranslationProviderProtoConversionHandlesDeepLNilAndUnknown(t *testing.T) {
	t.Parallel()

	assert.Nil(t, toProtoTranslationProvider(nil, false))

	deepl := &model.TranslationProviderConfig{
		ID:        "provider-2",
		Name:      "DeepL",
		Type:      model.TranslationProviderTypeDeepL,
		CreatedAt: time.Unix(1_700_001_100, 0).UTC(),
		UpdatedAt: time.Unix(1_700_001_200, 0).UTC(),
	}
	require.NoError(t, deepl.SetConfig(&model.DeepLTranslationProviderConfig{
		APIKey:     "deepl-secret",
		APIBaseURL: "https://api-free.deepl.com",
	}))

	masked := toProtoTranslationProvider(deepl, false)
	require.NotNil(t, masked.GetDeeplConfig())
	assert.Empty(t, masked.GetDeeplConfig().ApiKey)
	assert.Equal(t, "https://api-free.deepl.com", masked.GetDeeplConfig().GetApiBaseUrl())

	withSecrets := toProtoTranslationProvider(deepl, true)
	assert.Equal(t, "deepl-secret", withSecrets.GetDeeplConfig().ApiKey)

	unknown := toProtoTranslationProvider(&model.TranslationProviderConfig{
		Type:      "unknown",
		CreatedAt: time.Unix(1_700_001_300, 0).UTC(),
		UpdatedAt: time.Unix(1_700_001_400, 0).UTC(),
	}, false)
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
		model.TranslationProviderTypeLLM,
		protoToModelTranslationProviderType(managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_LLM),
	)
	assert.Equal(
		t,
		managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_DEEPL,
		modelToProtoTranslationProviderType(model.TranslationProviderTypeDeepL),
	)
	assert.Empty(t, protoToModelTranslationProviderType(managev1.TranslationProviderType_TRANSLATION_PROVIDER_TYPE_UNSPECIFIED))
	assert.Equal(
		t,
		managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE,
		modelToProtoTranslationLLMProviderPreset(model.TranslationLLMProviderPresetOpenAICompatible),
	)
	assert.Empty(
		t,
		protoToModelTranslationLLMProviderPreset(
			managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED,
		),
	)
	assert.Equal(
		t,
		managev1.TranslationLLMProviderPreset_TRANSLATION_LLM_PROVIDER_PRESET_UNSPECIFIED,
		modelToProtoTranslationLLMProviderPreset("unknown"),
	)

	value := 0.75
	value32 := float32PtrFromFloat64Ptr(&value)
	require.NotNil(t, value32)
	assert.Equal(t, float32(0.75), *value32)
	assert.Nil(t, float32PtrFromFloat64Ptr(nil))

	provider := &model.TranslationProviderConfig{}
	assert.Same(t, provider, translationProviderExistingConfig(provider, false))
	assert.Nil(t, translationProviderExistingConfig(provider, true))

}

func TestTranslationGeneratorFromProviderRejectsInvalidConfigs(t *testing.T) {
	t.Parallel()

	_, err := newTranslationGeneratorFromProvider(nil)
	require.ErrorIs(t, err, errTranslationProviderUnavailable)

	_, err = newTranslationGeneratorFromProvider(&model.TranslationProviderConfig{Type: "unknown"})
	require.ErrorContains(t, err, "unsupported translation provider type")

	badJSON := &model.TranslationProviderConfig{
		Type: model.TranslationProviderTypeLLM,
		Config: model.TranslationProviderConfigJSON{
			RawMessage: []byte(`{`),
		},
	}
	_, err = newTranslationGeneratorFromProvider(badJSON)
	require.Error(t, err)

	badPreset := &model.TranslationProviderConfig{Type: model.TranslationProviderTypeLLM}
	require.NoError(t, badPreset.SetConfig(&model.LLMTranslationProviderConfig{
		APIKey: "test-key",
		Preset: "unknown",
		Model:  "model-a",
	}))
	_, err = newTranslationGeneratorFromProvider(badPreset)
	require.ErrorContains(t, err, "unsupported LLM provider preset")

	badDeepL := &model.TranslationProviderConfig{Type: model.TranslationProviderTypeDeepL}
	require.NoError(t, badDeepL.SetConfig(&model.DeepLTranslationProviderConfig{
		APIKey:     "test-key",
		APIBaseURL: "not a url",
	}))
	_, err = newTranslationGeneratorFromProvider(badDeepL)
	require.ErrorContains(t, err, "invalid DeepL API base URL")
}

func TestTranslationLLMTextProviderBuildsGeminiAndOpenAICompatible(t *testing.T) {
	t.Parallel()

	geminiProvider, err := newTranslationLLMTextProvider(&model.LLMTranslationProviderConfig{
		APIKey: "test-key",
		Preset: model.TranslationLLMProviderPresetGemini,
	})
	require.NoError(t, err)
	assert.Equal(t, "gemini", geminiProvider.ProviderName())
	assert.Equal(t, defaultTranslationLLMGeminiModel, geminiProvider.ModelName())

	temperature := 0.2
	maxOutputTokens := int32(2048)
	openAIProvider, err := newTranslationLLMTextProvider(&model.LLMTranslationProviderConfig{
		APIKey:           "test-key",
		Preset:           model.TranslationLLMProviderPresetOpenAICompatible,
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

	_, err = newTranslationLLMTextProvider(&model.LLMTranslationProviderConfig{})
	require.ErrorContains(t, err, "LLM API key is required")
}
