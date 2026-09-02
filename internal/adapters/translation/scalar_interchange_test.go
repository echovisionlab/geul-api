package translationadapter

import (
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/stretchr/testify/require"
)

func TestScalarInterchangeRejectsMismatchedPlanBeforeDomainCall(t *testing.T) {
	t.Parallel()
	err := validateScalarInterchangeApply(application.TranslationInterchangeApply{
		EntityType: "menu", EntityID: "menu-1", SourceLocale: "ko", TargetLocale: "en",
		Source: &core.SourceDocument{},
		Plan: &core.ExtractionPlan{
			EntityType: "menu", EntityID: "other", SourceLocale: "ko", TargetLocale: "en",
		},
	}, "menu")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestScalarInterchangeMapsTargetCASConflictToAborted(t *testing.T) {
	t.Parallel()
	err := mapScalarInterchangeDomainError(&core.TargetRevisionConflict{
		CurrentRevision: "tr1_current", CurrentExists: true,
	})
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
}
