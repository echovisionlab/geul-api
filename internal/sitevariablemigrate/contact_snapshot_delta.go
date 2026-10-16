package sitevariablemigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// Catalog admission alone never authorizes a snapshot rewrite. Compare the
// complete payload independently of the converter: keys, array order, numbers,
// identities, styles and unrelated authored values must all survive unchanged.
func validateContactSnapshotDelta(before, after []byte) error {
	decode := func(source []byte) (any, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(source))
		decoder.UseNumber()
		err := decoder.Decode(&value)
		return value, err
	}
	previous, err := decode(before)
	if err != nil {
		return err
	}
	converted, err := decode(after)
	if err != nil {
		return err
	}
	var preserves func(any, any, []string) bool
	preserves = func(previous, converted any, path []string) bool {
		if reflect.DeepEqual(previous, converted) {
			return true
		}
		switch value := previous.(type) {
		case map[string]any:
			next, ok := converted.(map[string]any)
			if !ok || len(value) != len(next) {
				return false
			}
			for key, item := range value {
				other, exists := next[key]
				if !exists || !preserves(item, other, append(path, key)) {
					return false
				}
			}
			return true
		case []any:
			next, ok := converted.([]any)
			if !ok || len(value) != len(next) {
				return false
			}
			for index, item := range value {
				if !preserves(item, next[index], append(path, "[]")) {
					return false
				}
			}
			return true
		case string:
			next, ok := converted.(string)
			if !ok || len(path) == 0 {
				return false
			}
			if len(path) == 2 && path[0] == "document" && path[1] == "blockCatalogFingerprint" {
				return contactSourceCatalogs[value] && next == contactTargetCatalog
			}
			if len(path) == 1 && (path[0] == "title" || path[0] == "summary") {
				return next == ContactText(value)
			}
			if path[0] != "document" || len(path) < 2 {
				return false
			}
			field := path[len(path)-1]
			if field == "key" && path[len(path)-2] == "siteVariable" {
				return (value == "contact.supportEmail" || value == "contact.privacyEmail") && next == "contact.email"
			}
			return (field == "text" || field == "href") && next == ContactText(value)
		}
		return false
	}
	if !preserves(previous, converted, nil) {
		return fmt.Errorf("contact snapshot conversion changed unrelated data")
	}
	return nil
}
