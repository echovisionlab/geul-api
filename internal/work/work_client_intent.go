package work

import (
	"reflect"
	"slices"
)

func mergeWorkClientIntent(current, observed, desired []string) []string {
	observedSet := make(map[string]struct{}, len(observed))
	desiredSet := make(map[string]struct{}, len(desired))
	for _, clientID := range observed {
		observedSet[clientID] = struct{}{}
	}
	for _, clientID := range desired {
		desiredSet[clientID] = struct{}{}
	}

	merged := make([]string, 0, len(current)+len(desired))
	mergedSet := make(map[string]struct{}, len(current)+len(desired))
	for _, clientID := range current {
		_, wasObserved := observedSet[clientID]
		_, isDesired := desiredSet[clientID]
		if wasObserved && !isDesired {
			continue
		}
		merged = append(merged, clientID)
		mergedSet[clientID] = struct{}{}
	}
	orderChanged := workClientOrderChanged(observed, desired)
	additionPosition := 0
	for desiredIndex, clientID := range desired {
		_, wasObserved := observedSet[clientID]
		if wasObserved {
			continue
		}
		if _, alreadyPresent := mergedSet[clientID]; alreadyPresent {
			continue
		}
		position := len(merged)
		if !orderChanged {
			// Place local additions before the next surviving baseline member,
			// without undoing a peer's reorder of the existing members.
			for _, nextID := range desired[desiredIndex+1:] {
				if _, wasObserved := observedSet[nextID]; !wasObserved {
					continue
				}
				if index := slices.Index(merged, nextID); index >= 0 {
					position = index
					break
				}
			}
			// A peer may have reversed the anchors; local additions still keep
			// their requested order relative to one another.
			position = max(position, additionPosition)
		}
		merged = slices.Insert(merged, position, clientID)
		additionPosition = position + 1
		mergedSet[clientID] = struct{}{}
	}
	if !orderChanged {
		return merged
	}

	// A deliberate local reorder controls baseline members that still exist, while preserving peer-added members (and their relative order) after that sequence.
	reordered := make([]string, 0, len(merged))
	seen := make(map[string]struct{}, len(merged))
	for _, clientID := range desired {
		if _, exists := mergedSet[clientID]; !exists {
			continue
		}
		if _, alreadyAdded := seen[clientID]; alreadyAdded {
			continue
		}
		reordered = append(reordered, clientID)
		seen[clientID] = struct{}{}
	}
	for _, clientID := range merged {
		if _, alreadyAdded := seen[clientID]; alreadyAdded {
			continue
		}
		reordered = append(reordered, clientID)
		seen[clientID] = struct{}{}
	}
	return reordered
}

func workClientOrderChanged(observed, desired []string) bool {
	observedSet := make(map[string]struct{}, len(observed))
	desiredSet := make(map[string]struct{}, len(desired))
	for _, clientID := range observed {
		observedSet[clientID] = struct{}{}
	}
	for _, clientID := range desired {
		desiredSet[clientID] = struct{}{}
	}
	observedCommon := make([]string, 0, len(observed))
	desiredCommon := make([]string, 0, len(desired))
	for _, clientID := range observed {
		if _, exists := desiredSet[clientID]; exists {
			observedCommon = append(observedCommon, clientID)
		}
	}
	for _, clientID := range desired {
		if _, exists := observedSet[clientID]; exists {
			desiredCommon = append(desiredCommon, clientID)
		}
	}
	return !reflect.DeepEqual(observedCommon, desiredCommon)
}
