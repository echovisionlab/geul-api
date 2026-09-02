package contentblock

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// MutationContributor returns the one Member who authored an interactive
// Content Block mutation. Presence and edit permission remain domain-owned.
func MutationContributor(memberIDs []string) (string, error) {
	if len(memberIDs) != 1 {
		return "", errs.InvalidArgument(
			"contributor_member_ids",
			"collaboration mutation requires exactly one origin Member",
		)
	}
	memberID := strings.TrimSpace(memberIDs[0])
	if _, err := uuidutil.ParseCanonical(memberID, "contributor_member_ids"); err != nil {
		return "", errs.InvalidArgument(
			"contributor_member_ids",
			"must contain one canonical Member UUID",
		)
	}
	return memberID, nil
}

// BatchFromRichTextProto converts one strict aggregate Rich Text mutation to
// the Store boundary. The generated catalog owns all oneof and storage JSON
// mapping.
func BatchFromRichTextProto(documentID uuid.UUID, input *contentv1.RichTextBlockMutationBatch) (Batch, error) {
	profile, err := contentv1.RichTextProfileStorageName(input.GetProfile())
	if err != nil {
		return Batch{}, invalidProtoMutation("Rich Text profile", err)
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		input,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return Batch{}, invalidProtoMutation("flatten Rich Text mutation", err)
	}
	return batchFromStorage(documentID, profile, storage)
}

// BatchFromRichTextSystemProto converts a trusted system mutation. System
// translation has no human attribution and must not invent a contributor UUID;
// the generated flattener still owns every other mutation check.
func BatchFromRichTextSystemProto(documentID uuid.UUID, input *contentv1.RichTextBlockMutationBatch) (Batch, error) {
	profile, err := contentv1.RichTextProfileStorageName(input.GetProfile())
	if err != nil {
		return Batch{}, invalidProtoMutation("system Rich Text profile", err)
	}
	storage, err := contentv1.FlattenRichTextSystemMutationBatchStorage(
		input,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return Batch{}, invalidProtoMutation("flatten system Rich Text mutation", err)
	}
	return batchFromStorage(documentID, profile, storage)
}

// BatchFromRichTextStorage converts generated Rich Text storage mutations
// after an owning adapter has combined multiple already-flattened locale
// changes (for example, source-seeding an absent target locale). The Store
// still performs the authoritative aggregate and generated Contract
// validation before persistence.
func BatchFromRichTextStorage(
	documentID uuid.UUID,
	profile contentv1.RichTextProfile,
	input contentv1.ContentStorageMutationBatch,
) (Batch, error) {
	profileName, err := contentv1.RichTextProfileStorageName(profile)
	if err != nil {
		return Batch{}, invalidProtoMutation("Rich Text profile", err)
	}
	return batchFromStorage(documentID, profileName, input)
}

// SnapshotToRichTextDocument materializes the durable aggregate, including
// every locale overlay, into the generated transport contract.
func SnapshotToRichTextDocument(input Snapshot) (*contentv1.RichTextDocument, error) {
	profile, err := contentv1.ParseRichTextProfileStorageName(input.Document.Profile)
	if err != nil {
		return nil, invalidProtoMutation("Rich Text profile", err)
	}
	document, err := contentv1.MaterializeRichTextDocumentStorage(
		profile,
		input.SourceLocale,
		snapshotToStorageRows(input),
	)
	if err != nil {
		return nil, invalidProtoMutation("materialize Rich Text document", err)
	}
	return document, nil
}

// SnapshotToLocalizedRichTextDocument projects the durable presence of one
// locale. Target Blocks absent from storage remain absent so translation apply
// can distinguish a missing unit from an explicitly empty localized value.
// Read-time source fallback belongs to MaterializeSnapshotRichTextLocale.
func SnapshotToLocalizedRichTextDocument(input Snapshot, locale string) (*contentv1.LocalizedRichTextDocument, error) {
	profile, err := contentv1.ParseRichTextProfileStorageName(input.Document.Profile)
	if err != nil {
		return nil, invalidProtoMutation("Rich Text profile", err)
	}
	document, err := contentv1.MaterializeRichTextDocumentStorage(
		profile,
		input.SourceLocale,
		snapshotToStorageRows(input),
	)
	if err != nil {
		return nil, invalidProtoMutation("materialize localized Rich Text document", err)
	}
	overlay := &contentv1.RichTextLocaleOverlay{Locale: locale}
	for _, candidate := range document.GetLocaleOverlays() {
		if candidate.GetLocale() == locale {
			overlay = proto.Clone(candidate).(*contentv1.RichTextLocaleOverlay)
			break
		}
	}
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		Locale:                  locale,
		Base:                    proto.Clone(document.GetBase()).(*contentv1.RichTextBlockGraph),
		LocaleOverlay:           overlay,
	}, nil
}

// ReplaceFromRichTextProto converts a complete aggregate document for initial
// creation/import. Missing attachments are rejected in this write path.
func ReplaceFromRichTextProto(
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	input *contentv1.RichTextDocument,
) (ReplaceInput, error) {
	rows, err := contentv1.FlattenRichTextDocumentStorage(
		input,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return ReplaceInput{}, invalidProtoMutation("flatten Rich Text document", err)
	}
	return replaceFromStorage(documentID, expectedRevision, rows)
}

// ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments converts a
// Version snapshot after rewriting only the explicitly unavailable active File
// references to generated, restore-only missing_attachment values. The input
// snapshot is not mutated.
func ReplaceFromLocalizedRichTextProtoWithUnavailableAttachments(
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	input *contentv1.LocalizedRichTextDocument,
	unavailable map[uuid.UUID]contentv1.MissingAttachmentMediaKind,
) (ReplaceInput, error) {
	return replaceFromLocalizedRichTextProto(documentID, expectedRevision, input, unavailable)
}

func replaceFromLocalizedRichTextProto(
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	input *contentv1.LocalizedRichTextDocument,
	unavailable map[uuid.UUID]contentv1.MissingAttachmentMediaKind,
) (ReplaceInput, error) {
	if input == nil {
		return ReplaceInput{}, invalidProtoMutation("localized Rich Text document", fmt.Errorf("document is required"))
	}
	aggregate := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: input.GetBlockCatalogFingerprint(),
		Profile:                 input.GetProfile(),
		SourceLocale:            input.GetLocale(),
		Base:                    input.GetBase(),
		LocaleOverlays:          []*contentv1.RichTextLocaleOverlay{input.GetLocaleOverlay()},
	}
	rows, err := contentv1.FlattenRichTextDocumentStorage(
		aggregate,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	)
	if err != nil {
		return ReplaceInput{}, invalidProtoMutation("flatten localized Rich Text document", err)
	}
	if len(unavailable) != 0 {
		unavailableStorage, unavailableErr := missingAttachmentKindsToStorage(unavailable)
		if unavailableErr != nil {
			return ReplaceInput{}, invalidProtoMutation("unavailable Rich Text attachments", unavailableErr)
		}
		profile, profileErr := contentv1.RichTextProfileStorageName(input.GetProfile())
		if profileErr != nil {
			return ReplaceInput{}, invalidProtoMutation("Rich Text profile", profileErr)
		}
		rows, err = contentv1.RewriteContentStorageMissingAttachments(
			profile,
			input.GetLocale(),
			rows,
			unavailableStorage,
		)
		if err != nil {
			return ReplaceInput{}, invalidProtoMutation("rewrite unavailable Rich Text attachments", err)
		}
	}
	return replaceFromStorage(documentID, expectedRevision, rows)
}

func missingAttachmentKindsToStorage(
	input map[uuid.UUID]contentv1.MissingAttachmentMediaKind,
) (map[string]contentv1.MissingAttachmentMediaKind, error) {
	result := make(map[string]contentv1.MissingAttachmentMediaKind, len(input))
	for fileID, mediaKind := range input {
		if fileID == uuid.Nil {
			return nil, fmt.Errorf("unavailable File UUID is required")
		}
		if mediaKind == contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_UNSPECIFIED {
			return nil, fmt.Errorf("unavailable File %s media kind is required", fileID)
		}
		result[fileID.String()] = mediaKind
	}
	return result, nil
}

func batchFromStorage(documentID uuid.UUID, profile string, input contentv1.ContentStorageMutationBatch) (Batch, error) {
	expectedRevision, err := parseRequiredUUID(input.ExpectedRevision, "expected revision")
	if err != nil {
		return Batch{}, err
	}
	batch := Batch{
		DocumentID: documentID, ExpectedRevision: expectedRevision, validatedProfile: profile,
		validatedBaseReferences: make(map[uuid.UUID][]FileReference, len(input.BaseUpserts)),
	}
	for _, row := range input.BaseUpserts {
		block, err := baseBlockFromStorage(row)
		if err != nil {
			return Batch{}, err
		}
		batch.Upserts = append(batch.Upserts, block)
		validated, err := contentv1.NormalizeContentStorageShared(
			profile, row.Kind, row.SharedData,
			contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
		)
		if err != nil {
			return Batch{}, invalidProtoMutation("normalize shared Block mutation", err)
		}
		references, err := fileReferencesFromGenerated(validated.FileReferences)
		if err != nil {
			return Batch{}, invalidProtoMutation("extract shared Block File references", err)
		}
		for index := range references {
			references[index].BlockID = block.ID
		}
		batch.validatedBaseReferences[block.ID] = references
	}
	for _, rawID := range input.Deletes {
		id, err := parseRequiredUUID(rawID, "delete Block ID")
		if err != nil {
			return Batch{}, err
		}
		batch.Deletes = append(batch.Deletes, id)
	}
	for _, move := range input.Moves {
		blockID, err := parseRequiredUUID(move.BlockID, "move Block ID")
		if err != nil {
			return Batch{}, err
		}
		parentID, err := parseOptionalUUID(move.ParentBlockID, "move parent Block ID")
		if err != nil {
			return Batch{}, err
		}
		batch.Reorders = append(batch.Reorders, Reorder{
			BlockID:       blockID,
			ParentID:      parentID,
			ContainerSlot: move.ContainerSlot,
			Position:      int(move.Position),
		})
	}
	for _, group := range input.LocaleGroups {
		converted := LocaleMutationGroup{Locale: group.Locale}
		for _, update := range group.Upserts {
			blockID, err := parseRequiredUUID(update.BlockID, "locale Block ID")
			if err != nil {
				return Batch{}, err
			}
			converted.Upserts = append(converted.Upserts, LocaleBlockUpdate{
				BlockID:       blockID,
				ExpectedKind:  update.ExpectedKind,
				LocalizedData: append(json.RawMessage(nil), update.LocalizedData...),
			})
		}
		for _, rawID := range group.Deletes {
			blockID, err := parseRequiredUUID(rawID, "locale delete Block ID")
			if err != nil {
				return Batch{}, err
			}
			converted.Deletes = append(converted.Deletes, blockID)
		}
		batch.LocaleGroups = append(batch.LocaleGroups, converted)
	}
	for _, rawID := range input.ContributorMemberIDs {
		memberID, err := parseRequiredUUID(rawID, "contributor Member ID")
		if err != nil {
			return Batch{}, err
		}
		batch.ContributorMemberIDs = append(batch.ContributorMemberIDs, memberID)
	}
	sort.Slice(batch.ContributorMemberIDs, func(i, j int) bool {
		return batch.ContributorMemberIDs[i].String() < batch.ContributorMemberIDs[j].String()
	})
	return batch, nil
}

func replaceFromStorage(documentID, expectedRevision uuid.UUID, rows []contentv1.ContentStorageRow) (ReplaceInput, error) {
	if documentID == uuid.Nil || expectedRevision == uuid.Nil {
		return ReplaceInput{}, invalidProtoMutation("replace document IDs", fmt.Errorf("document and expected revision must be UUIDs"))
	}
	result := ReplaceInput{DocumentID: documentID, ExpectedRevision: expectedRevision}
	overlays := make(map[string][]LocaleBlockUpdate)
	for _, row := range rows {
		block, err := baseBlockFromStorage(row)
		if err != nil {
			return ReplaceInput{}, err
		}
		result.Blocks = append(result.Blocks, block)
		for _, locale := range row.Locales {
			overlays[locale.Locale] = append(overlays[locale.Locale], LocaleBlockUpdate{
				BlockID:       block.ID,
				LocalizedData: append(json.RawMessage(nil), locale.LocalizedData...),
			})
		}
	}
	locales := make([]string, 0, len(overlays))
	for locale := range overlays {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		result.LocaleOverlays = append(result.LocaleOverlays, LocaleOverlay{
			Locale: locale,
			Blocks: overlays[locale],
		})
	}
	return result, nil
}

func snapshotToStorageRows(input Snapshot) []contentv1.ContentStorageRow {
	locales := make(map[uuid.UUID][]contentv1.ContentStorageLocale, len(input.Blocks))
	for _, overlay := range input.LocaleOverlays {
		for _, block := range overlay.Blocks {
			locales[block.BlockID] = append(locales[block.BlockID], contentv1.ContentStorageLocale{
				Locale:        overlay.Locale,
				LocalizedData: append([]byte(nil), block.LocalizedData...),
			})
		}
	}
	rows := make([]contentv1.ContentStorageRow, 0, len(input.Blocks))
	for _, block := range input.Blocks {
		parentID := ""
		if block.ParentID != nil {
			parentID = block.ParentID.String()
		}
		rowLocales := append([]contentv1.ContentStorageLocale(nil), locales[block.ID]...)
		sort.Slice(rowLocales, func(i, j int) bool { return rowLocales[i].Locale < rowLocales[j].Locale })
		rows = append(rows, contentv1.ContentStorageRow{
			BlockID:       block.ID.String(),
			ParentBlockID: parentID,
			ContainerSlot: block.ContainerSlot,
			Position:      int32(block.Position),
			Kind:          block.Kind,
			SharedData:    append([]byte(nil), block.SharedData...),
			Locales:       rowLocales,
		})
	}
	return rows
}

func baseBlockFromStorage(row contentv1.ContentStorageRow) (BaseBlock, error) {
	blockID, err := parseRequiredUUID(row.BlockID, "Block ID")
	if err != nil {
		return BaseBlock{}, err
	}
	parentID, err := parseOptionalUUID(row.ParentBlockID, "parent Block ID")
	if err != nil {
		return BaseBlock{}, err
	}
	return BaseBlock{
		ID:            blockID,
		ParentID:      parentID,
		ContainerSlot: row.ContainerSlot,
		Position:      int(row.Position),
		Kind:          row.Kind,
		SharedData:    append(json.RawMessage(nil), row.SharedData...),
	}, nil
}

func parseRequiredUUID(value, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, invalidProtoMutation(field, fmt.Errorf("must be a UUID"))
	}
	return id, nil
}

func parseOptionalUUID(value, field string) (*uuid.UUID, error) {
	if value == "" {
		return nil, nil
	}
	id, err := parseRequiredUUID(value, field)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func invalidProtoMutation(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrInvalidMutation, operation, err)
}
