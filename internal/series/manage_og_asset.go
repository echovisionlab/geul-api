package series

import (
	"strings"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func manageOgAssetFromReadyMap(
	ready map[string]*commonv1.AssetRef,
	candidateIDs ...*string,
) *commonv1.AssetRef {
	for _, candidateID := range candidateIDs {
		if candidateID == nil {
			continue
		}
		assetID := strings.TrimSpace(*candidateID)
		if assetID != "" && ready[assetID] != nil {
			return ready[assetID]
		}
	}
	return nil
}
