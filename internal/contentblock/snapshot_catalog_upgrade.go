package contentblock

import contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

const previousCollectionCatalogFingerprint = "de190638c6014cc2ac80efce5f747b30d20cc024f3d0820c87ba6c1f1a56e16e"
const collectionSizeCatalogFingerprint = "2db3b07d50a139a670f68e3cd045063d20475a102acbee7852066b1929b7172b"

// UpgradeVersionSnapshotCatalogFingerprint upgrades only the released catalog
// preceding the additive collection-size fields. Snapshot decoders call this
// before ordinary validation; live mutations retain strict fingerprint checks.
// Pin both ends so a later incompatible catalog cannot inherit this exception.
func UpgradeVersionSnapshotCatalogFingerprint(fingerprint string) string {
	if contentv1.ContentBlockCatalogFingerprint == collectionSizeCatalogFingerprint && fingerprint == previousCollectionCatalogFingerprint {
		return collectionSizeCatalogFingerprint
	}
	return fingerprint
}
