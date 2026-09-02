package contentblock

import (
	"encoding/json"
	"fmt"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

const (
	generatedMaxDocumentBlocks = 10_000
	generatedMaxBlockDepth     = 32
	translationSourceLocale    = "source"
)

type generatedContract struct{}

// NewGeneratedContract returns the production adapter around the generated
// Content Block catalog. The generated package remains the only authority for
// payload kinds, field ownership, File references, and parent/slot rules.
func NewGeneratedContract() Contract {
	return generatedContract{}
}

// NewGeneratedStore constructs the production Store with the generated
// catalog and an explicit File reuse authority.
func NewGeneratedStore(reuse FileReuseAuthorizer, options ...Option) (*Store, error) {
	return NewStore(NewGeneratedContract(), reuse, options...)
}

func (generatedContract) Limits(profile string) (Limits, error) {
	if _, err := contentv1.ParseRichTextProfileStorageName(profile); err != nil {
		return Limits{}, err
	}
	return Limits{MaxBlocks: generatedMaxDocumentBlocks, MaxDepth: generatedMaxBlockDepth}, nil
}

func (generatedContract) ValidateBlock(profile string, block FullBlock) (ValidatedPayload, error) {
	validated, err := contentv1.NormalizeContentStorageBlock(
		profile,
		block.Kind,
		block.SharedData,
		block.LocalizedData,
	)
	if err != nil {
		return ValidatedPayload{}, err
	}
	references, err := fileReferencesFromGenerated(validated.FileReferences)
	if err != nil {
		return ValidatedPayload{}, err
	}
	return ValidatedPayload{
		SharedData:     append(json.RawMessage(nil), validated.SharedData...),
		LocalizedData:  append(json.RawMessage(nil), validated.LocalizedData...),
		FileReferences: references,
	}, nil
}

func fileReferencesFromGenerated(input []contentv1.ContentStorageFileReference) ([]FileReference, error) {
	references := make([]FileReference, 0, len(input))
	for _, reference := range input {
		fileID, err := uuid.Parse(reference.FileID)
		if err != nil {
			return nil, fmt.Errorf("invalid generated File UUID: %w", err)
		}
		references = append(references, FileReference{
			ReferencePath:       reference.ReferencePath,
			FileID:              fileID,
			Missing:             reference.Missing,
			MissingMediaKind:    missingAttachmentMediaKindName(reference.MissingAttachmentMediaKind),
			AllowedMIMETypes:    append([]string(nil), reference.AllowedMIMETypes...),
			AllowedMIMEPrefixes: append([]string(nil), reference.AllowedMIMEPrefixes...),
		})
	}
	return references, nil
}

func missingAttachmentMediaKindName(kind contentv1.MissingAttachmentMediaKind) string {
	switch kind {
	case contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_IMAGE:
		return "image"
	case contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_AUDIO:
		return "audio"
	case contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_VIDEO:
		return "video"
	case contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE:
		return "file"
	default:
		return ""
	}
}

func (generatedContract) ValidateLocale(profile, kind string, localizedData json.RawMessage) (json.RawMessage, error) {
	validated, err := contentv1.NormalizeContentStorageLocale(
		profile,
		kind,
		localizedData,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), validated...), nil
}

func (generatedContract) BuildExplicitEmptyLocale(
	profile string,
	kind string,
	localizedData json.RawMessage,
) (json.RawMessage, error) {
	empty, err := contentv1.BuildExplicitEmptyContentStorageLocale(profile, kind, localizedData)
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), empty...), nil
}

func (generatedContract) ValidateParent(profile string, parent *FullBlock, child FullBlock) error {
	var parentKind string
	var parentShared []byte
	if parent != nil {
		parentKind = parent.Kind
		parentShared = parent.SharedData
	}
	return contentv1.ValidateContentStorageParent(
		profile,
		parentKind,
		parentShared,
		child.Kind,
		child.SharedData,
		child.ContainerSlot,
	)
}

func (generatedContract) TranslationSourceChanged(profile string, before, after []FullBlock) (bool, error) {
	return contentv1.ContentStorageTranslationSourceChanged(
		profile,
		fullBlocksToStorageRows(before, translationSourceLocale),
		fullBlocksToStorageRows(after, translationSourceLocale),
		translationSourceLocale,
	)
}

func fullBlocksToStorageRows(blocks []FullBlock, locale string) []contentv1.ContentStorageRow {
	rows := make([]contentv1.ContentStorageRow, 0, len(blocks))
	for _, block := range blocks {
		parentID := ""
		if block.ParentID != nil {
			parentID = block.ParentID.String()
		}
		rows = append(rows, contentv1.ContentStorageRow{
			BlockID:       block.ID.String(),
			ParentBlockID: parentID,
			ContainerSlot: block.ContainerSlot,
			Position:      int32(block.Position),
			Kind:          block.Kind,
			SharedData:    append([]byte(nil), block.SharedData...),
			Locales: []contentv1.ContentStorageLocale{{
				Locale:        locale,
				LocalizedData: append([]byte(nil), block.LocalizedData...),
			}},
		})
	}
	return rows
}
