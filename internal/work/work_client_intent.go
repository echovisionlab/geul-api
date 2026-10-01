package work

import "reflect"

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
	for _, clientID := range desired {
		_, wasObserved := observedSet[clientID]
		if wasObserved {
			continue
		}
		if _, alreadyPresent := mergedSet[clientID]; alreadyPresent {
			continue
		}
		merged = append(merged, clientID)
		mergedSet[clientID] = struct{}{}
	}
	if !workClientOrderChanged(observed, desired) {
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
