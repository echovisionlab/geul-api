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
			// A peer may have reversed the anchors; local additions still keep
			// their requested order relative to one another.
			position = max(workClientAnchor(merged, desired[desiredIndex+1:], observedSet), additionPosition)
		}
		merged = slices.Insert(merged, position, clientID)
		additionPosition = position + 1
		mergedSet[clientID] = struct{}{}
	}
	if !orderChanged {
		return merged
	}
	return reorderWorkClients(merged, mergedSet, desired)
}

// workClientAnchor places a local addition before the next surviving
// baseline member that follows it in desired, without undoing a peer's
// reorder of the existing members; with none it goes last.
func workClientAnchor(merged, following []string, observedSet map[string]struct{}) int {
	for _, nextID := range following {
		if _, wasObserved := observedSet[nextID]; !wasObserved {
			continue
		}
		if index := slices.Index(merged, nextID); index >= 0 {
			return index
		}
	}
	return len(merged)
}

// reorderWorkClients applies a deliberate local reorder: it controls the
// baseline members that still exist, and peer-added members keep their
// relative order after that sequence.
func reorderWorkClients(merged []string, mergedSet map[string]struct{}, desired []string) []string {
	reordered := make([]string, 0, len(merged))
	seen := make(map[string]struct{}, len(merged))
	add := func(clientID string) {
		if _, alreadyAdded := seen[clientID]; !alreadyAdded {
			reordered = append(reordered, clientID)
			seen[clientID] = struct{}{}
		}
	}
	for _, clientID := range desired {
		if _, exists := mergedSet[clientID]; exists {
			add(clientID)
		}
	}
	for _, clientID := range merged {
		add(clientID)
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
