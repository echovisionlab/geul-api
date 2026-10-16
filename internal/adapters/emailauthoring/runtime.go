package emailauthoring

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
	emailauthoringdomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
)

// Runtime adapts shared email rendering and locale selection to Email
// Authoring-owned ports.
type Runtime struct{}

func NewRuntime() *Runtime { return &Runtime{} }

func (*Runtime) ResolveLocalizedTemplate(
	ctx context.Context,
	db *gorm.DB,
	template model.EmailTemplate,
	locale string,
) (model.EmailTemplate, error) {
	resolved, _, err := email.ResolveLocalizedEmailTemplate(ctx, db, template, locale)
	return resolved, err
}

func (*Runtime) ResolveLayoutHTML(ctx context.Context, db *gorm.DB, layoutID, locale string) (string, error) {
	layout, _, err := email.ResolveLocalizedEmailLayoutHTML(ctx, db, layoutID, locale)
	return layout, err
}

func (*Runtime) NormalizeSupportedLocale(value string) *string {
	return localization.NormalizeSupportedLocale(value)
}

func (*Runtime) BuildEmailRenderData(
	ctx context.Context,
	db *gorm.DB,
	cdnDomain string,
	siteOrigin string,
	locale string,
	input map[string]string,
) map[string]string {
	return emaildelivery.BuildEmailRenderData(ctx, db, cdnDomain, siteOrigin, locale, input)
}

var _ emailauthoringdomain.EmailTemplateRuntime = (*Runtime)(nil)
var _ emailauthoringdomain.EmailRenderDataBuilder = (*Runtime)(nil)
