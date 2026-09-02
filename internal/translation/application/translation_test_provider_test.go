package application

// This provider is scoped to Translation tests. Metadata AI uses the fixtures
// in internal/ai.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/translation"
)

type testAITextProvider struct {
	delay time.Duration
}

type testAITextSession struct {
	provider *testAITextProvider
	spec     llm.SessionSpec
}

type translationAIRequestPayload struct {
	Profile translation.GenerationProfile `json:"profile"`
	Bundles []translationAIRequestBundle  `json:"bundles"`
}

type translationAIRequestBundle struct {
	BundleID string                     `json:"bundleId"`
	Units    []translationAIRequestUnit `json:"units"`
}

type translationAIRequestUnit struct {
	UnitID       string `json:"unitId"`
	SourceText   string `json:"sourceText"`
	SourceInline string `json:"sourceInline"`
}

type translationAIResponseEnvelope struct {
	Bundles []translationAIResponseBundle `json:"bundles"`
}

type translationAIResponseBundle struct {
	BundleID string                      `json:"bundle_id"`
	Units    []translationAIResponseUnit `json:"units"`
}

type translationAIResponseUnit struct {
	UnitID           string `json:"unit_id"`
	TranslatedInline string `json:"translated_inline"`
}

func newTestAITextProvider(testDelayMS int) *testAITextProvider {
	delay := max(time.Duration(testDelayMS)*time.Millisecond, 0)
	return &testAITextProvider{delay: delay}
}

func (p *testAITextProvider) ProviderName() string {
	return "test"
}

func (p *testAITextProvider) ModelName() string {
	return "test-provider"
}

func (p *testAITextProvider) StartSession(
	_ context.Context,
	spec llm.SessionSpec,
) (llm.Session, error) {
	return &testAITextSession{
		provider: p,
		spec:     spec,
	}, nil
}

func (p *testAITextProvider) GenerateText(
	ctx context.Context,
	req llm.GenerationRequest,
) (string, error) {
	if p.delay > 0 {
		timer := time.NewTimer(p.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
	}

	if req.Action == "translation-json" {
		return generateTestTranslationResponse(req.UserPrompt)
	}

	if strings.Contains(req.UserPrompt, "[ai:fail]") {
		return "", fmt.Errorf("test AI provider forced failure")
	}

	return "Test AI response", nil
}

func generateTestTranslationResponse(userPrompt string) (string, error) {
	var payload translationAIRequestPayload
	if err := json.Unmarshal([]byte(userPrompt), &payload); err != nil {
		return "", fmt.Errorf("test AI provider failed to parse translation payload: %w", err)
	}
	response := translationAIResponseEnvelope{Bundles: make([]translationAIResponseBundle, 0, len(payload.Bundles))}
	for _, bundle := range payload.Bundles {
		nextBundle := translationAIResponseBundle{BundleID: bundle.BundleID, Units: make([]translationAIResponseUnit, 0, len(bundle.Units))}
		for _, unit := range bundle.Units {
			if strings.Contains(unit.SourceText, "[ai:fail]") {
				return "", fmt.Errorf("test AI provider forced failure")
			}
			if strings.Contains(unit.SourceText, "[ai:invalid-json]") {
				return `{"bundles":`, nil
			}
			translatedInline, err := translateTestInline(unit.SourceInline, payload.Profile.TargetLocale, strings.Contains(unit.SourceText, "[ai:same]"))
			if err != nil {
				return "", err
			}
			nextBundle.Units = append(nextBundle.Units, translationAIResponseUnit{UnitID: unit.UnitID, TranslatedInline: translatedInline})
		}
		response.Bundles = append(response.Bundles, nextBundle)
	}
	body, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("test AI provider failed to encode translation response: %w", err)
	}
	return string(body), nil
}

func translateTestInline(sourceInline string, targetLocale string, unchanged bool) (string, error) {
	nodes, err := translation.UnmarshalXLIFFInlineFragment(sourceInline)
	if err != nil {
		return "", err
	}
	var translate func([]translation.XLIFFInline)
	translate = func(items []translation.XLIFFInline) {
		for index := range items {
			node := &items[index]
			if node.Kind == translation.XLIFFInlineText {
				sourceText := node.Text
				translated := cleanTranslationTestText(sourceText)
				if !unchanged && strings.TrimSpace(translated) != "" {
					translated = fmt.Sprintf("[%s] %s", targetLocale, translated)
				}
				node.Text = translation.PreserveSourceEdgeWhitespace(sourceText, translated)
			}
			translate(node.Children)
		}
	}
	translate(nodes)
	return translation.MarshalXLIFFInlineFragment(nodes)
}

func (s *testAITextSession) GenerateText(
	ctx context.Context,
	turn llm.SessionTurn,
) (string, error) {
	return s.provider.GenerateText(ctx, llm.GenerationRequest{
		RequestID:          s.spec.RequestID,
		Action:             s.spec.Action,
		SystemPrompt:       s.spec.SystemPrompt,
		UserPrompt:         turn.UserPrompt,
		ResponseJSONSchema: s.spec.ResponseJSONSchema,
	})
}

func (s *testAITextSession) Close(context.Context) error {
	return nil
}

func cleanTranslationTestText(text string) string {
	cleaned := text
	for _, marker := range []string{
		"[ai:fail]",
		"[ai:invalid-json]",
		"[ai:same]",
	} {
		cleaned = strings.ReplaceAll(cleaned, marker, "")
	}
	return strings.Join(strings.Fields(cleaned), " ")
}
