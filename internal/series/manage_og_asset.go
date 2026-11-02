package series

import "strings"

func manageOgAssetFromReadyMap(ready map[string]*Asset, candidateIDs ...*string) *Asset {
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
