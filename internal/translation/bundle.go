package translation

import (
	"fmt"
	"strings"
)

const (
	// MaxUnitsPerProviderBundle limits one provider request context so a large
	// structured document cannot exceed the stable Translation contract.
	MaxUnitsPerProviderBundle = 48
	// MaxSourceBytesPerProviderBundle limits source text bytes in one provider
	// request context. A single indivisible unit may exceed this limit.
	MaxSourceBytesPerProviderBundle = 12000
)

// BuildBundles partitions units by semantic container and provider limits.
// Unit order and bundle sequence are preserved.
func BuildBundles(
	entityType string,
	entityID string,
	sourceLocale string,
	targetLocale string,
	units []Unit,
	contextText *string,
) []Bundle {
	if len(units) == 0 {
		return nil
	}

	bundles := make([]Bundle, 0)
	currentUnits := make([]Unit, 0)
	currentGroupKey := ""
	currentBundleType := BundleTypeEntity
	currentSourceBytes := 0
	currentGroupChunkIndex := 0

	flush := func() {
		if len(currentUnits) == 0 {
			return
		}
		sequenceIndex := len(bundles)
		bundleID := bundleID(currentGroupKey, currentGroupChunkIndex)
		copiedUnits := append([]Unit(nil), currentUnits...)
		bundles = append(bundles, Bundle{
			BundleID:      bundleID,
			EntityType:    entityType,
			EntityID:      entityID,
			SourceLocale:  sourceLocale,
			TargetLocale:  targetLocale,
			BundleType:    currentBundleType,
			ContextText:   contextText,
			SequenceIndex: sequenceIndex,
			Units:         copiedUnits,
		})
		currentUnits = currentUnits[:0]
		currentSourceBytes = 0
		currentGroupChunkIndex++
	}

	for _, unit := range units {
		groupKey, bundleType := bundleGroup(unit)
		if currentGroupKey != "" && groupKey != currentGroupKey {
			flush()
			currentGroupChunkIndex = 0
			currentGroupKey = ""
			currentBundleType = BundleTypeEntity
		}
		if currentGroupKey == "" {
			currentGroupKey = groupKey
			currentBundleType = bundleType
		}
		unitSourceBytes := len(unit.SourceText)
		if len(currentUnits) > 0 &&
			(len(currentUnits) >= MaxUnitsPerProviderBundle ||
				currentSourceBytes+unitSourceBytes > MaxSourceBytesPerProviderBundle) {
			flush()
		}
		currentUnits = append(currentUnits, unit)
		currentSourceBytes += unitSourceBytes
	}
	flush()

	sequenceTotal := len(bundles)
	for index := range bundles {
		bundles[index].SequenceIndex = index
		bundles[index].SequenceTotal = sequenceTotal
	}
	return bundles
}

func bundleID(groupKey string, chunkIndex int) string {
	id := strings.TrimSpace(groupKey)
	if id == "" {
		id = "entity:meta"
	}
	if chunkIndex > 0 {
		return fmt.Sprintf("%s:chunk:%d", id, chunkIndex+1)
	}
	return id
}

func bundleGroup(unit Unit) (string, string) {
	switch unit.ContainerType {
	case ContainerTypeBlock:
		return "block:" + unit.ContainerID, BundleTypeBlock
	case ContainerTypeSection:
		return "section:" + unit.ContainerID, BundleTypeSection
	case ContainerTypeHTMLNode:
		return "html:main", BundleTypeHTML
	default:
		return "entity:meta", BundleTypeEntity
	}
}
