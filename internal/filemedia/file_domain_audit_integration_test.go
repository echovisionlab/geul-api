//go:build integration

package filemedia

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestFileManagerDomainAuditIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "File audit admin")
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := releaseAuditContext(t, identityID, memberID)
	writer := apitelemetry.NewDurableWriter(db)
	service := &FileService{
		db: db, spiceDB: spiceDB, cdnDomain: "https://cdn.example.test", mediaSecret: "file-audit",
		downloadTTL: time.Hour, asyncPublisher: &capturingAsyncPublisher{}, auditWriter: writer,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}

	root, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Library"}))
	require.NoError(t, err)
	child, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{ParentId: &root.Msg.Folder.Id, Name: "Child"}))
	require.NoError(t, err)
	_, err = service.RenameFileFolder(ctx, connect.NewRequest(&managev1.RenameFileFolderRequest{FolderId: child.Msg.Folder.Id, Name: "Renamed"}))
	require.NoError(t, err)
	_, err = service.MoveFileFolder(ctx, connect.NewRequest(&managev1.MoveFileFolderRequest{FolderId: child.Msg.Folder.Id}))
	require.NoError(t, err)
	// Repeating root move is a semantic no-op.
	_, err = service.MoveFileFolder(ctx, connect.NewRequest(&managev1.MoveFileFolderRequest{FolderId: child.Msg.Folder.Id}))
	require.NoError(t, err)

	fileID := seedFileManagerFile(t, db, root.Msg.Folder.Id, memberID, "source", "jpg", "image/jpeg")
	_, err = service.RenameFile(ctx, connect.NewRequest(&managev1.RenameFileRequest{FileId: fileID, FileName: "renamed"}))
	require.NoError(t, err)
	_, err = service.MoveFiles(ctx, connect.NewRequest(&managev1.MoveFilesRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)
	_, err = service.MoveFiles(ctx, connect.NewRequest(&managev1.MoveFilesRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)
	_, err = service.DeleteFiles(ctx, connect.NewRequest(&managev1.DeleteFilesRequest{FileIds: []string{fileID}}))
	require.NoError(t, err)

	var rows []postSeriesAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE target_type IN ('file','file_folder') ORDER BY occurred_at, audit_id`).Scan(&rows).Error)
	require.Len(t, rows, 7)
	for _, row := range rows {
		require.Equal(t, memberID, row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), row.RequestID)
		require.NotContains(t, string(row.Attributes), "renamed")
		require.NotContains(t, string(row.Attributes), "https://")
	}
	require.Equal(t, []string{"file_folder.created", "file_folder.created", "file_folder.updated", "file_folder.updated", "file.updated", "file.updated", "file.deleted"}, []string{rows[0].Action, rows[1].Action, rows[2].Action, rows[3].Action, rows[4].Action, rows[5].Action, rows[6].Action})

	failing := &FileService{
		db: db, spiceDB: spiceDB, auditWriter: fileFailingAuditAppender{},
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}
	_, err = failing.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "must rollback"}))
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.FileFolder{}).Where("name = ?", "must rollback").Count(&count).Error)
	require.Zero(t, count)
}

func TestVerifiedFileIngestAndRecursiveFolderDeletionDomainAuditIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "File ingest audit admin")
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := releaseAuditContext(t, identityID, memberID)
	writer := apitelemetry.NewDurableWriter(db)
	publisher := &capturingAsyncPublisher{}
	service := &FileService{
		db: db, spiceDB: spiceDB, asyncPublisher: publisher, auditWriter: writer,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}

	createdID := integrationTestUUID()
	err := service.createVerifiedFileIngestRecord(ctx, structured.Fields{
		"id": createdID, "file_name": "verified", "mime_type": "image/jpeg", "file_size": int64(1024), "extension": "jpg", "sha256": make([]byte, 32), "ingest_attempt_id": integrationTestUUID(),
	}, managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, "", trustedSystemFileIngestAuthority{})
	require.NoError(t, err)
	var createdRows []postSeriesAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE target_id = ?`, createdID).Scan(&createdRows).Error)
	require.Len(t, createdRows, 1)
	require.Equal(t, "file.created", createdRows[0].Action)
	require.Equal(t, "file", createdRows[0].TargetType)
	require.Equal(t, memberID, createdRows[0].ActorMemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), createdRows[0].RequestID)
	require.JSONEq(t, `{}`, string(createdRows[0].Attributes))

	failingIngestID := integrationTestUUID()
	failingIngest := &FileService{db: db, spiceDB: spiceDB, auditWriter: fileFailingAuditAppender{}}
	err = failingIngest.createVerifiedFileIngestRecord(ctx, structured.Fields{
		"id": failingIngestID, "file_name": "must-rollback", "mime_type": "image/jpeg", "file_size": int64(1024), "extension": "jpg", "sha256": make([]byte, 32), "ingest_attempt_id": integrationTestUUID(),
	}, managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, "", trustedSystemFileIngestAuthority{})
	require.Error(t, err)
	var ingestCount int64
	require.NoError(t, db.Table("file").Where("id = ?", failingIngestID).Count(&ingestCount).Error)
	require.Zero(t, ingestCount)

	root, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Delete root"}))
	require.NoError(t, err)
	child, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{ParentId: &root.Msg.Folder.Id, Name: "Delete child"}))
	require.NoError(t, err)
	rootFileID := seedFileManagerFile(t, db, root.Msg.Folder.Id, memberID, "root-file", "jpg", "image/jpeg")
	childFileID := seedFileManagerFile(t, db, child.Msg.Folder.Id, memberID, "child-file", "jpg", "image/jpeg")
	_, err = service.DeleteFileFolder(ctx, connect.NewRequest(&managev1.DeleteFileFolderRequest{FolderId: root.Msg.Folder.Id}))
	require.NoError(t, err)
	require.Len(t, publisher.messages, 2, "folder deletion publishes independent file commands only")
	var pending int64
	require.NoError(t, db.Table("file").Where("id IN ? AND delete_requested_at IS NOT NULL", []string{rootFileID, childFileID}).Count(&pending).Error)
	require.EqualValues(t, 2, pending)
	var deleteRows []postSeriesAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE action IN ('file.deleted', 'file_folder.deleted') AND target_id IN ? ORDER BY target_type, target_id`, []string{root.Msg.Folder.Id, child.Msg.Folder.Id, rootFileID, childFileID}).Scan(&deleteRows).Error)
	require.Len(t, deleteRows, 4)
	for _, row := range deleteRows {
		require.Equal(t, memberID, row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), row.RequestID)
		require.JSONEq(t, `{}`, string(row.Attributes))
	}

	rollbackRoot, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Rollback root"}))
	require.NoError(t, err)
	rollbackChild, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{ParentId: &rollbackRoot.Msg.Folder.Id, Name: "Rollback child"}))
	require.NoError(t, err)
	rollbackFileID := seedFileManagerFile(t, db, rollbackChild.Msg.Folder.Id, memberID, "rollback-file", "jpg", "image/jpeg")
	failingDelete := &FileService{
		db: db, spiceDB: spiceDB, asyncPublisher: &capturingAsyncPublisher{}, auditWriter: fileFailingAuditAppender{},
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}
	_, err = failingDelete.DeleteFileFolder(ctx, connect.NewRequest(&managev1.DeleteFileFolderRequest{FolderId: rollbackRoot.Msg.Folder.Id}))
	require.Error(t, err)
	var folderCount int64
	require.NoError(t, db.Table("file_folder").Where("id IN ?", []string{rollbackRoot.Msg.Folder.Id, rollbackChild.Msg.Folder.Id}).Count(&folderCount).Error)
	require.EqualValues(t, 2, folderCount)
	var pendingRollback int64
	require.NoError(t, db.Table("file").Where("id = ? AND delete_requested_at IS NOT NULL", rollbackFileID).Count(&pendingRollback).Error)
	require.Zero(t, pendingRollback)
}

type fileFailingAuditAppender struct{}

func (fileFailingAuditAppender) AppendDomainAuditInTransaction(_ context.Context, _ *gorm.DB, _ sharedtelemetry.AuditRecord) error {
	return errFileAuditAppend
}

var errFileAuditAppend = fmt.Errorf("injected file audit append failure")
