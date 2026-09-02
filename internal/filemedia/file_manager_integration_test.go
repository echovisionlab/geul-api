//go:build integration

package filemedia

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestFileManagerCatalogUsageAndDeletionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	memberID := auth.GetUser(ctx).MemberID.String()
	publisher := &capturingAsyncPublisher{}
	service := &FileService{
		db: db, cdnDomain: "https://cdn.example.test", mediaSecret: "file-manager-integration-secret",
		downloadTTL: time.Hour, asyncPublisher: publisher, spiceDB: spiceDB,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}

	root, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Library"}))
	require.NoError(t, err)
	require.Equal(t, "Library", root.Msg.Folder.GetName())
	child, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{
		ParentId: &root.Msg.Folder.Id, Name: "Images",
	}))
	require.NoError(t, err)

	require.NoError(t, db.SavePoint("file_manager_cycle_check").Error)
	_, err = service.MoveFileFolder(ctx, connect.NewRequest(&managev1.MoveFileFolderRequest{
		FolderId: root.Msg.Folder.Id, ParentId: &child.Msg.Folder.Id,
	}))
	require.Error(t, err)
	require.NoError(t, db.RollbackTo("file_manager_cycle_check").Error)

	fileID := seedFileManagerFile(t, db, child.Msg.Folder.Id, memberID, "cover", "jpg", "image/jpeg")
	postID, blockID, postSlug := seedFileManagerPostBlockUsage(t, db, fileID, "Exact usage post")
	secondBlockID := uuid.NewString()
	releaseDocumentID := uuid.NewString()
	releaseID := uuid.NewString()
	releaseSlug := "file-manager-release-" + releaseID
	trackID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (
			id, document_id, parent_block_id, container_slot, position, kind, shared_data
		) SELECT ?, content_document_id, NULL, 'root', 1, 'file', '{}'::jsonb FROM post WHERE id = ?
	`, secondBlockID, postID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'file', 'active', ?)
	`, secondBlockID, fileID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?, 'compact', ?)
	`, releaseDocumentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO release (id, slug, type, content_document_id)
		VALUES (?, ?, 'RELEASE_TYPE_ALBUM', ?)
	`, releaseID, releaseSlug, releaseDocumentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO track (id, release_id, track_number, title, audio_original_file_id)
		VALUES (?, ?, 1, 'Exact usage track', ?)
	`, trackID, releaseID, fileID).Error)

	listed, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		FolderId: &child.Msg.Folder.Id,
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, listed.Msg.Total)
	require.Len(t, listed.Msg.Items, 1)
	require.Equal(t, "cover", listed.Msg.Items[0].GetFile().GetFileName())
	require.Equal(t, "jpg", listed.Msg.Items[0].GetFile().GetExtension())
	require.EqualValues(t, 3, listed.Msg.Items[0].GetFile().GetUsageCount())
	require.Equal(t, memberID, listed.Msg.Items[0].GetFile().GetUploadedByMember().GetId())
	usages, err := service.ListFileUsages(ctx, connect.NewRequest(&managev1.ListFileUsagesRequest{FileId: fileID}))
	require.NoError(t, err)
	require.Len(t, usages.Msg.Usages, 3)
	usageByBlockID := make(map[string]*managev1.FileUsage)
	var trackUsage *managev1.FileUsage
	for _, usage := range usages.Msg.Usages {
		if usage.GetDomain() == managev1.FileUsageDomain_FILE_USAGE_DOMAIN_TRACK {
			trackUsage = usage
			continue
		}
		usageByBlockID[usage.GetBlockId()] = usage
	}
	require.Len(t, usageByBlockID, 2)
	for _, exactBlockID := range []string{blockID, secondBlockID} {
		usage := usageByBlockID[exactBlockID]
		require.NotNil(t, usage)
		require.Equal(t, "Exact usage post", usage.GetTitle())
		require.Equal(t, "file", usage.GetReferencePath())
		require.Equal(t, "file", usage.GetBlockType())
		require.Equal(t, "/posts/"+postSlug, usage.GetLink())
	}
	require.NotNil(t, trackUsage)
	require.Equal(t, trackID, trackUsage.GetEntityId())
	require.Equal(t, "Exact usage track", trackUsage.GetTitle())
	require.Equal(t, "audio_original", trackUsage.GetReferencePath())
	require.Equal(t, "/releases/"+releaseSlug, trackUsage.GetLink())

	impact, err := service.GetFileDeletionImpact(ctx, connect.NewRequest(&managev1.GetFileDeletionImpactRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)
	require.Len(t, impact.Msg.Impacts, 1)
	require.EqualValues(t, 3, impact.Msg.Impacts[0].GetTotalUsageCount())
	require.Equal(t, managev1.FileUsageDomain_FILE_USAGE_DOMAIN_POST, impact.Msg.Impacts[0].GetFirstUsages()[0].GetDomain())

	rejected, err := service.DeleteFiles(ctx, connect.NewRequest(&managev1.DeleteFilesRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)
	require.Empty(t, rejected.Msg.AcceptedFileIds)
	require.Len(t, rejected.Msg.RejectedFiles, 1)

	require.NoError(t, db.Exec(`DELETE FROM content_block_attachment WHERE block_id IN ? AND file_id = ?`, []string{blockID, secondBlockID}, fileID).Error)
	require.NoError(t, db.Exec(`UPDATE track SET audio_original_file_id = NULL WHERE id = ?`, trackID).Error)
	accepted, err := service.DeleteFiles(ctx, connect.NewRequest(&managev1.DeleteFilesRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)
	require.Equal(t, []string{fileID}, accepted.Msg.AcceptedFileIds)
	require.NotEmpty(t, publisher.messages)
}

func TestFileManagerSiteWideSearchIncludesFoldersPathsAndPaginationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminCtx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	memberID := auth.GetUser(adminCtx).MemberID.String()
	service := &FileService{
		db: db, cdnDomain: "https://cdn.example.test", mediaSecret: "file-search-integration-secret",
		downloadTTL: time.Hour, spiceDB: spiceDB,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}

	root, err := service.CreateFileFolder(adminCtx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Library"}))
	require.NoError(t, err)
	covers, err := service.CreateFileFolder(adminCtx, connect.NewRequest(&managev1.CreateFileFolderRequest{
		ParentId: &root.Msg.Folder.Id, Name: "Needle Covers",
	}))
	require.NoError(t, err)
	fileID := seedFileManagerFile(t, db, covers.Msg.Folder.Id, memberID, "Needle Hero", "jpg", "image/jpeg")
	needleQuery := "nEeDlE"
	ctx := adminCtx

	first, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		Query: &needleQuery, PageSize: 1,
	}))
	require.NoError(t, err)
	require.EqualValues(t, 2, first.Msg.Total)
	require.Len(t, first.Msg.Items, 1)
	require.Equal(t, managev1.FileManagerItemType_FILE_MANAGER_ITEM_TYPE_FOLDER, first.Msg.Items[0].GetType())
	require.Equal(t, []string{"Library", "Needle Covers"}, []string{
		first.Msg.Items[0].GetFolderPath()[0].GetName(), first.Msg.Items[0].GetFolderPath()[1].GetName(),
	})
	require.NotEmpty(t, first.Msg.GetNextPageToken())

	second, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		Query: &needleQuery, PageSize: 1, PageToken: first.Msg.NextPageToken,
	}))
	require.NoError(t, err)
	require.EqualValues(t, 2, second.Msg.Total)
	require.Len(t, second.Msg.Items, 1)
	require.Equal(t, fileID, second.Msg.Items[0].GetFile().GetId())
	require.Equal(t, []string{"Library", "Needle Covers"}, []string{
		second.Msg.Items[0].GetFolderPath()[0].GetName(), second.Msg.Items[0].GetFolderPath()[1].GetName(),
	})
	require.Empty(t, second.Msg.GetNextPageToken())

	fullNameQuery := "NEEDLE HERO.JPG"
	fullName, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		Query: &fullNameQuery,
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, fullName.Msg.Total)
	require.Equal(t, fileID, fullName.Msg.Items[0].GetFile().GetId())

	browse, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		FolderId: &covers.Msg.Folder.Id,
	}))
	require.NoError(t, err)
	require.Empty(t, browse.Msg.Items[0].GetFolderPath())

	otherRoot, err := service.CreateFileFolder(adminCtx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Other"}))
	require.NoError(t, err)
	seedFileManagerFile(t, db, otherRoot.Msg.Folder.Id, memberID, "Needle Outside", "jpg", "image/jpeg")

	withinLibrary, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		FolderId: &root.Msg.Folder.Id, Query: &needleQuery,
	}))
	require.NoError(t, err)
	require.EqualValues(t, 2, withinLibrary.Msg.Total)
	require.Len(t, withinLibrary.Msg.Items, 2)

	withinCovers, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		FolderId: &covers.Msg.Folder.Id, Query: &needleQuery,
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, withinCovers.Msg.Total)
	require.Equal(t, fileID, withinCovers.Msg.Items[0].GetFile().GetId())
}

func TestFileManagerGetFileIncludesGeneratedOutputsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	memberID := auth.GetUser(ctx).MemberID.String()
	now := time.Now().UTC()
	service := &FileService{
		db: db, cdnDomain: "https://cdn.example.test", mediaSecret: "generated-output-secret",
		downloadTTL: time.Hour, spiceDB: spiceDB,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}

	folder, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Generated"}))
	require.NoError(t, err)
	sourceFileID := seedFileManagerFile(t, db, folder.Msg.Folder.Id, memberID, "source", "glb", "model/gltf-binary")
	outputFileID := seedFileManagerFile(t, db, folder.Msg.Folder.Id, memberID, "optimized", "glb", "model/gltf-binary")

	asset, _, err := mediaasset.NewLifecycle(db, service.cdnDomain).AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &sourceFileID, Kind: "thumbnail", Extension: "webp", MimeType: "image/webp",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	digest := sha256.Sum256([]byte("generated thumbnail"))
	_, err = mediaasset.NewLifecycle(db, service.cdnDomain).CompletePublicAsset(ctx, &commonv1.AssetWriteResult{
		AssetId: asset.ID, FileSize: int64(len("generated thumbnail")), Sha256: digest[:],
	})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.FileDerivative{
		ID: uuid.NewString(), FileID: sourceFileID,
		Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL, AssetID: &asset.ID,
		CreatedAt: now,
	}).Error)
	candidateID := uuid.NewString()
	require.NoError(t, db.Create(&model.MeshOptimizationCandidate{
		ID: candidateID, SourceFileID: sourceFileID, OutputObjectID: outputFileID, OutputFileID: &outputFileID,
		TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco,
		PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1,
		CacheKey:        sourceFileID + ":50:DRACO:file-manager-generated-output",
		Status:          model.MeshOptimizationCandidateStatusReady, CreatedAt: now, UpdatedAt: now,
	}).Error)

	response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: sourceFileID}))
	require.NoError(t, err)
	require.Len(t, response.Msg.GeneratedOutputs, 2)
	byType := make(map[managev1.FileDerivativeType]*managev1.FileGeneratedOutput, len(response.Msg.GeneratedOutputs))
	for _, output := range response.Msg.GeneratedOutputs {
		byType[output.GetType()] = output
	}
	thumbnail := byType[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL]
	require.NotNil(t, thumbnail)
	require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY, thumbnail.GetStatus())
	require.Equal(t, "https://cdn.example.test/asset/"+asset.ID+"/thumbnail.webp", thumbnail.GetDelivery().GetAsset().GetUrl())
	optimized := byType[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_OPTIMIZED_MESH]
	require.NotNil(t, optimized)
	require.Equal(t, candidateID, optimized.GetId())
	require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY, optimized.GetStatus())
	require.Equal(t, outputFileID, optimized.GetDelivery().GetFileId())
	require.True(t, strings.Contains(optimized.GetDelivery().GetInline().GetUrl(), "/"+outputFileID+".glb"))
}

func TestFileManagerFolderDeletionIsAllOrNothingIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	memberID := auth.GetUser(ctx).MemberID.String()
	publisher := &capturingAsyncPublisher{}
	service := &FileService{
		db: db, cdnDomain: "https://cdn.example.test", mediaSecret: "folder-delete-integration-secret",
		downloadTTL: time.Hour, asyncPublisher: publisher, spiceDB: spiceDB,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}

	root, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Tree"}))
	require.NoError(t, err)
	child, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{
		ParentId: &root.Msg.Folder.Id, Name: "Child",
	}))
	require.NoError(t, err)
	unusedFileID := seedFileManagerFile(t, db, root.Msg.Folder.Id, memberID, "unused", "jpg", "image/jpeg")
	usedFileID := seedFileManagerFile(t, db, child.Msg.Folder.Id, memberID, "used", "jpg", "image/jpeg")
	_, blockID, _ := seedFileManagerPostBlockUsage(t, db, usedFileID, "Folder deletion usage")

	_, err = service.DeleteFileFolder(ctx, connect.NewRequest(&managev1.DeleteFileFolderRequest{FolderId: root.Msg.Folder.Id}))
	require.Error(t, err)
	var folderCount int64
	require.NoError(t, db.Table("file_folder").Where("id IN ?", []string{root.Msg.Folder.Id, child.Msg.Folder.Id}).Count(&folderCount).Error)
	require.EqualValues(t, 2, folderCount)
	var pendingCount int64
	require.NoError(t, db.Table("file").Where("id IN ? AND delete_requested_at IS NOT NULL", []string{unusedFileID, usedFileID}).Count(&pendingCount).Error)
	require.Zero(t, pendingCount)

	require.NoError(t, db.Exec(`DELETE FROM content_block_attachment WHERE block_id = ? AND file_id = ?`, blockID, usedFileID).Error)
	deleted, err := service.DeleteFileFolder(ctx, connect.NewRequest(&managev1.DeleteFileFolderRequest{FolderId: root.Msg.Folder.Id}))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{unusedFileID, usedFileID}, deleted.Msg.AcceptedFileIds)
	require.NoError(t, db.Table("file_folder").Where("id IN ?", []string{root.Msg.Folder.Id, child.Msg.Folder.Id}).Count(&folderCount).Error)
	require.Zero(t, folderCount)
	require.NoError(t, db.Table("file").Where("id IN ? AND folder_id IS NULL AND delete_requested_at IS NOT NULL", []string{unusedFileID, usedFileID}).Count(&pendingCount).Error)
	require.EqualValues(t, 2, pendingCount)
	require.Len(t, publisher.messages, 2)
}

func TestFileManagerListsEverySiteSettingsUsageSlotIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	memberID := auth.GetUser(ctx).MemberID.String()
	service := &FileService{
		db: db, spiceDB: spiceDB,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}

	folder, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Settings"}))
	require.NoError(t, err)
	fileID := seedFileManagerFile(t, db, folder.Msg.Folder.Id, memberID, "branding", "png", "image/png")
	require.NoError(t, db.Exec(`UPDATE site_settings SET
		logo_light_file_id = ?, logo_dark_file_id = ?, logo_email_file_id = ?, favicon_file_id = ?,
		site_og_background_file_id = ?, privacy_og_background_file_id = ?, terms_og_background_file_id = ?
		WHERE id = 1`, fileID, fileID, fileID, fileID, fileID, fileID, fileID).Error)
	require.NoError(t, db.Exec(`INSERT INTO site_setting_loader_file (site_setting_id, file_id, position) VALUES (1, ?, 0)`, fileID).Error)

	response, err := service.ListFileUsages(ctx, connect.NewRequest(&managev1.ListFileUsagesRequest{FileId: fileID}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Usages, 8)

	slots := make([]string, 0, len(response.Msg.Usages))
	for _, usage := range response.Msg.Usages {
		require.Equal(t, managev1.FileUsageDomain_FILE_USAGE_DOMAIN_SITE_SETTINGS, usage.GetDomain())
		slots = append(slots, usage.GetReferencePath())
	}
	require.ElementsMatch(t, []string{
		"logo_light", "logo_dark", "logo_email", "favicon", "loader",
		"site_og_background", "privacy_og_background", "terms_og_background",
	}, slots)

	impact, err := service.GetFileDeletionImpact(ctx, connect.NewRequest(&managev1.GetFileDeletionImpactRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)
	require.Len(t, impact.Msg.Impacts, 1)
	require.EqualValues(t, 8, impact.Msg.Impacts[0].GetTotalUsageCount())
	require.Len(t, impact.Msg.Impacts[0].GetFirstUsages(), fileDeletionImpactPreviewSize)
	require.True(t, impact.Msg.Impacts[0].GetHasMoreUsages())
	require.Len(t, impact.Msg.Impacts[0].GetDomainCounts(), 1)
	require.EqualValues(t, 8, impact.Msg.Impacts[0].GetDomainCounts()[0].GetCount())

	rejected, err := service.DeleteFiles(ctx, connect.NewRequest(&managev1.DeleteFilesRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)
	require.Empty(t, rejected.Msg.AcceptedFileIds)
	require.Len(t, rejected.Msg.RejectedFiles, 1)
	require.EqualValues(t, 8, rejected.Msg.RejectedFiles[0].GetTotalUsageCount())
	require.Len(t, rejected.Msg.RejectedFiles[0].GetFirstUsages(), fileDeletionImpactPreviewSize)
}

func TestFileManagerFiltersAuthorUsageProjectionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminCtx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	principal := auth.GetUser(adminCtx)
	identityID := principal.IdentityID.String()
	memberID := principal.MemberID.String()
	identitySubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	service := &FileService{
		db: db, spiceDB: spiceDB, cdnDomain: "https://cdn.example.test",
		mediaSecret: "file-manager-author-secret", downloadTTL: time.Hour,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}
	folder, err := service.CreateFileFolder(adminCtx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Author Files"}))
	require.NoError(t, err)
	fileID := seedFileManagerFile(t, db, folder.Msg.Folder.Id, memberID, "shared", "png", "image/png")
	postID, _, _ := seedFileManagerPostBlockUsage(t, db, fileID, "Author usage")
	require.NoError(t, db.Exec(`UPDATE site_settings SET favicon_file_id = ? WHERE id = 1`, fileID).Error)
	identityActor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	grantAuthor, err := policyv1.Post.TouchAuthor(postID, identityActor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), grantAuthor)
	require.NoError(t, err)
	seedFileDeliveryContentPolicy(t, spiceDB, "post", postID)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), identitySubject, policyv1.Role.Author())
	require.NoError(t, err)

	authorCtx := adminCtx

	listed, err := service.ListFileManagerItems(authorCtx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{FolderId: &folder.Msg.Folder.Id}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Items, 1)
	require.EqualValues(t, 1, listed.Msg.Items[0].GetFile().GetUsageCount())

	file, err := service.GetFile(authorCtx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
	require.NoError(t, err)
	require.EqualValues(t, 1, file.Msg.File.GetUsageCount())
	require.Len(t, file.Msg.DomainUsageSummary, 1)
	require.Equal(t, managev1.FileUsageDomain_FILE_USAGE_DOMAIN_POST, file.Msg.DomainUsageSummary[0].GetDomain())

	usages, err := service.ListFileUsages(authorCtx, connect.NewRequest(&managev1.ListFileUsagesRequest{FileId: fileID}))
	require.NoError(t, err)
	require.EqualValues(t, 1, usages.Msg.Total)
	require.Len(t, usages.Msg.Usages, 1)
	require.Equal(t, managev1.FileUsageDomain_FILE_USAGE_DOMAIN_POST, usages.Msg.Usages[0].GetDomain())
	revokeAuthor, err := policyv1.Post.DeleteAuthor(postID, identityActor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), revokeAuthor)
	require.NoError(t, err)
	hidden, err := service.GetFile(authorCtx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
	require.NoError(t, err)
	require.Zero(t, hidden.Msg.File.GetUsageCount())
	require.Empty(t, hidden.Msg.DomainUsageSummary)
}

func seedFileManagerFile(t *testing.T, db *gorm.DB, folderID, memberID, name, extension, mimeType string) string {
	t.Helper()
	fileID := uuid.NewString()
	digest := sha256.Sum256([]byte(fileID))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, extension, mime_type, file_size, sha256, folder_id, uploaded_by_member_id) VALUES (?, ?, ?, ?, 1024, ?, ?, ?)`, fileID, name, extension, mimeType, digest[:], folderID, memberID).Error)
	return fileID
}

func seedFileManagerPostBlockUsage(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	title string,
) (postID string, blockID string, slug string) {
	t.Helper()
	documentID := uuid.NewString()
	postID = uuid.NewString()
	blockID = uuid.NewString()
	slug = "file-manager-" + postID
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?, 'post', ?)
	`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO post (id, slug, content_document_id, source_locale) VALUES (?, ?, ?, 'en')`,
		postID, slug, documentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (
			id, document_id, parent_block_id, container_slot, position, kind, shared_data
		) VALUES (?, ?, NULL, 'root', 0, 'file', '{}'::jsonb)
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'file', 'active', ?)
	`, blockID, fileID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post_translation (
		entity_id, locale, title
	) VALUES (?::uuid, 'en', ?)`, postID, title).Error)
	return postID, blockID, slug
}
