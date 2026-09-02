package og

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestParseOgGenerationTargetEnforcesScopeAndCanonicalIdentity(t *testing.T) {
	tests := []struct {
		name    string
		target  *managev1.OgGenerationTarget
		wantErr bool
	}{
		{
			name: "post locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_POST,
				EntityId:   "11111111-1111-4111-8111-111111111111",
				Scope:      &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "ko"}},
			},
		},
		{
			name: "page locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_PAGE,
				EntityId:   "11111111-1111-4111-8111-111111111111",
				Scope:      &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "ko"}},
			},
		},
		{
			name: "form locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_FORM,
				EntityId:   "11111111-1111-4111-8111-111111111111",
				Scope:      &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "ko"}},
			},
		},
		{
			name: "series locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_SERIES,
				EntityId:   "11111111-1111-4111-8111-111111111111",
				Scope:      &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "ko"}},
			},
		},
		{
			name: "privacy locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY,
				EntityId:   PrivacyRouteEntityID,
				Scope:      &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "ko"}},
			},
		},
		{
			name: "terms locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_TERMS,
				EntityId:   TermsRouteEntityID,
				Scope:      &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "ko"}},
			},
		},
		{
			name: "work locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_WORK,
				EntityId:   "11111111-1111-4111-8111-111111111111",
				Scope:      &managev1.OgGenerationTarget_Locale{Locale: &managev1.OgLocaleTarget{Locale: "ko"}},
			},
		},
		{
			name: "site entity",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_SITE,
				EntityId:   SiteEntityID,
				Scope:      &managev1.OgGenerationTarget_Entity{Entity: &managev1.OgEntityTarget{}},
			},
		},
		{
			name: "site rejects noncanonical id",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_SITE,
				EntityId:   "other",
				Scope:      &managev1.OgGenerationTarget_Entity{Entity: &managev1.OgEntityTarget{}},
			},
			wantErr: true,
		},
		{
			name: "translated rejects entity scope",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_POST,
				EntityId:   "11111111-1111-4111-8111-111111111111",
				Scope:      &managev1.OgGenerationTarget_Entity{Entity: &managev1.OgEntityTarget{}},
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, _, err := parseOgGenerationTarget(test.target)
			require.Equal(t, test.wantErr, err != nil)
		})
	}
}

func TestOgGenerationQueryKeepsReadyHistoryAfterAssetRetentionCleanup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE public_asset (
		id text PRIMARY KEY, kind text NOT NULL, object_key text NOT NULL,
		extension text NOT NULL, mime_type text NOT NULL, disposition text NOT NULL,
		status text NOT NULL, created_at datetime NOT NULL, updated_at datetime NOT NULL
	)`).Error)
	now := time.Date(2026, time.July, 16, 2, 0, 0, 0, time.UTC)
	generationID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO public_asset (
		id, kind, object_key, extension, mime_type, disposition, status, created_at, updated_at
	) VALUES (?, 'og', ?, 'webp', 'image/webp', 'inline', ?, ?, ?)`,
		generationID, "og/"+generationID+".webp", model.PublicAssetStatusDeleted, now, now,
	).Error)
	generation := &model.OgGeneration{
		ID: generationID, RunID: uuid.NewString(), Status: model.OgGenerationStatusReady,
		ReadyAt: &now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	target := &model.OgGenerationTarget{
		ID: uuid.NewString(), EntityType: "work", EntityID: uuid.NewString(),
		TargetKind: "locale", Locale: new("en"), CreatedAt: now, UpdatedAt: now,
	}

	view, err := (&AdminService{db: db, cdnDomain: "https://cdn.example.com"}).ogGenerationToProto(
		t.Context(), generation, target,
	)
	require.NoError(t, err)
	assert.Equal(t, managev1.OgGenerationStatus_OG_GENERATION_STATUS_READY, view.Status)
	assert.Nil(t, view.Asset)
}
