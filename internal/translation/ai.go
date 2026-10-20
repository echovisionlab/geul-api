package translation

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/llm"
)

const translationAIProviderTimeout = 90 * time.Second
const translationAISessionCloseTimeout = 5 * time.Second

type aiTranslationGenerator struct {
	provider llm.Provider
}

type aiTranslationGeneratorSession struct {
	session llm.Session
}

func NewAIGenerator(provider llm.Provider) Generator {
	dependencycheck.New("AITranslationGenerator").
		RequireNotNil(provider, "provider").
		Validate()

	return &aiTranslationGenerator{provider: provider}
}

func (g *aiTranslationGenerator) ProviderName() string {
	return g.provider.ProviderName()
}

func (g *aiTranslationGenerator) ModelName() string {
	return g.provider.ModelName()
}

func (g *aiTranslationGenerator) Translate(
	ctx context.Context,
	req ProviderRequest,
) (*ProviderResponse, error) {
	session, err := g.StartSession(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), translationAISessionCloseTimeout)
		defer cancel()
		if logErr := session.Close(closeCtx); logErr != nil {
			LogProviderEvent(ctx, slog.LevelError, "Operation failed", "domain", "translation", "event", "translation.provider.close_failed", "outcome", "failed", failurecause.Attr(logErr), "request_id", req.RequestID)
		}
	}()

	return session.Translate(ctx, req)
}

func (g *aiTranslationGenerator) StartSession(
	ctx context.Context,
	req ProviderRequest,
) (GeneratorSession, error) {
	if err := ValidateProviderRequest(req); err != nil {
		return nil, err
	}

	session, err := g.provider.StartSession(ctx, llm.SessionSpec{
		RequestID:          req.RequestID,
		Action:             "translation-json",
		SystemPrompt:       translationAISystemPrompt(req),
		ResponseJSONSchema: translationResponseJSONSchema,
		ResponseSchemaName: "geul_translation_response",
		Timeout:            translationAIProviderTimeout,
	})
	if err != nil {
		return nil, err
	}

	return &aiTranslationGeneratorSession{session: session}, nil
}

func (s *aiTranslationGeneratorSession) Translate(
	ctx context.Context,
	req ProviderRequest,
) (*ProviderResponse, error) {
	userPrompt, err := buildTranslationAIUserPrompt(req)
	if err != nil {
		return nil, err
	}

	text, err := s.session.GenerateText(ctx, llm.SessionTurn{
		OperationID: req.OperationID,
		TurnKind:    "initial",
		UserPrompt:  userPrompt,
	})
	if err != nil {
		return nil, err
	}
	raw, err := NewProviderRawResponse("application/json", []byte(text))
	if err != nil {
		return nil, err
	}
	response, parseErr := parseTranslationProviderResponse(req, text)
	if parseErr != nil {
		LogProviderResponseFailure(ctx, req, parseErr)
	}
	if response == nil {
		response = &ProviderResponse{}
	}
	response.Raw = raw
	return response, parseErr
}

func (s *aiTranslationGeneratorSession) Close(ctx context.Context) error {
	return s.session.Close(ctx)
}
