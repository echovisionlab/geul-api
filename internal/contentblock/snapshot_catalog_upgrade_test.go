package contentblock

import (
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestVersionSnapshotCatalogUpgradeAllowsOnlyTheKnownAdditivePair(t *testing.T) {
	require.Equal(t, collectionSizeCatalogFingerprint, contentv1.ContentBlockCatalogFingerprint)
	require.Equal(t, collectionSizeCatalogFingerprint, UpgradeVersionSnapshotCatalogFingerprint(previousCollectionCatalogFingerprint))
	for _, fingerprint := range []string{"", "stale-catalog", "de190638c6014cc2ac80efce5f747b30d20cc024f3d0820c87ba6c1f1a56e16f", collectionSizeCatalogFingerprint} {
		require.Equal(t, fingerprint, UpgradeVersionSnapshotCatalogFingerprint(fingerprint))
	}
}
