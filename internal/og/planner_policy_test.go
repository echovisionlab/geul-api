package og_test

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestInternalServiceCompleteUsesCanonicalCDNAssetURL(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	now := time.Now().UTC()
	leaseToken := uuid.NewString()
	locale := "ko"
	generation := seedProcessingOgGeneration(
		t, db, managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY, og.PrivacyRouteEntityID, &locale,
		leaseToken, now.Add(time.Minute), now.Add(time.Minute),
	)

	response, err := og.NewInternalService(db, "https://cdn.example.com/", migratedProjection{}).CompleteOgGeneration(
		t.Context(),
		connect.NewRequest(&intrav1.CompleteOgGenerationRequest{
			GenerationId: generation.ID,
			LeaseToken:   leaseToken,
			Written:      validOgWriteResult(generation.ID),
		}),
	)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/asset/"+generation.ID+"/og.webp", response.Msg.GetAsset().GetUrl())

	var stored model.OgGeneration
	require.NoError(t, db.First(&stored, "id = ?", generation.ID).Error)
	require.Equal(t, model.OgGenerationStatusReady, stored.Status)
}

func TestOgPlannerAllocatesWorkLocaleOutput(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	sourceFileID := uuid.NewString()
	require.NoError(t, db.Create(&model.File{
		ID: sourceFileID, FileName: "featured.webp", MimeType: "image/webp",
		FileSize: 64, Extension: "webp", SHA256: make([]byte, 32),
	}).Error)
	lifecycle := mediaasset.NewLifecycle(db, "https://cdn.example.com")
	featured, _, err := lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &sourceFileID,
		Kind:         "image",
		Extension:    "webp",
		MimeType:     "image/webp",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte("featured image"))
	_, err = lifecycle.CompletePublicAsset(t.Context(), &commonv1.AssetWriteResult{
		AssetId: featured.ID, FileSize: 64, Sha256: digest[:],
	})
	require.NoError(t, err)
	locale := "en"

	_, err = requestOgGenerationForTest(t.Context(), newOGPlannerForTest(db, "https://cdn.example.com"), "automatic", "work_updated", []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_WORK, "work-1", "Inspire Resort: Le Space", &locale, &sourceFileID),
	})
	require.NoError(t, err)
	var generations []model.OgGeneration
	require.NoError(t, db.Order("request_sequence").Find(&generations).Error)
	require.Len(t, generations, 1)
	var snapshot ogEntitySnapshotForTest
	require.NoError(t, json.Unmarshal(generations[0].EntitySnapshot, &snapshot))
	assert.Equal(t, "work-1", snapshot.EntityID)
	assert.Equal(t, "Inspire Resort: Le Space", snapshot.Title)
	assert.Equal(t, "en", ptrStringValue(snapshot.Locale))
	require.NotNil(t, snapshot.FeaturedImage)
	assert.Equal(t, featured.ID, snapshot.FeaturedImage.AssetID)
	assert.Equal(t, generations[0].ID, snapshot.Output.AssetID)
	assert.Equal(t, "asset/"+generations[0].ID+".webp", snapshot.Output.ObjectKey)
}

func TestOgPlannerAllocatesOneOutputPerLocale(t *testing.T) {
	db := newServiceUnitDB(t)
	setupOgLifecycleUnitTables(t, db)
	ko, fr := "ko", "fr"
	_, err := requestOgGenerationForTest(t.Context(), newOGPlannerForTest(db, "https://cdn.example.com"), "automatic", "post_updated", []og.Request{
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_POST, "post-1", "한국어 제목", &ko, nil),
		ogTestRequest(managev1.OgEntityType_OG_ENTITY_TYPE_POST, "post-1", "Titre FR", &fr, nil),
	})
	require.NoError(t, err)
	var generations []model.OgGeneration
	require.NoError(t, db.Order("request_sequence").Find(&generations).Error)
	require.Len(t, generations, 2)
	var first, second ogEntitySnapshotForTest
	require.NoError(t, json.Unmarshal(generations[0].EntitySnapshot, &first))
	require.NoError(t, json.Unmarshal(generations[1].EntitySnapshot, &second))
	assert.Equal(t, "ko", ptrStringValue(first.Locale))
	assert.Equal(t, "한국어 제목", first.Title)
	assert.Equal(t, "fr", ptrStringValue(second.Locale))
	assert.Equal(t, "Titre FR", second.Title)
	assert.NotEqual(t, first.Output.AssetID, second.Output.AssetID)
}

func TestOgPolicyHelpersClassifyLocaleAwareEntities(t *testing.T) {
	for _, entityType := range []string{"post", "page", "form", "series", "work"} {
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
		managev1.OgEntityType_OG_ENTITY_TYPE_SITE:    og.LocaleStrategyBaseOnly,
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
