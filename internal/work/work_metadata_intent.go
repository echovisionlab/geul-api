package work

import (
	"reflect"

	"github.com/echovisionlab/geul-api/internal/structured"
)

func mergeWorkMetadataIntent(current, observed, desired structured.Fields) structured.Fields {
	return structured.Fields(mergeMetadataObject(current, observed, desired))
}

func mergeMetadataObject(current, observed, desired map[string]interface{}) map[string]interface{} {
	merged := cloneMetadataObject(current)
	empty := map[string]interface{}{}
	for key, before := range observed {
		after, desiredExists := desired[key]
		if desiredExists && reflect.DeepEqual(before, after) {
			continue
		}
		beforeObject, beforeIsObject := metadataObject(before)
		afterObject, afterIsObject := metadataObject(after)
		if desiredExists && beforeIsObject && afterIsObject {
			currentObject, _ := metadataObject(current[key])
			merged[key] = mergeMetadataObject(currentObject, beforeObject, afterObject)
			continue
		}
		if !desiredExists && beforeIsObject {
			currentObject, _ := metadataObject(current[key])
			remaining := mergeMetadataObject(currentObject, beforeObject, empty)
			if len(remaining) == 0 {
				delete(merged, key)
			} else {
				merged[key] = remaining
			}
			continue
		}
		if desiredExists {
			merged[key] = cloneMetadataValue(after)
		} else {
			delete(merged, key)
		}
	}
	for key, after := range desired {
		if _, wasObserved := observed[key]; wasObserved {
			continue
		}
		if afterObject, isObject := metadataObject(after); isObject {
			currentObject, currentIsObject := metadataObject(current[key])
			if !currentIsObject {
				currentObject = empty
			}
			merged[key] = mergeMetadataObject(currentObject, empty, afterObject)
			continue
		}
		merged[key] = cloneMetadataValue(after)
	}
	return merged
}

func metadataObject(value interface{}) (map[string]interface{}, bool) {
	object, ok := value.(map[string]interface{})
	return object, ok
}

func cloneMetadataObject(value map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(value))
	for key, nested := range value {
		cloned[key] = cloneMetadataValue(nested)
	}
	return cloned
}

func cloneMetadataValue(value interface{}) interface{} {
	if object, ok := metadataObject(value); ok {
		return cloneMetadataObject(object)
	}
	if values, ok := value.([]interface{}); ok {
		cloned := make([]interface{}, len(values))
		for index, nested := range values {
			cloned[index] = cloneMetadataValue(nested)
		}
		return cloned
	}
	return value
}
