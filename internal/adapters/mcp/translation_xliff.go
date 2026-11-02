package mcp

import (
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type translationXLIFFExportArguments struct {
	translationTargetArguments
	TargetLocale core.Locale `json:"l"`
	Mode         string      `json:"m"`
	UnitHandles  []string    `json:"u,omitempty"`
}

type translationXLIFFImportArguments struct {
	translationTargetArguments
	TargetLocale           core.Locale `json:"l"`
	Mode                   string      `json:"m"`
	FileID                 string      `json:"f"`
	ExpectedTargetRevision *string     `json:"er,omitempty"`
}

var translationInterchangeModes = map[string]managev1.TranslationInterchangeMode{
	"patch":   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
	"replace": managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
}

var translationInterchangeModeNames = func() map[managev1.TranslationInterchangeMode]string {
	names := make(map[managev1.TranslationInterchangeMode]string, len(translationInterchangeModes))
	for name, mode := range translationInterchangeModes {
		names[mode] = name
	}
	return names
}()

func translationXLIFFExportCommand(arguments mcpserver.ToolArguments) (translationapp.ExportTranslationXLIFFCommand, error) {
	if err := rejectNullArguments(arguments, "u"); err != nil {
		return translationapp.ExportTranslationXLIFFCommand{}, err
	}
	var input translationXLIFFExportArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return translationapp.ExportTranslationXLIFFCommand{}, err
	}
	target, err := translationTarget(input.translationTargetArguments)
	if err != nil {
		return translationapp.ExportTranslationXLIFFCommand{}, err
	}
	if err := validateCompactLocale(input.TargetLocale); err != nil {
		return translationapp.ExportTranslationXLIFFCommand{}, err
	}
	mode, err := translationInterchangeMode(input.Mode)
	if err != nil {
		return translationapp.ExportTranslationXLIFFCommand{}, err
	}
	if err := validateXLIFFSelection(mode, input.UnitHandles); err != nil {
		return translationapp.ExportTranslationXLIFFCommand{}, err
	}
	return translationapp.ExportTranslationXLIFFCommand{
		Target: translationapp.TargetFromMessage(target), TargetLocale: string(input.TargetLocale),
		Mode:        translationapp.InterchangeModeFromMessage(mode),
		UnitHandles: append([]string(nil), input.UnitHandles...),
	}, nil
}

func translationXLIFFImportCommand(arguments mcpserver.ToolArguments) (translationapp.ImportTranslationXLIFFCommand, error) {
	if err := rejectNullArguments(arguments, "er"); err != nil {
		return translationapp.ImportTranslationXLIFFCommand{}, err
	}
	var input translationXLIFFImportArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return translationapp.ImportTranslationXLIFFCommand{}, err
	}
	target, err := translationTarget(input.translationTargetArguments)
	if err != nil {
		return translationapp.ImportTranslationXLIFFCommand{}, err
	}
	if err := validateCompactLocale(input.TargetLocale); err != nil {
		return translationapp.ImportTranslationXLIFFCommand{}, err
	}
	mode, err := translationInterchangeMode(input.Mode)
	if err != nil {
		return translationapp.ImportTranslationXLIFFCommand{}, err
	}
	if err := validateUUID("existing uploaded file ID", input.FileID); err != nil {
		return translationapp.ImportTranslationXLIFFCommand{}, err
	}
	if input.ExpectedTargetRevision != nil {
		if err := validateCompactOpaque("expected target revision", *input.ExpectedTargetRevision, 256); err != nil {
			return translationapp.ImportTranslationXLIFFCommand{}, err
		}
	}
	return translationapp.ImportTranslationXLIFFCommand{
		Target: translationapp.TargetFromMessage(target), TargetLocale: string(input.TargetLocale),
		Mode: translationapp.InterchangeModeFromMessage(mode), FileID: input.FileID,
		ExpectedTargetRevision: input.ExpectedTargetRevision,
	}, nil
}

func translationInterchangeMode(mode string) (managev1.TranslationInterchangeMode, error) {
	value, ok := translationInterchangeModes[mode]
	if !ok {
		return managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_UNSPECIFIED, fmt.Errorf("unsupported XLIFF mode %q", mode)
	}
	return value, nil
}

func validateXLIFFSelection(mode managev1.TranslationInterchangeMode, handles []string) error {
	if len(handles) > 1000 {
		return errors.New("XLIFF export allows at most 1000 explicit unit handles")
	}
	if mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH && len(handles) == 0 {
		return errors.New("patch XLIFF export requires at least one stable unit handle")
	}
	if mode == managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE && len(handles) != 0 {
		return errors.New("replace XLIFF export requires a complete manifest and no unit handles")
	}
	return validateStableUnitHandleSet(handles)
}

func validateStableUnitHandleSet(handles []string) error {
	duplicate, err := inspectStableUnitHandleSet(handles)
	if err != nil {
		return err
	}
	if duplicate != "" {
		return fmt.Errorf("stable unit handle %q is repeated", duplicate)
	}
	return nil
}

func inspectStableUnitHandleSet(handles []string) (string, error) {
	seen := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		if err := validateStableUnitHandle(handle); err != nil {
			return "", err
		}
		if _, duplicate := seen[handle]; duplicate {
			return handle, nil
		}
		seen[handle] = struct{}{}
	}
	return "", nil
}

func validateStableUnitHandle(handle string) error {
	// Handles are opaque owning-domain identities. The application checks them
	// against the current manifest; digit segments can be valid Menu item IDs.
	return validateCompactOpaque("stable unit handle", handle, 256)
}

func compactInterchangeMode(mode managev1.TranslationInterchangeMode) (string, error) {
	value, ok := translationInterchangeModeNames[mode]
	if !ok {
		return "", fmt.Errorf("translation application returned unsupported XLIFF mode %q", mode)
	}
	return value, nil
}
