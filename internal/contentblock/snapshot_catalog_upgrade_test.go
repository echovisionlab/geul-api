package contentblock

import (
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestVersionSnapshotCatalogUpgradeAllowsOnlyTheKnownAdditivePair(t *testing.T) {
	require.Equal(t, siteVariableCatalogFingerprint, contentv1.ContentBlockCatalogFingerprint)
	require.Equal(t, siteVariableCatalogFingerprint, UpgradeVersionSnapshotCatalogFingerprint(previousCollectionCatalogFingerprint))
	require.Equal(t, siteVariableCatalogFingerprint, UpgradeVersionSnapshotCatalogFingerprint(collectionSizeCatalogFingerprint))
	for _, fingerprint := range []string{"", "stale-catalog", "de190638c6014cc2ac80efce5f747b30d20cc024f3d0820c87ba6c1f1a56e16f", siteVariableCatalogFingerprint} {
		require.Equal(t, fingerprint, UpgradeVersionSnapshotCatalogFingerprint(fingerprint))
	}
}
