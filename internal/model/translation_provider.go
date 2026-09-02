package model

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

type TranslationProviderType string

const (
	TranslationProviderTypeLLM   TranslationProviderType = "TRANSLATION_PROVIDER_TYPE_LLM"
	TranslationProviderTypeDeepL TranslationProviderType = "TRANSLATION_PROVIDER_TYPE_DEEPL"
)

type TranslationLLMProviderPreset string

const (
	TranslationLLMProviderPresetGemini           TranslationLLMProviderPreset = "TRANSLATION_LLM_PROVIDER_PRESET_GEMINI"
	TranslationLLMProviderPresetOpenAICompatible TranslationLLMProviderPreset = "TRANSLATION_LLM_PROVIDER_PRESET_OPENAI_COMPATIBLE"
)

type TranslationProviderConfigJSON struct {
	json.RawMessage
}

func (c *TranslationProviderConfigJSON) Scan(value structured.Value) error {
	if value == nil {
		c.RawMessage = json.RawMessage("{}")
		return nil
	}
	switch v := value.(type) {
	case []byte:
		c.RawMessage = v
	case string:
		c.RawMessage = json.RawMessage(v)
	}
	return nil
}

func (c TranslationProviderConfigJSON) Value() (driver.Value, error) {
	if c.RawMessage == nil {
		return "{}", nil
	}
	return []byte(c.RawMessage), nil
}

type LLMTranslationProviderConfig struct {
	APIKey                        string                       `json:"api_key"`
	Preset                        TranslationLLMProviderPreset `json:"preset"`
	APIBaseURL                    string                       `json:"api_base_url,omitempty"`
	Model                         string                       `json:"model"`
	InputTokenPriceUSDPerMillion  *float64                     `json:"input_token_price_usd_per_million,omitempty"`
	OutputTokenPriceUSDPerMillion *float64                     `json:"output_token_price_usd_per_million,omitempty"`
	MaxContextTokens              *int32                       `json:"max_context_tokens,omitempty"`
	MaxOutputTokens               *int32                       `json:"max_output_tokens,omitempty"`
	SupportsJSONMode              bool                         `json:"supports_json_mode"`
	Temperature                   *float64                     `json:"temperature,omitempty"`
}

type DeepLTranslationProviderConfig struct {
	APIKey     string `json:"api_key"`
	APIBaseURL string `json:"api_base_url,omitempty"`
}

type TranslationProviderConfig struct {
	ID        string                        `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string                        `gorm:"column:name;type:varchar(255);not null"`
	Type      TranslationProviderType       `gorm:"column:type;type:translation_provider_type;not null"`
	IsActive  bool                          `gorm:"column:is_active;not null;default:false"`
	Priority  int                           `gorm:"column:priority;not null;default:0"`
	Config    TranslationProviderConfigJSON `gorm:"column:config;type:jsonb;not null;default:'{}'"`
	CreatedAt time.Time                     `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time                     `gorm:"column:updated_at;not null;default:now()"`
}

func (TranslationProviderConfig) TableName() string {
	return "translation_provider_config"
}

func (p *TranslationProviderConfig) GetLLMConfig() (*LLMTranslationProviderConfig, error) {
	var cfg LLMTranslationProviderConfig
	if err := json.Unmarshal(p.Config.RawMessage, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (p *TranslationProviderConfig) GetDeepLConfig() (*DeepLTranslationProviderConfig, error) {
	var cfg DeepLTranslationProviderConfig
	if err := json.Unmarshal(p.Config.RawMessage, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (p *TranslationProviderConfig) SetConfig(cfg structured.Value) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	p.Config.RawMessage = data
	return nil
}
