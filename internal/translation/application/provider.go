package application

import (
	"encoding/json"
	"time"
)

// ProviderType is a stored translation provider type.
type ProviderType string

const (
	ProviderTypeLLM   ProviderType = "TRANSLATION_PROVIDER_TYPE_LLM"
	ProviderTypeDeepL ProviderType = "TRANSLATION_PROVIDER_TYPE_DEEPL"
)

// LLMPreset is a stored LLM provider preset.
type LLMPreset string

const (
	LLMPresetGemini           LLMPreset = "TRANSLATION_LLM_PROVIDER_PRESET_GEMINI"
	LLMPresetOpenAICompatible LLMPreset = "TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE"
)

// LLMConfig is the stored config of an LLM provider. Its JSON is the stored
// config document.
type LLMConfig struct {
	APIKey                        string    `json:"api_key"`
	Preset                        LLMPreset `json:"preset"`
	APIBaseURL                    string    `json:"api_base_url,omitempty"`
	Model                         string    `json:"model"`
	InputTokenPriceUSDPerMillion  *float64  `json:"input_token_price_usd_per_million,omitempty"`
	OutputTokenPriceUSDPerMillion *float64  `json:"output_token_price_usd_per_million,omitempty"`
	MaxContextTokens              *int32    `json:"max_context_tokens,omitempty"`
	MaxOutputTokens               *int32    `json:"max_output_tokens,omitempty"`
	SupportsJSONMode              bool      `json:"supports_json_mode"`
	Temperature                   *float64  `json:"temperature,omitempty"`
}

// DeepLConfig is the stored config of a DeepL provider.
type DeepLConfig struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url,omitempty"`
}

// Provider is a configured translation provider. Config keeps the stored
// JSON document, which an administrator read may find undecodable.
type Provider struct {
	ID        string
	Name      string
	Type      ProviderType
	IsActive  bool
	Priority  int
	Config    json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LLMConfig decodes Config as an LLM provider config.
func (p *Provider) LLMConfig() (*LLMConfig, error) {
	var cfg LLMConfig
	if err := json.Unmarshal(p.Config, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// DeepLConfig decodes Config as a DeepL provider config.
func (p *Provider) DeepLConfig() (*DeepLConfig, error) {
	var cfg DeepLConfig
	if err := json.Unmarshal(p.Config, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetConfig encodes cfg as the stored config document.
func (p *Provider) SetConfig(cfg any) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	p.Config = data
	return nil
}

// ProviderChange is an update of a provider's stored fields; a nil field is
// unchanged.
type ProviderChange struct {
	Name     *string
	IsActive *bool
	Priority *int
	Type     *ProviderType
	Config   json.RawMessage
}
