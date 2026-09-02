package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func encodeXLIFFExport(response *managev1.ExportEntityTranslationXLIFFResponse) ([]byte, error) {
	artifact := response.Artifact
	if artifact == nil || strings.TrimSpace(artifact.FileId) == "" || strings.TrimSpace(artifact.Url) == "" || artifact.ExpiresAt == nil {
		return nil, errors.New("translation application returned an incomplete XLIFF artifact reference")
	}
	if err := validateUUID("Translation application XLIFF artifact file ID", artifact.FileId); err != nil {
		return nil, err
	}
	if err := validateArtifactURL(artifact.Url); err != nil {
		return nil, err
	}
	expiresAt, err := compactTimestamp(artifact.ExpiresAt)
	if err != nil || expiresAt == nil {
		return nil, errors.New("translation application returned an invalid XLIFF artifact expiration")
	}
	mode, err := compactInterchangeMode(response.Mode)
	if err != nil {
		return nil, err
	}
	if err := validateCompactLocale(core.Locale(response.SourceLocale)); err != nil {
		return nil, err
	}
	if err := validateCompactLocale(core.Locale(response.TargetLocale)); err != nil {
		return nil, err
	}
	if err := validateCompactOpaque("Translation application XLIFF artifact extension", artifact.Extension, 32); err != nil {
		return nil, err
	}
	if err := validateCompactOpaque("Translation application XLIFF artifact MIME type", artifact.MimeType, 128); err != nil {
		return nil, err
	}
	artifactOutput := map[string]any{
		"f": artifact.FileId,
		"u": artifact.Url,
		"e": *expiresAt,
		"x": artifact.Extension,
		"t": artifact.MimeType,
	}
	if artifact.FileName != nil {
		if err := validateCompactOpaque("Translation application XLIFF artifact file name", *artifact.FileName, 512); err != nil {
			return nil, err
		}
		artifactOutput["n"] = *artifact.FileName
	}
	output := map[string]any{
		"a": artifactOutput,
		"s": response.SourceLocale,
		"l": response.TargetLocale,
		"m": mode,
	}
	if response.TargetRevision != nil {
		if err := validateCompactOpaque("Translation application XLIFF target revision", *response.TargetRevision, 256); err != nil {
			return nil, err
		}
		output["r"] = *response.TargetRevision
	}
	return json.Marshal(output)
}

func validateArtifactURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("translation application returned an invalid XLIFF artifact URL")
	}
	return nil
}

func encodeXLIFFImport(response *managev1.ImportEntityTranslationXLIFFResponse) ([]byte, error) {
	if err := validateCompactOpaque("Translation application target revision", response.TargetRevision, 256); err != nil {
		return nil, err
	}
	duplicate, err := inspectStableUnitHandleSet(response.AffectedUnitHandles)
	if err != nil {
		return nil, fmt.Errorf("translation application returned invalid affected unit: %w", err)
	}
	if duplicate != "" {
		return nil, fmt.Errorf("translation application returned repeated affected unit %q", duplicate)
	}
	handles := make([]string, len(response.AffectedUnitHandles))
	copy(handles, response.AffectedUnitHandles)
	return json.Marshal(map[string]any{
		"r": response.TargetRevision,
		"c": response.Changed,
		"u": handles,
	})
}
