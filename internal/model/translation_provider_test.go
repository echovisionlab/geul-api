package model

import (
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslationProviderConfigJSONScanAndValue(t *testing.T) {
	t.Parallel()

	var cfg TranslationProviderConfigJSON
	require.NoError(t, cfg.Scan(nil))
	assert.JSONEq(t, `{}`, string(cfg.RawMessage))

	require.NoError(t, cfg.Scan([]byte(`{"api_key":"byte-key"}`)))
	assert.JSONEq(t, `{"api_key":"byte-key"}`, string(cfg.RawMessage))

	require.NoError(t, cfg.Scan(`{"api_key":"string-key"}`))
	assert.JSONEq(t, `{"api_key":"string-key"}`, string(cfg.RawMessage))

	value, err := cfg.Value()
	require.NoError(t, err)
	assert.Equal(t, driver.Value([]byte(`{"api_key":"string-key"}`)), value)

	emptyValue, err := (TranslationProviderConfigJSON{}).Value()
	require.NoError(t, err)
	assert.Equal(t, driver.Value("{}"), emptyValue)
}

func TestTranslationProviderConfigAccessors(t *testing.T) {
	t.Parallel()

	provider := &TranslationProviderConfig{}
	require.NoError(t, provider.SetConfig(&LLMTranslationProviderConfig{
		APIKey: "llm-key",
		Preset: TranslationLLMProviderPresetGemini,
		Model:  "gemini-2.5-flash-lite",
	}))

	llm, err := provider.GetLLMConfig()
	require.NoError(t, err)
	assert.Equal(t, "llm-key", llm.APIKey)
	assert.Equal(t, TranslationLLMProviderPresetGemini, llm.Preset)

	require.NoError(t, provider.SetConfig(&DeepLTranslationProviderConfig{
		APIKey:     "deepl-key",
		APIBaseURL: "https://api.deepl.com",
	}))
	deepl, err := provider.GetDeepLConfig()
	require.NoError(t, err)
	assert.Equal(t, "deepl-key", deepl.APIKey)
	assert.Equal(t, "https://api.deepl.com", deepl.APIBaseURL)
}

func TestTranslationProviderTableName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "translation_provider_config", (TranslationProviderConfig{}).TableName())
}
