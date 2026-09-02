//go:build integration

package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

type EditorMediaBlockType string

const (
	EditorMediaBlockTypeAudio      EditorMediaBlockType = "audio"
	EditorMediaBlockTypeVideo      EditorMediaBlockType = "video"
	EditorMediaBlockTypeImage      EditorMediaBlockType = "image"
	EditorMediaBlockTypeAttachment EditorMediaBlockType = "attachment"
)

type ManagedEditorMediaFixture struct {
	ManagedEditorEntity
	BlockID   string
	BlockType EditorMediaBlockType
	SectionID string
	SlotID    string
}

type ManagedEditorPendingUploadFixture struct {
	UploadID  string
	FileID    string
	AttemptID string
	FileName  string
	FileSize  int64
}

type ManagedImmersiveScenePageFixture struct {
	ManagedEditorEntity
	SectionID         string
	UnitID            string
	MeshSlotID        string
	TextureSlotID     string
	DarkTextureSlotID string
}

func (f ManagedEditorMediaFixture) EditURL(baseURL string) string {
	switch f.Name {
	case "post":
		return fmt.Sprintf("%s/posts/%s?edit=true&lang=en", baseURL, f.EntityID)
	case "page":
		return fmt.Sprintf("%s/admin/pages/%s?lang=en", baseURL, f.EntityID)
	case "work":
		return fmt.Sprintf("%s/admin/works/%s?lang=en", baseURL, f.EntityID)
	default:
		panic("unsupported editor entity: " + f.Name)
	}
}

func (f ManagedEditorMediaFixture) ViewURL(baseURL string) string {
	switch f.Name {
	case "post":
		return fmt.Sprintf("%s/posts/%s?lang=en", baseURL, f.EntityID)
	case "page":
		return fmt.Sprintf("%s/%s?lang=en", baseURL, f.EntityID)
	case "work":
		return fmt.Sprintf("%s/work/%s?lang=en", baseURL, f.EntityID)
	default:
		panic("unsupported editor entity: " + f.Name)
	}
}

func (f ManagedImmersiveScenePageFixture) EditURL(baseURL string) string {
	return fmt.Sprintf("%s/admin/pages/%s?lang=en", baseURL, f.EntityID)
}

func (f ManagedImmersiveScenePageFixture) ViewURL(baseURL string) string {
	return fmt.Sprintf("%s/%s?lang=en", baseURL, f.EntityID)
}

func (f ManagedImmersiveScenePageFixture) SlotIDs() []string {
	return []string{f.MeshSlotID, f.TextureSlotID, f.DarkTextureSlotID}
}

func CreateManagedEditorMediaFixtures(
	t *testing.T,
	db *gorm.DB,
	spiceDBClient *auth.SpiceDBClient,
	managerAccountIdentityID string,
	blockType EditorMediaBlockType,
) []ManagedEditorMediaFixture {
	post := CreateManagedEditorMediaFixture(
		t,
		db,
		spiceDBClient,
		managerAccountIdentityID,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		"en",
		blockType,
	)
	page := CreateManagedEditorMediaFixture(
		t,
		db,
		spiceDBClient,
		managerAccountIdentityID,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		"en",
		blockType,
	)
	work := CreateManagedEditorMediaFixture(
		t,
		db,
		spiceDBClient,
		managerAccountIdentityID,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		"en",
		blockType,
	)
	return []ManagedEditorMediaFixture{post, page, work}
}

func CreateManagedEditorMediaFixturesViaAPI(
	t *testing.T,
	backendURL string,
	db *gorm.DB,
	manager *OryUser,
	blockType EditorMediaBlockType,
) []ManagedEditorMediaFixture {
	post := CreateManagedEditorMediaFixtureViaAPI(
		t,
		backendURL,
		db,
		manager,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		"en",
		blockType,
	)
	page := CreateManagedEditorMediaFixtureViaAPI(
		t,
		backendURL,
		db,
		manager,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		"en",
		blockType,
	)
	work := CreateManagedEditorMediaFixtureViaAPI(
		t,
		backendURL,
		db,
		manager,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		"en",
		blockType,
	)
	return []ManagedEditorMediaFixture{post, page, work}
}

func CreateManagedEditorEntitiesViaAPI(
	t *testing.T,
	backendURL string,
	spiceDBClient *auth.SpiceDBClient,
	creator *OryUser,
	managerAccountIdentityID string,
) []ManagedEditorEntity {
	t.Helper()

	post := CreateManagedEditorEntityViaAPI(
		t,
		backendURL,
		creator,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		"ko",
	)
	maybeGrantResourceManager(t, spiceDBClient, post.Name, post.EntityID, managerAccountIdentityID)
	page := CreateManagedEditorEntityViaAPI(
		t,
		backendURL,
		creator,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		"en",
	)
	maybeGrantResourceManager(t, spiceDBClient, page.Name, page.EntityID, managerAccountIdentityID)
	work := CreateManagedEditorEntityViaAPI(
		t,
		backendURL,
		creator,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		"ja",
	)
	maybeGrantResourceManager(t, spiceDBClient, work.Name, work.EntityID, managerAccountIdentityID)
	return []ManagedEditorEntity{post, page, work}
}

func CreateManagedEditorMediaFixture(
	t *testing.T,
	db *gorm.DB,
	spiceDBClient *auth.SpiceDBClient,
	managerAccountIdentityID string,
	entityType managev1.TranscodeEntityType,
	sourceLocale string,
	blockType EditorMediaBlockType,
) ManagedEditorMediaFixture {
	entity := CreateManagedEditorEntity(t, db, spiceDBClient, managerAccountIdentityID, entityType, sourceLocale)
	return SeedManagedEditorMediaFixture(t, db, entity, blockType)
}

func CreateManagedEditorMediaFixtureViaAPI(
	t *testing.T,
	backendURL string,
	db *gorm.DB,
	manager *OryUser,
	entityType managev1.TranscodeEntityType,
	sourceLocale string,
	blockType EditorMediaBlockType,
) ManagedEditorMediaFixture {
	entity := CreateManagedEditorEntityViaAPI(t, backendURL, manager, entityType, sourceLocale)
	return SeedManagedEditorMediaFixture(t, db, entity, blockType)
}

func CreateManagedImmersiveScenePageFixtureViaAPI(
	t *testing.T,
	backendURL string,
	db *gorm.DB,
	manager *OryUser,
) ManagedImmersiveScenePageFixture {
	t.Helper()

	entity := CreateManagedEditorEntityViaAPI(
		t,
		backendURL,
		manager,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		"en",
	)
	return SeedManagedImmersiveScenePageFixture(t, db, entity)
}

func CreateManagedEditorEntityViaAPI(
	t *testing.T,
	backendURL string,
	manager *OryUser,
	entityType managev1.TranscodeEntityType,
	sourceLocale string,
) ManagedEditorEntity {
	client := &http.Client{Timeout: 30 * time.Second}

	switch entityType {
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST:
		postClient := managev1connect.NewPostServiceClient(client, backendURL)
		req := connect.NewRequest(&managev1.CreatePostRequest{
			Title:           "post-" + uuid.NewString(),
			CommentsEnabled: true,
		})
		ApplyAuthHeaders(req.Header(), manager)
		req.Header().Set("Accept-Language", sourceLocale)
		resp, err := postClient.CreatePost(context.Background(), req)
		require.NoError(t, err)
		return ManagedEditorEntity{
			Name:         "post",
			EntityID:     resp.Msg.Id,
			EntityType:   entityType,
			SourceLocale: sourceLocale,
		}
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE:
		pageClient := managev1connect.NewPageServiceClient(client, backendURL)
		showTitle := true
		req := connect.NewRequest(&managev1.CreatePageRequest{
			Title:     "page-" + uuid.NewString(),
			ShowTitle: &showTitle,
			Summary:   nil,
		})
		ApplyAuthHeaders(req.Header(), manager)
		req.Header().Set("Accept-Language", sourceLocale)
		resp, err := pageClient.CreatePage(context.Background(), req)
		require.NoError(t, err)
		return ManagedEditorEntity{
			Name:         "page",
			EntityID:     resp.Msg.Id,
			EntityType:   entityType,
			SourceLocale: sourceLocale,
		}
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK:
		workClient := managev1connect.NewWorkServiceClient(client, backendURL)
		now := time.Now().UTC()
		untilYear := int32(now.Year())
		untilMonth := int32(now.Month())
		isPresent := false
		req := connect.NewRequest(&managev1.CreateWorkRequest{
			Title:      "work-" + uuid.NewString(),
			Type:       managev1.WorkType_WORK_TYPE_MUSIC_PROJECT,
			Year:       int32(now.Year()),
			Month:      int32(now.Month()),
			UntilYear:  &untilYear,
			UntilMonth: &untilMonth,
			IsPresent:  &isPresent,
		})
		ApplyAuthHeaders(req.Header(), manager)
		req.Header().Set("Accept-Language", sourceLocale)
		resp, err := workClient.CreateWork(context.Background(), req)
		require.NoError(t, err)
		return ManagedEditorEntity{
			Name:         "work",
			EntityID:     resp.Msg.Id,
			EntityType:   entityType,
			SourceLocale: sourceLocale,
		}
	default:
		require.FailNow(t, fmt.Sprintf("unsupported managed editor entity type: %s", entityType.String()))
		return ManagedEditorEntity{}
	}
}

func SeedManagedEditorMediaFixture(
	t *testing.T,
	db *gorm.DB,
	entity ManagedEditorEntity,
	blockType EditorMediaBlockType,
) ManagedEditorMediaFixture {
	t.Helper()
	blockID := uuid.NewString()
	slotID := "file"
	sectionID := ""
	if entity.Name == "page" {
		sectionID = uuid.NewString()
	}
	seedTypedEditorMediaDocument(t, db, entity, blockID, sectionID, blockType)

	return ManagedEditorMediaFixture{
		ManagedEditorEntity: entity,
		BlockID:             blockID,
		BlockType:           blockType,
		SectionID:           sectionID,
		SlotID:              slotID,
	}
}

func SeedManagedImmersiveScenePageFixture(
	t *testing.T,
	db *gorm.DB,
	entity ManagedEditorEntity,
) ManagedImmersiveScenePageFixture {
	t.Helper()

	require.Equal(t, "page", entity.Name)

	sectionID := uuid.NewString()
	unitID := uuid.NewString()
	meshSlotID := fmt.Sprintf("immersive_scene:%s:mesh", unitID)
	textureSlotID := fmt.Sprintf("immersive_scene:%s:texture", unitID)
	darkTextureSlotID := fmt.Sprintf("immersive_scene:%s:dark_texture", unitID)
	seedTypedImmersivePageDocument(t, db, entity, sectionID, unitID)

	return ManagedImmersiveScenePageFixture{
		ManagedEditorEntity: entity,
		SectionID:           sectionID,
		UnitID:              unitID,
		MeshSlotID:          meshSlotID,
		TextureSlotID:       textureSlotID,
		DarkTextureSlotID:   darkTextureSlotID,
	}
}

func SeedManagedEditorPendingUploadFixture(
	t *testing.T,
	db *gorm.DB,
	fixture ManagedEditorMediaFixture,
	fileSize int64,
) ManagedEditorPendingUploadFixture {
	t.Helper()

	upload := NewIncompleteUploadFixture()
	pending := ManagedEditorPendingUploadFixture{
		UploadID:  upload.UploadID,
		FileID:    upload.FileID,
		AttemptID: uuid.NewString(),
		FileName:  "expired-upload-" + uuid.NewString() + ".bin",
		FileSize:  fileSize,
	}
	now := time.Now().UTC()

	uploadType := editorUploadTypeForBlockType(t, fixture.BlockType)
	chunkSize := int64(10 * 1024 * 1024)
	totalParts := int32((pending.FileSize + chunkSize - 1) / chunkSize)
	require.NoError(t, db.Exec(`
		INSERT INTO upload_session (
			upload_id,
			file_id,
			upload_type,
			file_name,
			file_size,
			file_last_modified,
			attempt_id,
			requested_mime,
			total_parts,
			chunk_size,
			status,
			last_activity_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'uploading', ?)
	`,
		pending.UploadID,
		pending.FileID,
		uploadType.String(),
		pending.FileName,
		pending.FileSize,
		now.UnixMilli(),
		pending.AttemptID,
		defaultMimeForEditorBlockType(fixture.BlockType),
		totalParts,
		chunkSize,
		now,
	).Error)

	return pending
}

type editorFixtureFileReuseAuthorizer struct{}

func (editorFixtureFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func seedTypedEditorMediaDocument(
	t *testing.T,
	db *gorm.DB,
	entity ManagedEditorEntity,
	blockID string,
	sectionID string,
	blockType EditorMediaBlockType,
) {
	t.Helper()
	fileID := seedEditorPlaceholderFile(t, db, defaultMimeForEditorBlockType(blockType))
	name := "editor-media-placeholder." + model.GetExtensionFromMime(defaultMimeForEditorBlockType(blockType))
	attachment := &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID}}
	fileBlock := &contentv1.RichTextBlock{
		Id: blockID,
		Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
			Attachment: attachment,
			Name:       &name,
		}}},
	}
	alt := "Editor media placeholder"
	caption := "Editor media fixture"
	fileLocale := &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
			Alt: &alt, Caption: &caption,
		}}},
	}

	switch entity.Name {
	case "post", "work":
		profile := contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST
		if entity.Name == "work" {
			profile = contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK
		}
		document := &contentv1.RichTextDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 profile,
			SourceLocale:            entity.SourceLocale,
			Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
				Block: fileBlock, Placement: &contentv1.ContentBlockPlacement{Index: 0},
			}}},
			LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
				Locale: entity.SourceLocale, Blocks: []*contentv1.RichTextBlockLocale{fileLocale},
			}},
		}
		seedTypedEditorDocument(t, db, entity, func(documentID, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			return contentblock.ReplaceFromRichTextProto(documentID, revision, document)
		})
	case "page":
		document := &contentv1.PageDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			SourceLocale:            entity.SourceLocale,
			Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
				Section: &contentv1.PageSection{Id: sectionID, Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
					Props:  &contentv1.RichTextSectionProps{},
					Blocks: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{Block: fileBlock, Placement: &contentv1.ContentBlockPlacement{Index: 0}}}},
				}}},
				Placement: &contentv1.PageSectionPlacement{Index: 0},
			}}},
			LocaleOverlays: []*contentv1.PageLocaleOverlay{{
				Locale: entity.SourceLocale,
				Sections: []*contentv1.PageSectionLocale{{
					SectionId: sectionID,
					Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
						Props:  &contentv1.RichTextSectionLocaleProps{},
						Blocks: &contentv1.RichTextLocaleOverlay{Locale: entity.SourceLocale, Blocks: []*contentv1.RichTextBlockLocale{fileLocale}},
					}},
				}},
			}},
		}
		seedTypedEditorDocument(t, db, entity, func(documentID, revision uuid.UUID) (contentblock.ReplaceInput, error) {
			return replaceFromPageDocumentForEditorFixture(documentID, revision, document)
		})
	default:
		require.FailNow(t, "unsupported editor entity kind for media fixture")
	}
}

func seedTypedImmersivePageDocument(t *testing.T, db *gorm.DB, entity ManagedEditorEntity, sectionID, unitID string) {
	t.Helper()
	meshID := seedEditorPlaceholderFile(t, db, "model/gltf-binary")
	textureID := seedEditorPlaceholderFile(t, db, "image/png")
	darkTextureID := seedEditorPlaceholderFile(t, db, "image/png")
	active := func(fileID string) *contentv1.FileAttachment {
		return &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID}}
	}
	mesh := contentv1.PageImmersiveUnitProps_MESH_SPHERE
	meshSource := contentv1.PageImmersiveUnitProps_MESH_SOURCE_FILE
	textureSource := contentv1.PageImmersiveUnitProps_TEXTURE_SOURCE_IMAGE
	darkTextureSource := contentv1.PageImmersiveUnitProps_DARK_TEXTURE_SOURCE_IMAGE
	title := "Runtime Upload Unit"
	text := "Uploads GLB and light/dark particle textures through the CMS editor."
	document := &contentv1.PageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		SourceLocale:            entity.SourceLocale,
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{{
			Section: &contentv1.PageSection{Id: sectionID, Value: &contentv1.PageSection_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSection{
				Props: &contentv1.ImmersiveSceneSectionProps{},
				Units: []*contentv1.PageImmersiveUnit{{Id: unitID, Props: &contentv1.PageImmersiveUnitProps{
					Mesh: &mesh, MeshSource: &meshSource, MeshFile: active(meshID),
					TextureSource: &textureSource, TextureFile: active(textureID),
					DarkTextureSource: &darkTextureSource, DarkTextureFile: active(darkTextureID),
				}}},
			}}},
			Placement: &contentv1.PageSectionPlacement{Index: 0},
		}}},
		LocaleOverlays: []*contentv1.PageLocaleOverlay{{
			Locale: entity.SourceLocale,
			Sections: []*contentv1.PageSectionLocale{{
				SectionId: sectionID,
				Value: &contentv1.PageSectionLocale_ImmersiveScene{ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{
					Props: &contentv1.ImmersiveSceneSectionLocaleProps{},
					Units: []*contentv1.PageImmersiveUnitLocale{{UnitId: unitID, Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &title, Text: &text}}},
				}},
			}},
		}},
	}
	seedTypedEditorDocument(t, db, entity, func(documentID, revision uuid.UUID) (contentblock.ReplaceInput, error) {
		return replaceFromPageDocumentForEditorFixture(documentID, revision, document)
	})
}

func replaceFromPageDocumentForEditorFixture(
	documentID uuid.UUID,
	revision uuid.UUID,
	document *contentv1.PageDocument,
) (contentblock.ReplaceInput, error) {
	var overlay *contentv1.PageLocaleOverlay
	if len(document.GetLocaleOverlays()) != 0 {
		overlay = document.GetLocaleOverlays()[0]
	}
	return contentblock.ReplaceFromLocalizedPageProtoWithUnavailableAttachments(
		documentID,
		revision,
		&contentv1.LocalizedPageDocument{
			BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
			Locale:                  document.GetSourceLocale(),
			Base:                    document.GetBase(),
			LocaleOverlay:           overlay,
		},
		nil,
	)
}

func seedTypedEditorDocument(
	t *testing.T,
	db *gorm.DB,
	entity ManagedEditorEntity,
	buildReplace func(uuid.UUID, uuid.UUID) (contentblock.ReplaceInput, error),
) {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(editorFixtureFileReuseAuthorizer{})
	require.NoError(t, err)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var root struct {
			ContentDocumentID *string `gorm:"column:content_document_id"`
		}
		if err := tx.Table(entity.Name).Select("content_document_id").Where("id = ?", entity.EntityID).Take(&root).Error; err != nil {
			return err
		}

		var snapshot contentblock.Snapshot
		if root.ContentDocumentID == nil || *root.ContentDocumentID == "" {
			created, err := store.CreateDocument(context.Background(), tx, contentblock.CreateInput{
				Profile: entity.Name, SourceLocale: entity.SourceLocale,
			})
			if err != nil {
				return err
			}
			snapshot = created
			if err := tx.Table(entity.Name).Where("id = ?", entity.EntityID).Update("content_document_id", created.Document.ID).Error; err != nil {
				return err
			}
		} else {
			documentID, err := uuid.Parse(*root.ContentDocumentID)
			if err != nil {
				return err
			}
			loaded, err := store.LoadSnapshotInTransaction(context.Background(), tx, documentID, entity.SourceLocale)
			if err != nil {
				return err
			}
			snapshot = loaded
		}

		replace, err := buildReplace(snapshot.Document.ID, snapshot.Document.Revision)
		if err != nil {
			return err
		}
		if _, err := store.ReplaceSnapshot(context.Background(), tx, replace, func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: entity.SourceLocale}, nil
		}); err != nil {
			return err
		}
		return tx.Table(entity.Name).Where("id = ?", entity.EntityID).
			Update("updated_at", time.Now().UTC()).Error
	}))
}

func seedEditorPlaceholderFile(t *testing.T, db *gorm.DB, mimeType string) string {
	t.Helper()
	fileID := uuid.NewString()
	require.NoError(t, db.Create(&model.File{
		ID: fileID, FileName: "editor-placeholder", MimeType: mimeType,
		FileSize: 1, Extension: model.GetExtensionFromMime(mimeType), SHA256: make([]byte, 32), CreatedAt: time.Now().UTC(),
	}).Error)
	return fileID
}

func editorUploadTypeForBlockType(t require.TestingT, blockType EditorMediaBlockType) managev1.UploadType {
	switch blockType {
	case EditorMediaBlockTypeAudio:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO
	case EditorMediaBlockTypeVideo:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO
	case EditorMediaBlockTypeImage:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE
	case EditorMediaBlockTypeAttachment:
		return managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT
	default:
		require.FailNow(t, fmt.Sprintf("unsupported editor media block type: %s", blockType))
		return managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED
	}
}

func defaultMimeForEditorBlockType(blockType EditorMediaBlockType) string {
	switch blockType {
	case EditorMediaBlockTypeAudio:
		return "audio/wav"
	case EditorMediaBlockTypeVideo:
		return "video/mp4"
	case EditorMediaBlockTypeImage:
		return "image/png"
	case EditorMediaBlockTypeAttachment:
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func ReadManagedEditorRichTextBlocks(
	t *testing.T,
	db *gorm.DB,
	fixture ManagedEditorMediaFixture,
) []structured.Fields {
	t.Helper()
	var rows []struct {
		ID            string `gorm:"column:id"`
		Kind          string `gorm:"column:kind"`
		SharedData    []byte `gorm:"column:shared_data"`
		LocalizedData []byte `gorm:"column:localized_data"`
	}
	require.NoError(t, db.Raw(`
		SELECT cb.id::text, cb.kind, cb.shared_data, cbl.localized_data
		FROM content_block cb
		JOIN `+fixture.Name+` owner ON owner.content_document_id = cb.document_id
		LEFT JOIN content_block_locale cbl ON cbl.block_id = cb.id AND cbl.locale = ?
		WHERE owner.id = ? AND cb.id = ?
		ORDER BY cb.id
	`, fixture.SourceLocale, fixture.EntityID, fixture.BlockID).Scan(&rows).Error)
	blocks := make([]structured.Fields, 0, len(rows))
	for _, row := range rows {
		props := structured.Fields{}
		require.NoError(t, json.Unmarshal(row.SharedData, &props))
		localized := structured.Fields{}
		if len(row.LocalizedData) > 0 {
			require.NoError(t, json.Unmarshal(row.LocalizedData, &localized))
		}
		blocks = append(blocks, structured.Fields{
			"id": row.ID, "type": row.Kind, "props": props, "locale": localized,
		})
	}
	return blocks
}
