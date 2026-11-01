package og_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestOgPolicyHelpersClassifyLocaleAwareEntities(t *testing.T) {
	for _, entityType := range []string{"post", "page", "form", "series", "work", "artist"} {
		policy, ok := og.PolicyForEntityName(entityType)
		require.True(t, ok)
		assert.Equal(t, og.LocaleStrategyTranslated, policy.LocaleStrategy)
		assert.True(t, og.SupportsLocaleAware(entityType))
	}
	privacy, ok := og.PolicyForEntityName("privacy")
	require.True(t, ok)
	assert.Equal(t, og.LocaleStrategyStatic, privacy.LocaleStrategy)
	assert.Equal(t, managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY, og.EntityTypeForName("privacy"))
	assert.Equal(t, managev1.OgEntityType_OG_ENTITY_TYPE_UNSPECIFIED, og.EntityTypeForName("unknown"))
}

func TestOgGenerationPolicyCoversEverySupportedEntity(t *testing.T) {
	want := map[managev1.OgEntityType]og.LocaleStrategy{
		managev1.OgEntityType_OG_ENTITY_TYPE_POST:    og.LocaleStrategyTranslated,
		managev1.OgEntityType_OG_ENTITY_TYPE_PAGE:    og.LocaleStrategyTranslated,
		managev1.OgEntityType_OG_ENTITY_TYPE_WORK:    og.LocaleStrategyTranslated,
		managev1.OgEntityType_OG_ENTITY_TYPE_LABEL:   og.LocaleStrategyBaseOnly,
		managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST:  og.LocaleStrategyTranslated,
		managev1.OgEntityType_OG_ENTITY_TYPE_SITE:    og.LocaleStrategyBaseOnly,
		managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE: og.LocaleStrategyBaseOnly,
		managev1.OgEntityType_OG_ENTITY_TYPE_SERIES:  og.LocaleStrategyTranslated,
		managev1.OgEntityType_OG_ENTITY_TYPE_FORM:    og.LocaleStrategyTranslated,
		managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY: og.LocaleStrategyStatic,
		managev1.OgEntityType_OG_ENTITY_TYPE_TERMS:   og.LocaleStrategyStatic,
	}
	for entityType, strategy := range want {
		policy, ok := og.PolicyForEntityType(entityType)
		require.True(t, ok, entityType.String())
		assert.Equal(t, strategy, policy.LocaleStrategy, entityType.String())
		assert.NotEmpty(t, policy.Name, entityType.String())
	}
}
