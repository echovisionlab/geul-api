package og

import (
	"testing"

	"github.com/stretchr/testify/require"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestParseOgGenerationTargetEnforcesScopeAndCanonicalIdentity(t *testing.T) {
	tests := []struct {
		name    string
		target  *managev1.OgGenerationTarget
		wantErr bool
	}{
		{
			name: "label entity",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_LABEL,
				EntityId:   "11111111-1111-4111-8111-111111111111",
				Scope:      &managev1.OgGenerationTarget_Entity{Entity: &managev1.OgEntityTarget{}},
			},
		},
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
			name: "artist locale",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST,
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
			name: "release entity",
			target: &managev1.OgGenerationTarget{
				EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE,
				EntityId:   "11111111-1111-4111-8111-111111111111",
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
			_, err := parseTargetQuery(targetQueryFromProto(test.target))
			require.Equal(t, test.wantErr, err != nil)
		})
	}
}
