//go:build integration

package application

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestTranslationProtectedTermsPersistCanonicallyAndBindOnlyCurrentSourceOccurrences(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	require.NoError(t, testutil.ResetBackendIntegrationState(t.Context(), stack))

	admin := seedTranslationProviderAuditAdmin(t, stack.Postgres.DB)
	grantTranslationProviderIntegrationAdmin(t, stack.SpiceDBClient, admin)
	ctx := translationProviderAuditedMemberContext(t, admin)
	service := &TranslationService{db: stack.Postgres.DB, spiceDB: stack.SpiceDBClient}

	response, err := service.UpdateTranslationSettings(ctx, connect.NewRequest(
		&managev1.UpdateTranslationSettingsRequest{Settings: &managev1.TranslationSettings{
			DefaultLocale: "en", ProtectedTerms: []string{" Photoshop ", "Photoshop", "react native", "React Native", " "},
		}},
	))
	require.NoError(t, err)
	updated := response.Msg.Settings
	require.Equal(t, []string{"Photoshop", "react native", "React Native"}, updated.ProtectedTerms)
	var stored model.TranslationSettings
	require.NoError(t, stack.Postgres.DB.First(&stored, "id = 1").Error)
	require.Equal(t, []string{"Photoshop", "react native", "React Native"}, []string(stored.ProtectedTerms))

	request := translation.ProviderRequest{
		RequestID: "job", OperationID: "job", Profile: translation.GenerationProfile{
			SourceLocale: "en", TargetLocale: "ko", ProtectedTerms: []string{"React Native"},
		},
		Document: translation.XLIFFDocument{Version: translation.XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: translation.XLIFFFile{
			ID: "post:1", Groups: []translation.XLIFFGroup{{ID: "body", TranslationUnit: []translation.XLIFFUnit{{ID: "u1", Source: "Photoshop and React Native"}}}},
		}},
	}
	request, err = loadTranslationGenerationResources(ctx, stack.Postgres.DB, &model.TranslationJob{EntityType: "post"}, request)
	require.NoError(t, err)
	require.Equal(t, []string{"React Native", "Photoshop"}, request.Profile.ProtectedTerms)
	require.Len(t, request.Document.File.Groups[0].TranslationUnit[0].OriginalData, 2)

	canonicalNoopResponse, err := service.UpdateTranslationSettings(ctx, connect.NewRequest(
		&managev1.UpdateTranslationSettingsRequest{Settings: &managev1.TranslationSettings{
			DefaultLocale: "en", ProtectedTerms: []string{"Photoshop", "react native", "React Native"},
		}},
	))
	require.NoError(t, err)
	canonicalNoop := canonicalNoopResponse.Msg.Settings
	require.NotNil(t, updated.UpdatedAt)
	require.NotNil(t, canonicalNoop.UpdatedAt)
	require.True(t, updated.UpdatedAt.AsTime().Equal(canonicalNoop.UpdatedAt.AsTime()))
}
