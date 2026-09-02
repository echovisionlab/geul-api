//go:build integration

package application

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestTranslationSettingsAuditOmitsProtectedTermValuesIntegration(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	require.NoError(t, testutil.ResetBackendIntegrationState(t.Context(), stack))

	admin := seedTranslationProviderAuditAdmin(t, stack.Postgres.DB)
	grantTranslationProviderIntegrationAdmin(t, stack.SpiceDBClient, admin)
	ctx := translationProviderAuditedMemberContext(t, admin)
	service := newAuditedTranslationProviderService(
		stack.Postgres.DB, apitelemetry.NewDurableWriter(stack.Postgres.DB), stack.SpiceDBClient,
	)
	_, err = service.UpdateTranslationSettings(ctx, connect.NewRequest(
		&managev1.UpdateTranslationSettingsRequest{Settings: &managev1.TranslationSettings{
			DefaultLocale: "en", ProtectedTerms: []string{" Photoshop ", "Photoshop", "React Native"},
		}},
	))
	require.NoError(t, err)

	var row struct {
		Action     string
		TargetType string `gorm:"column:target_type"`
		Attributes []byte
	}
	require.NoError(t, stack.Postgres.DB.Table("public.domain_audit").
		Select("action, target_type, attributes").
		Where("target_type = ?", "translation_settings").Take(&row).Error)
	require.Equal(t, string(sharedtelemetry.AuditTranslationSettingsUpdated), row.Action)
	var attributes struct {
		ChangedFields []string `json:"changed_fields"`
	}
	require.NoError(t, json.Unmarshal(row.Attributes, &attributes))
	require.Equal(t, []string{"protected_terms"}, attributes.ChangedFields)
	require.NotContains(t, string(row.Attributes), "Photoshop")
	require.NotContains(t, string(row.Attributes), "React Native")
}
