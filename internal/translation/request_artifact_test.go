package translation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestArtifactRoundTripsDeterministically(t *testing.T) {
	plan := requestArtifactPlan("u1", "u2")
	document, err := BuildXLIFFDocument(plan)
	require.NoError(t, err)
	document.File.ID = "source:stable"
	request := ProviderRequest{
		RequestID: "job-1", OperationID: "operation-1",
		Profile:  GenerationProfile{SourceLocale: "en", TargetLocale: "fr", ProtectedTerms: []string{"Geul"}},
		Document: *document,
	}
	require.NoError(t, ProtectXLIFFTerms(&request.Document, request.Profile.ProtectedTerms))

	first, err := BuildRequestArtifact(request, plan)
	require.NoError(t, err)
	second, err := BuildRequestArtifact(request, plan)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Len(t, first.Digest, 64)
	require.Contains(t, string(first.XLIFF), `version="2.2"`)
	require.Contains(t, string(first.XLIFF), XLIFFNamespace)

	restored, restoredPlan, err := ParseRequestArtifact(first)
	require.NoError(t, err)
	require.Empty(t, restored.RequestID)
	require.Empty(t, restored.OperationID)
	require.Equal(t, request.Profile, restored.Profile)
	require.Equal(t, plan, restoredPlan)
	require.Equal(t, "title", restored.Document.File.Groups[0].TranslationUnit[0].FieldName)
	require.Equal(t, ContainerTypeEntity, restored.Document.File.Groups[0].TranslationUnit[0].ContainerType)
}

func TestParseRequestArtifactRejectsManifestUnitDrift(t *testing.T) {
	plan := requestArtifactPlan("u1")
	document, err := BuildXLIFFDocument(plan)
	require.NoError(t, err)
	artifact, err := BuildRequestArtifact(ProviderRequest{
		RequestID: "job-1", OperationID: "operation-1",
		Profile: GenerationProfile{SourceLocale: "en", TargetLocale: "fr"}, Document: *document,
	}, plan)
	require.NoError(t, err)
	artifact.Manifest = []byte(`{"version":1,"profile":{},"plan":{"EntityType":"post","EntityID":"post-1","SourceLocale":"en","TargetLocale":"fr","Units":[],"Bundles":[]}}`)

	_, _, err = ParseRequestArtifact(artifact)
	require.ErrorContains(t, err, "manifest units do not match XLIFF")
}

func TestRequestArtifactDigestExcludesJobCorrelation(t *testing.T) {
	plan := requestArtifactPlan("u1")
	document, err := BuildXLIFFDocument(plan)
	require.NoError(t, err)
	document.File.ID = "source:stable"

	first, err := BuildRequestArtifact(ProviderRequest{
		RequestID: "job-1", OperationID: "operation-1",
		Profile: GenerationProfile{SourceLocale: "en", TargetLocale: "fr"}, Document: *document,
	}, plan)
	require.NoError(t, err)
	second, err := BuildRequestArtifact(ProviderRequest{
		RequestID: "job-2", OperationID: "operation-2",
		Profile: GenerationProfile{SourceLocale: "en", TargetLocale: "fr"}, Document: *document,
	}, plan)
	require.NoError(t, err)
	require.Equal(t, first.Digest, second.Digest)
	require.Equal(t, first.XLIFF, second.XLIFF)
	require.Equal(t, first.Manifest, second.Manifest)
}

func TestCanonicalizeRequestManifestRestoresJSONBRepresentation(t *testing.T) {
	plan := requestArtifactPlan("u1")
	document, err := BuildXLIFFDocument(plan)
	require.NoError(t, err)
	artifact, err := BuildRequestArtifact(ProviderRequest{
		RequestID: "job-1", OperationID: "operation-1",
		Profile: GenerationProfile{SourceLocale: "en", TargetLocale: "fr"}, Document: *document,
	}, plan)
	require.NoError(t, err)

	var jsonbValue map[string]any
	require.NoError(t, json.Unmarshal(artifact.Manifest, &jsonbValue))
	jsonbRepresentation, err := json.Marshal(jsonbValue)
	require.NoError(t, err)
	require.NotEqual(t, artifact.Manifest, jsonbRepresentation)

	canonical, err := CanonicalizeRequestManifest(jsonbRepresentation)
	require.NoError(t, err)
	require.Equal(t, artifact.Manifest, canonical)
	require.Equal(t, artifact.Digest, RequestArtifactDigest(artifact.XLIFF, canonical))
}

func TestBuildRequestArtifactRejectsBundleMetadataDrift(t *testing.T) {
	plan := requestArtifactPlan("u1")
	document, err := BuildXLIFFDocument(plan)
	require.NoError(t, err)
	plan.Bundles[0].Units[0].FieldName = "summary"

	_, err = BuildRequestArtifact(ProviderRequest{
		RequestID: "job-1", OperationID: "operation-1",
		Profile: GenerationProfile{SourceLocale: "en", TargetLocale: "fr"}, Document: *document,
	}, plan)
	require.ErrorContains(t, err, "metadata does not match the flat plan")
}

func requestArtifactPlan(unitIDs ...string) *ExtractionPlan {
	units := make([]Unit, 0, len(unitIDs))
	for index, unitID := range unitIDs {
		field := "body"
		if index == 0 {
			field = "title"
		}
		units = append(units, Unit{
			UnitID: unitID, EntityType: "post", EntityID: "post-1",
			Path: "entity:" + field, ContainerType: ContainerTypeEntity,
			ContainerID: "post-1", FieldName: field,
			SourceText: "request text " + unitID, SourceFormat: SourceFormatPlainText,
			SourceLocale: "en",
		})
	}
	return &ExtractionPlan{
		EntityType: "post", EntityID: "post-1", SourceLocale: "en", TargetLocale: "fr",
		Units: units,
		Bundles: []Bundle{{
			BundleID: "entity:main", EntityType: "post", EntityID: "post-1",
			SourceLocale: "en", TargetLocale: "fr", BundleType: BundleTypeEntity,
			SequenceTotal: 1, Units: append([]Unit(nil), units...),
		}},
	}
}
