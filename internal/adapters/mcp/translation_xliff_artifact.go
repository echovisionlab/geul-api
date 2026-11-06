package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
	"google.golang.org/protobuf/types/known/timestamppb"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func encodeXLIFFExport(response translationapp.XLIFFExport) ([]byte, error) {
	artifact := response.Artifact
	if artifact == nil || strings.TrimSpace(artifact.FileID) == "" || strings.TrimSpace(artifact.URL) == "" || artifact.ExpiresAt.IsZero() {
		return nil, errors.New("translation application returned an incomplete XLIFF artifact reference")
	}
	if err := validateUUID("Translation application XLIFF artifact file ID", artifact.FileID); err != nil {
		return nil, err
	}
	if err := validateArtifactURL(artifact.URL); err != nil {
		return nil, err
	}
	expiresAt, err := timestampText(timestamppb.New(artifact.ExpiresAt))
	if err != nil || expiresAt == nil {
		return nil, errors.New("translation application returned an invalid XLIFF artifact expiration")
	}
	mode, err := interchangeModeName(translationapp.InterchangeModeMessage(response.Mode))
	if err != nil {
		return nil, err
	}
	if err := validateLocaleCode(core.Locale(response.SourceLocale)); err != nil {
		return nil, err
	}
	if err := validateLocaleCode(core.Locale(response.TargetLocale)); err != nil {
		return nil, err
	}
	if err := validateOpaqueValue("Translation application XLIFF artifact extension", artifact.Extension, 32); err != nil {
		return nil, err
	}
	if err := validateOpaqueValue("Translation application XLIFF artifact MIME type", artifact.MimeType, 128); err != nil {
		return nil, err
	}
	artifactOutput := map[string]any{
		"file_id":    artifact.FileID,
		"url":        artifact.URL,
		"expires_at": *expiresAt,
		"extension":  artifact.Extension,
		"mime_type":  artifact.MimeType,
	}
	if artifact.FileName != nil {
		if err := validateOpaqueValue("Translation application XLIFF artifact file name", *artifact.FileName, 512); err != nil {
			return nil, err
		}
		artifactOutput["file_name"] = *artifact.FileName
	}
	output := map[string]any{
		"artifact":      artifactOutput,
		"source_locale": response.SourceLocale,
		"locale":        response.TargetLocale,
		"mode":          mode,
	}
	if response.TargetRevision != nil {
		if err := validateOpaqueValue("Translation application XLIFF target revision", *response.TargetRevision, 256); err != nil {
			return nil, err
		}
		output["target_revision"] = *response.TargetRevision
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

func encodeXLIFFImport(response translationapp.XLIFFImport) ([]byte, error) {
	if err := validateOpaqueValue("Translation application target revision", response.TargetRevision, 256); err != nil {
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
		"target_revision": response.TargetRevision,
		"changed":         response.Changed,
		"unit_ids":        handles,
	})
}
