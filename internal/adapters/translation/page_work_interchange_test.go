package translationadapter

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type pageWorkInterchangeAuditAppender struct{}

func (pageWorkInterchangeAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return nil
}

func TestPageAndWorkInterchangePortsRejectRouteIdentityBeforeDomainCall(t *testing.T) {
	pagePort := NewPageInterchangePort(
		pageWorkInterchangeAuditAppender{}, sharedtelemetry.NewPageLocaleContentAuditRecord,
	)
	_, err := pagePort.LoadTranslationInterchangeTarget(
		context.Background(), nil, nil, "work", "page-a", "ko",
		&translation.ExtractionPlan{EntityType: "page", EntityID: "page-a", TargetLocale: "ko"},
	)
	require.ErrorContains(t, err, "Page translation interchange identity")

	workPort := NewWorkInterchangePort(
		pageWorkInterchangeAuditAppender{}, sharedtelemetry.NewWorkLocaleContentAuditRecord,
	)
	_, err = workPort.LoadTranslationInterchangeTarget(
		context.Background(), nil, nil, "work", "work-a", "ko",
		&translation.ExtractionPlan{EntityType: "work", EntityID: "other", TargetLocale: "ko"},
	)
	require.ErrorContains(t, err, "Work translation interchange identity")
}

func TestPageAndWorkInterchangePortsRejectApplyIdentityBeforeDomainCall(t *testing.T) {
	_, err := NewPageInterchangePort(
		pageWorkInterchangeAuditAppender{}, sharedtelemetry.NewPageLocaleContentAuditRecord,
	).ApplyTranslationInterchange(
		context.Background(), nil, nil,
		application.TranslationInterchangeApply{
			EntityType: "page", EntityID: "page-a", SourceLocale: "en", TargetLocale: "ko",
			Source: &translation.SourceDocument{},
			Plan: &translation.ExtractionPlan{
				EntityType: "page", EntityID: "other", SourceLocale: "en", TargetLocale: "ko",
			},
		},
	)
	require.ErrorContains(t, err, "Page translation interchange identity")

	_, err = NewWorkInterchangePort(
		pageWorkInterchangeAuditAppender{}, sharedtelemetry.NewWorkLocaleContentAuditRecord,
	).ApplyTranslationInterchange(
		context.Background(), nil, nil,
		application.TranslationInterchangeApply{
			EntityType: "page", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko",
			Source: &translation.SourceDocument{},
			Plan: &translation.ExtractionPlan{
				EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko",
			},
		},
	)
	require.ErrorContains(t, err, "Work translation interchange identity")
}
