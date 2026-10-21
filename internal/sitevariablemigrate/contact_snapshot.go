package sitevariablemigrate

import (
	"encoding/json"
	"fmt"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// This allowlist is an offline data conversion policy, never a runtime decoder.
var contactSourceCatalogs = map[string]bool{
	// Verified contracts Git: initial catalog 5a24205, then Mermaid 68d7cd7.
	"1b0b80933dd4e3a92f66d8f85d25c915d74410ee47bb6cf6ec3adffcfe964bc8": true,
	"e037d77d859734c7c716372434602edfe8872d521e8f0f9b9e28e12827635335": true,
	"de190638c6014cc2ac80efce5f747b30d20cc024f3d0820c87ba6c1f1a56e16e": true,
	"2db3b07d50a139a670f68e3cd045063d20475a102acbee7852066b1929b7172b": true,
	"7ee996e5a23685d3b981268c3dd6608d8f006f5b62dbec5fb35e1b4d0532cf49": true,
	"ffc51f7192e406fe25fcb5383428be94e74a52db83c666b550ca38d4db88dc85": true,
	// Observed stored identities, not recovered historical schema definitions.
	// Each snapshot must independently satisfy the complete current schema and
	// the contact/catalog-only delta proof below. No field repair is permitted.
	"5afc20ddc15f7c6099873f33dae6557c5f263e668f12e0babb0186f051a52977": true,
	"0bff2608f4dfcb86193ffbb4ef9da9ad27a9c5ed9812e7dfe15a6711ef873235": true,
	"17a1f44809c2a0c3acc168ae6ad756b00a7d980ccca546881fd3b43b3fbbc401": true,
	// The reviewed target before contracts 0.11.0, which only appends the xs and xl
	// collection item sizes: every snapshot valid there is valid in the new target.
	"732d62ad223d3012770b10d0a1ab25a8dae2336c52b56ecd01cf276051e75637": true,
	// The reviewed target before contracts 0.13.0, which only adds optional
	// paragraph link presentation fields: every snapshot valid there is valid in
	// the new target.
	"d876a2c013cb49556f8953118b47c81ea0a13262db8f06581672048e48a761a6": true,
	// The reviewed target before contracts 0.14.0, which renames previewWidth to
	// displaySize. That rename is not additive: run display-size-migrate first.
	// A snapshot that still holds previewWidth fails the schema check below.
	"ebe3faa84fc5bd95f8ed1f459ce260f55415232036c709338efdce838e817252": true,
}

const contactTargetCatalog = "c3faf5bae48b1ef80f2f5496bdc3382130f53b9f744cd35dbe566beab4573fc5"

func ContactSnapshotJSON(source []byte, kind string) ([]byte, error) {
	if contentv1.ContentBlockCatalogFingerprint != contactTargetCatalog {
		return nil, fmt.Errorf("contact migration requires its reviewed target catalog")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(source, &envelope); err != nil {
		return nil, err
	}
	if len(envelope) != 5 {
		return nil, fmt.Errorf("invalid version snapshot envelope")
	}
	for _, key := range []string{"schemaVersion", "sourceLocale", "title", "summary", "document"} {
		if _, ok := envelope[key]; !ok {
			return nil, fmt.Errorf("missing version snapshot field %s", key)
		}
	}
	var version int
	var locale string
	if json.Unmarshal(envelope["schemaVersion"], &version) != nil || version != 1 || json.Unmarshal(envelope["sourceLocale"], &locale) != nil || locale == "" || locale != strings.TrimSpace(locale) {
		return nil, fmt.Errorf("invalid version snapshot identity")
	}
	for _, key := range []string{"title", "summary"} {
		var value *string
		if err := json.Unmarshal(envelope[key], &value); err != nil {
			return nil, err
		}
		if value != nil {
			text := ContactText(*value)
			envelope[key], _ = json.Marshal(text)
		}
	}
	converted, err := ContactRichJSON(envelope["document"])
	if err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(converted, &document); err != nil {
		return nil, err
	}
	var fingerprint string
	if json.Unmarshal(document["blockCatalogFingerprint"], &fingerprint) != nil || (fingerprint != contactTargetCatalog && !contactSourceCatalogs[fingerprint]) {
		return nil, fmt.Errorf("unreviewed stored catalog fingerprint")
	}
	document["blockCatalogFingerprint"], _ = json.Marshal(contactTargetCatalog)
	converted, err = json.Marshal(document)
	if err != nil {
		return nil, err
	}
	mode := contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT
	switch kind {
	case "page":
		var typed contentv1.LocalizedPageDocument
		if err := (protojson.UnmarshalOptions{}).Unmarshal(converted, &typed); err != nil {
			return nil, err
		}
		if typed.GetLocale() != locale || typed.GetLocaleOverlay().GetLocale() != locale {
			return nil, fmt.Errorf("version locale mismatch")
		}
		err = contentv1.ValidatePageDocument(&contentv1.PageDocument{BlockCatalogFingerprint: typed.GetBlockCatalogFingerprint(), SourceLocale: locale, Base: typed.GetBase(), LocaleOverlays: []*contentv1.PageLocaleOverlay{typed.GetLocaleOverlay()}}, mode)
	case "post", "work":
		var typed contentv1.LocalizedRichTextDocument
		if err := (protojson.UnmarshalOptions{}).Unmarshal(converted, &typed); err != nil {
			return nil, err
		}
		profile := contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST
		if kind == "work" {
			profile = contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK
		}
		if typed.GetProfile() != profile || typed.GetLocale() != locale || typed.GetLocaleOverlay().GetLocale() != locale {
			return nil, fmt.Errorf("version profile or locale mismatch")
		}
		err = contentv1.ValidateRichTextDocument(&contentv1.RichTextDocument{BlockCatalogFingerprint: typed.GetBlockCatalogFingerprint(), Profile: profile, SourceLocale: locale, Base: typed.GetBase(), LocaleOverlays: []*contentv1.RichTextLocaleOverlay{typed.GetLocaleOverlay()}}, mode)
	default:
		return nil, fmt.Errorf("unsupported version owner")
	}
	if err != nil {
		return nil, fmt.Errorf("validate converted %s snapshot: %w", kind, err)
	}
	envelope["document"] = converted
	result, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if err := validateContactSnapshotDelta(source, result); err != nil {
		return nil, err
	}
	return result, nil
}
