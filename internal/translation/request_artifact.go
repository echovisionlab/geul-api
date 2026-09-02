package translation

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const RequestManifestVersion = 1

// RequestArtifact is the immutable provider input captured in the same
// transaction as a Translation Job. XLIFF is the provider-facing source;
// Manifest retains the stable domain unit mapping and generation policy needed
// to process the request without re-extracting a newer source document.
type RequestArtifact struct {
	XLIFF    []byte
	Manifest []byte
	Digest   string
}

type RequestManifest struct {
	Version int               `json:"version"`
	Profile GenerationProfile `json:"profile"`
	Plan    ExtractionPlan    `json:"plan"`
}

// CanonicalizeRequestManifest restores the versioned manifest structure and
// serializes it deterministically. PostgreSQL jsonb does not preserve the
// original JSON object representation, so artifact identity must use this
// canonical representation rather than the bytes returned by the database.
func CanonicalizeRequestManifest(manifest []byte) ([]byte, error) {
	if len(bytes.TrimSpace(manifest)) == 0 {
		return nil, fmt.Errorf("translation request manifest is required")
	}
	var parsed RequestManifest
	if err := json.Unmarshal(manifest, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal translation request manifest: %w", err)
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("marshal translation request manifest: %w", err)
	}
	return canonical, nil
}

// BuildRequestArtifact serializes one exact provider request as canonical
// XLIFF 2.2 plus a versioned stable-unit manifest.
func BuildRequestArtifact(request ProviderRequest, plan *ExtractionPlan) (RequestArtifact, error) {
	if plan == nil {
		return RequestArtifact{}, fmt.Errorf("translation request plan is required")
	}
	if strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.OperationID) == "" {
		return RequestArtifact{}, fmt.Errorf("translation request identity is required")
	}
	if err := ValidateXLIFFDocument(&request.Document, false); err != nil {
		return RequestArtifact{}, err
	}
	if err := validateRequestPlan(request.Document, plan); err != nil {
		return RequestArtifact{}, err
	}
	if err := validateRequestProfile(request.Document, request.Profile); err != nil {
		return RequestArtifact{}, err
	}
	xliff, err := MarshalXLIFF(&request.Document)
	if err != nil {
		return RequestArtifact{}, err
	}
	manifest, err := json.Marshal(RequestManifest{
		Version: RequestManifestVersion, Profile: request.Profile, Plan: *plan,
	})
	if err != nil {
		return RequestArtifact{}, fmt.Errorf("marshal translation request manifest: %w", err)
	}
	manifest, err = CanonicalizeRequestManifest(manifest)
	if err != nil {
		return RequestArtifact{}, err
	}
	return RequestArtifact{
		XLIFF: xliff, Manifest: manifest,
		Digest: RequestArtifactDigest(xliff, manifest),
	}, nil
}

// RequestArtifactDigest returns the lowercase SHA-256 identity of the exact
// canonical outbound XLIFF and manifest bytes. Length framing prevents two
// differently split byte pairs from sharing an input stream.
func RequestArtifactDigest(xliff, manifest []byte) string {
	hasher := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(xliff)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write(xliff)
	binary.BigEndian.PutUint64(size[:], uint64(len(manifest)))
	_, _ = hasher.Write(size[:])
	_, _ = hasher.Write(manifest)
	return hex.EncodeToString(hasher.Sum(nil))
}

// ParseRequestArtifact restores the exact provider request and stable plan.
func ParseRequestArtifact(artifact RequestArtifact) (ProviderRequest, *ExtractionPlan, error) {
	if len(bytes.TrimSpace(artifact.XLIFF)) == 0 || len(bytes.TrimSpace(artifact.Manifest)) == 0 {
		return ProviderRequest{}, nil, fmt.Errorf("translation request artifact is incomplete")
	}
	document, err := UnmarshalXLIFF(artifact.XLIFF)
	if err != nil {
		return ProviderRequest{}, nil, err
	}
	var manifest RequestManifest
	if err := json.Unmarshal(artifact.Manifest, &manifest); err != nil {
		return ProviderRequest{}, nil, fmt.Errorf("unmarshal translation request manifest: %w", err)
	}
	if manifest.Version != RequestManifestVersion {
		return ProviderRequest{}, nil, fmt.Errorf("unsupported translation request manifest version %d", manifest.Version)
	}
	if err := validateRequestPlan(*document, &manifest.Plan); err != nil {
		return ProviderRequest{}, nil, err
	}
	if err := validateRequestProfile(*document, manifest.Profile); err != nil {
		return ProviderRequest{}, nil, err
	}
	rehydrateXLIFFMetadata(document, &manifest.Plan)
	return ProviderRequest{
		Profile: manifest.Profile, Document: *document,
	}, &manifest.Plan, nil
}

func validateRequestPlan(document XLIFFDocument, plan *ExtractionPlan) error {
	if plan == nil || strings.TrimSpace(plan.EntityType) == "" || strings.TrimSpace(plan.EntityID) == "" {
		return fmt.Errorf("translation request plan target is required")
	}
	if document.SourceLocale != plan.SourceLocale || document.TargetLocale != plan.TargetLocale {
		return fmt.Errorf("translation request plan locales do not match XLIFF")
	}
	planUnits := make(map[string]struct{}, len(plan.Units))
	planUnitMetadata := make(map[string]Unit, len(plan.Units))
	for _, unit := range plan.Units {
		if strings.TrimSpace(unit.UnitID) == "" {
			return fmt.Errorf("translation request manifest unit identity is required")
		}
		if _, duplicate := planUnits[unit.UnitID]; duplicate {
			return fmt.Errorf("duplicate translation request manifest unit %q", unit.UnitID)
		}
		planUnits[unit.UnitID] = struct{}{}
		planUnitMetadata[unit.UnitID] = unit
	}
	documentUnits := make(map[string]struct{}, len(planUnits))
	documentGroups := make(map[string]struct{}, len(document.File.Groups))
	for _, group := range document.File.Groups {
		if strings.TrimSpace(group.ID) == "" {
			return fmt.Errorf("translation request XLIFF group identity is required")
		}
		if _, duplicate := documentGroups[group.ID]; duplicate {
			return fmt.Errorf("duplicate translation request XLIFF group %q", group.ID)
		}
		documentGroups[group.ID] = struct{}{}
		for _, unit := range group.TranslationUnit {
			if strings.TrimSpace(unit.ID) == "" {
				return fmt.Errorf("translation request XLIFF unit identity is required")
			}
			if _, duplicate := documentUnits[unit.ID]; duplicate {
				return fmt.Errorf("duplicate translation request XLIFF unit %q", unit.ID)
			}
			documentUnits[unit.ID] = struct{}{}
		}
	}
	if len(planUnits) != len(documentUnits) {
		return fmt.Errorf("translation request manifest units do not match XLIFF")
	}
	for unitID := range planUnits {
		if _, ok := documentUnits[unitID]; !ok {
			return fmt.Errorf("translation request manifest unit %q is missing from XLIFF", unitID)
		}
	}
	manifestUnits := make(map[string]struct{}, len(planUnits))
	manifestGroups := make(map[string]struct{}, len(plan.Bundles))
	for _, bundle := range plan.Bundles {
		if strings.TrimSpace(bundle.BundleID) == "" {
			return fmt.Errorf("translation request manifest bundle identity is required")
		}
		if bundle.EntityType != plan.EntityType || bundle.EntityID != plan.EntityID ||
			bundle.SourceLocale != plan.SourceLocale || bundle.TargetLocale != plan.TargetLocale {
			return fmt.Errorf("translation request manifest bundle %q identity does not match the plan", bundle.BundleID)
		}
		if _, duplicate := manifestGroups[bundle.BundleID]; duplicate {
			return fmt.Errorf("duplicate translation request manifest bundle %q", bundle.BundleID)
		}
		manifestGroups[bundle.BundleID] = struct{}{}
		for _, unit := range bundle.Units {
			flatUnit, ok := planUnitMetadata[unit.UnitID]
			if !ok {
				return fmt.Errorf("translation request manifest bundle unit %q is not in the flat plan", unit.UnitID)
			}
			if !sameRequestUnitMetadata(flatUnit, unit) {
				return fmt.Errorf("translation request manifest bundle unit %q metadata does not match the flat plan", unit.UnitID)
			}
			if _, duplicate := manifestUnits[unit.UnitID]; duplicate {
				return fmt.Errorf("duplicate translation request manifest bundle unit %q", unit.UnitID)
			}
			manifestUnits[unit.UnitID] = struct{}{}
		}
	}
	if len(manifestGroups) != len(documentGroups) || len(manifestUnits) != len(planUnits) {
		return fmt.Errorf("translation request manifest bundles do not match XLIFF units")
	}
	for groupID := range documentGroups {
		if _, ok := manifestGroups[groupID]; !ok {
			return fmt.Errorf("translation request manifest bundle %q is missing", groupID)
		}
	}
	for unitID := range planUnits {
		if _, ok := manifestUnits[unitID]; !ok {
			return fmt.Errorf("translation request manifest bundle unit %q is missing", unitID)
		}
	}
	for _, group := range document.File.Groups {
		for _, unit := range group.TranslationUnit {
			manifestUnit := planUnitMetadata[unit.ID]
			if unit.Name != manifestUnit.Path {
				return fmt.Errorf("translation request XLIFF unit %q metadata does not match the manifest", unit.ID)
			}
		}
	}
	return nil
}

func sameRequestUnitMetadata(left, right Unit) bool {
	return left.UnitID == right.UnitID && left.EntityType == right.EntityType &&
		left.EntityID == right.EntityID && left.Path == right.Path &&
		left.ContainerType == right.ContainerType && left.ContainerID == right.ContainerID &&
		left.FieldName == right.FieldName && left.SourceFormat == right.SourceFormat &&
		left.SourceLocale == right.SourceLocale
}

func validateRequestProfile(document XLIFFDocument, profile GenerationProfile) error {
	if profile.SourceLocale != document.SourceLocale || profile.TargetLocale != document.TargetLocale {
		return fmt.Errorf("translation request generation profile locales do not match XLIFF")
	}
	return nil
}

func rehydrateXLIFFMetadata(document *XLIFFDocument, plan *ExtractionPlan) {
	units := make(map[string]Unit, len(plan.Units))
	for _, unit := range plan.Units {
		units[unit.UnitID] = unit
	}
	bundles := make(map[string]Bundle, len(plan.Bundles))
	for _, bundle := range plan.Bundles {
		bundles[bundle.BundleID] = bundle
	}
	for groupIndex := range document.File.Groups {
		group := &document.File.Groups[groupIndex]
		if bundle, ok := bundles[group.ID]; ok {
			group.SequenceIndex = bundle.SequenceIndex
			group.SequenceTotal = bundle.SequenceTotal
		}
		for unitIndex := range group.TranslationUnit {
			unit := &group.TranslationUnit[unitIndex]
			if manifestUnit, ok := units[unit.ID]; ok {
				unit.FieldName = manifestUnit.FieldName
				unit.SourceFormat = manifestUnit.SourceFormat
				unit.ContainerType = manifestUnit.ContainerType
				unit.ContainerID = manifestUnit.ContainerID
			}
		}
	}
}
