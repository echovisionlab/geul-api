// Package displaysizemigrate renames the stored block field previewWidth to
// displaySize in live content and version snapshots. The value is kept as is.
package displaysizemigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	legacyField = "previewWidth"
	targetField = "displaySize"
)

// targetCatalog is the reviewed catalog that introduced displaySize.
const targetCatalog = "c3faf5bae48b1ef80f2f5496bdc3382130f53b9f744cd35dbe566beab4573fc5"

// sourceCatalogs are the reviewed catalogs that stored version snapshots may
// carry. Between each of them and the target the only change that is not an
// addition is this rename, so a renamed snapshot must validate in the target.
// This allowlist is an offline conversion policy, never a runtime decoder.
var sourceCatalogs = map[string]bool{
	// Before contracts 0.11.0, which only appends the xs and xl collection item sizes.
	"732d62ad223d3012770b10d0a1ab25a8dae2336c52b56ecd01cf276051e75637": true,
	// Before contracts 0.13.0, which only adds optional paragraph link presentation fields.
	"d876a2c013cb49556f8953118b47c81ea0a13262db8f06581672048e48a761a6": true,
	// Before contracts 0.14.0, which renames previewWidth to displaySize.
	"ebe3faa84fc5bd95f8ed1f459ce260f55415232036c709338efdce838e817252": true,
}

func requireTargetCatalog() error {
	if contentv1.ContentBlockCatalogFingerprint != targetCatalog {
		return fmt.Errorf("display size migration requires its reviewed target catalog")
	}
	return nil
}

func decodeJSON(source []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// renameFields renames every previewWidth object key. A source that already
// holds displaySize is ambiguous and is rejected rather than merged.
func renameFields(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if _, legacy := typed[legacyField]; legacy {
			if _, current := typed[targetField]; current {
				return nil, fmt.Errorf("object holds both %s and %s", legacyField, targetField)
			}
		}
		next := make(map[string]any, len(typed))
		for key, item := range typed {
			converted, err := renameFields(item)
			if err != nil {
				return nil, err
			}
			if key == legacyField {
				key = targetField
			}
			next[key] = converted
		}
		return next, nil
	case []any:
		next := make([]any, len(typed))
		for index, item := range typed {
			converted, err := renameFields(item)
			if err != nil {
				return nil, err
			}
			next[index] = converted
		}
		return next, nil
	}
	return value, nil
}

// LiveSharedJSON converts one content_block.shared_data value and validates it
// against the target catalog. changed is false when nothing was renamed.
func LiveSharedJSON(profile, kind string, source []byte) (result []byte, changed bool, err error) {
	if err := requireTargetCatalog(); err != nil {
		return nil, false, err
	}
	if !bytes.Contains(source, []byte(`"`+legacyField+`"`)) {
		return source, false, nil
	}
	value, err := decodeJSON(source)
	if err != nil {
		return nil, false, err
	}
	converted, err := renameFields(value)
	if err != nil {
		return nil, false, err
	}
	if result, err = json.Marshal(converted); err != nil {
		return nil, false, err
	}
	mode := contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT
	if _, err := contentv1.NormalizeContentStorageShared(profile, kind, result, mode); err != nil {
		return nil, false, fmt.Errorf("validate converted %s block: %w", kind, err)
	}
	if err := validateRenameDelta(source, result, false); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

// SnapshotJSON converts one version snapshot envelope to the target catalog.
// changed is false for a snapshot already stored under the target.
func SnapshotJSON(source []byte, kind string) (result []byte, changed bool, err error) {
	if err := requireTargetCatalog(); err != nil {
		return nil, false, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(source, &envelope); err != nil {
		return nil, false, err
	}
	if len(envelope) != 5 {
		return nil, false, fmt.Errorf("invalid version snapshot envelope")
	}
	for _, key := range []string{"schemaVersion", "sourceLocale", "title", "summary", "document"} {
		if _, ok := envelope[key]; !ok {
			return nil, false, fmt.Errorf("missing version snapshot field %s", key)
		}
	}
	var version int
	var locale string
	if json.Unmarshal(envelope["schemaVersion"], &version) != nil || version != 1 ||
		json.Unmarshal(envelope["sourceLocale"], &locale) != nil || locale == "" || locale != strings.TrimSpace(locale) {
		return nil, false, fmt.Errorf("invalid version snapshot identity")
	}
	value, err := decodeJSON(envelope["document"])
	if err != nil {
		return nil, false, err
	}
	document, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("invalid version snapshot document")
	}
	fingerprint, _ := document["blockCatalogFingerprint"].(string)
	if fingerprint == targetCatalog && !bytes.Contains(envelope["document"], []byte(`"`+legacyField+`"`)) {
		return source, false, nil
	}
	if fingerprint != targetCatalog && !sourceCatalogs[fingerprint] {
		return nil, false, fmt.Errorf("unreviewed stored catalog fingerprint")
	}
	converted, err := renameFields(document)
	if err != nil {
		return nil, false, err
	}
	converted.(map[string]any)["blockCatalogFingerprint"] = targetCatalog
	if envelope["document"], err = json.Marshal(converted); err != nil {
		return nil, false, err
	}
	if err := validateSnapshotDocument(envelope["document"], kind, locale); err != nil {
		return nil, false, err
	}
	if result, err = json.Marshal(envelope); err != nil {
		return nil, false, err
	}
	if err := validateRenameDelta(source, result, true); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func validateSnapshotDocument(document []byte, kind, locale string) error {
	mode := contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT
	var err error
	switch kind {
	case "page":
		var typed contentv1.LocalizedPageDocument
		if err := (protojson.UnmarshalOptions{}).Unmarshal(document, &typed); err != nil {
			return err
		}
		if typed.GetLocale() != locale || typed.GetLocaleOverlay().GetLocale() != locale {
			return fmt.Errorf("version locale mismatch")
		}
		err = contentv1.ValidatePageDocument(&contentv1.PageDocument{BlockCatalogFingerprint: typed.GetBlockCatalogFingerprint(), SourceLocale: locale, Base: typed.GetBase(), LocaleOverlays: []*contentv1.PageLocaleOverlay{typed.GetLocaleOverlay()}}, mode)
	case "post", "work":
		var typed contentv1.LocalizedRichTextDocument
		if err := (protojson.UnmarshalOptions{}).Unmarshal(document, &typed); err != nil {
			return err
		}
		profile := contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST
		if kind == "work" {
			profile = contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK
		}
		if typed.GetProfile() != profile || typed.GetLocale() != locale || typed.GetLocaleOverlay().GetLocale() != locale {
			return fmt.Errorf("version profile or locale mismatch")
		}
		err = contentv1.ValidateRichTextDocument(&contentv1.RichTextDocument{BlockCatalogFingerprint: typed.GetBlockCatalogFingerprint(), Profile: profile, SourceLocale: locale, Base: typed.GetBase(), LocaleOverlays: []*contentv1.RichTextLocaleOverlay{typed.GetLocaleOverlay()}}, mode)
	default:
		return fmt.Errorf("unsupported version owner")
	}
	if err != nil {
		return fmt.Errorf("validate converted %s snapshot: %w", kind, err)
	}
	return nil
}

// validateRenameDelta compares the payloads independently of the converter:
// apart from the renamed keys (and, for snapshots, the catalog fingerprint),
// keys, array order, numbers and authored values must all survive unchanged.
func validateRenameDelta(before, after []byte, snapshot bool) error {
	previous, err := decodeJSON(before)
	if err != nil {
		return err
	}
	converted, err := decodeJSON(after)
	if err != nil {
		return err
	}
	var preserves func(previous, converted any, path []string) bool
	preserves = func(previous, converted any, path []string) bool {
		switch value := previous.(type) {
		case map[string]any:
			next, ok := converted.(map[string]any)
			if !ok || len(value) != len(next) {
				return false
			}
			for key, item := range value {
				target := key
				if key == targetField {
					return false
				}
				if key == legacyField {
					target = targetField
				}
				other, exists := next[target]
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
			if snapshot && len(path) == 2 && path[0] == "document" && path[1] == "blockCatalogFingerprint" {
				next, ok := converted.(string)
				return ok && (value == targetCatalog || sourceCatalogs[value]) && next == targetCatalog
			}
		}
		return reflect.DeepEqual(previous, converted)
	}
	if !preserves(previous, converted, nil) {
		return fmt.Errorf("display size conversion changed unrelated data")
	}
	return nil
}
