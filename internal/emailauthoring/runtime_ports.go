package emailauthoring

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type EmailPublisher interface {
	PublishSendEmail(context.Context, *managev1.SendEmailEvent) error
}

// EmailTemplateRuntime is the narrow rendering and locale capability used by
// Email Template preview and test-send flows.
type EmailTemplateRuntime interface {
	ResolveLocalizedTemplate(context.Context, *gorm.DB, model.EmailTemplate, string) (model.EmailTemplate, error)
	RenderVariables(string, map[string]string) string
	WrapWithLayout(context.Context, *gorm.DB, string, string, string, map[string]string) (string, error)
	NormalizeRenderedHTML(string) string
	NormalizeSupportedLocale(string) *string
}
