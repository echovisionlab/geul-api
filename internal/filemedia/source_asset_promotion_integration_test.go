//go:build integration

package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestSourceAssetPromotionStreamsLargeObjectThroughMinIOMultipartIntegration(t *testing.T) {
	stack := testutil.SetupSharedDirectMediaRuntimeStack(t)
	client := runtimeS3Client(t, stack)
	body := bytes.Repeat([]byte{0x5a}, 16*1024*1024+1)
	digest := sha256.Sum256(body)
	file := model.File{
		ID:        uuid.NewString(),
		FileName:  "large-source",
		MimeType:  "image/webp",
		FileSize:  int64(len(body)),
		Extension: "webp",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, stack.DB.Create(&file).Error)
	sourceKey, err := mediaauth.MediaObjectKey(file.ID, file.Extension)
	require.NoError(t, err)
	_, err = client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket:        aws.String(stack.S3MediaBucket),
		Key:           aws.String(sourceKey),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(file.FileSize),
		ContentType:   aws.String(file.MimeType),
	})
	require.NoError(t, err)
	assetObjectKey := ""
	t.Cleanup(func() {
		if assetObjectKey != "" {
			_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
				Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(assetObjectKey),
			})
		}
		_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(sourceKey),
		})
		_ = stack.DB.Where("source_file_id = ?", file.ID).Delete(&model.PublicAsset{}).Error
		_ = stack.DB.Where("id = ?", file.ID).Delete(&model.File{}).Error
	})

	service := &FileService{
		db: stack.DB, s3Client: client, s3Bucket: stack.S3MediaBucket,
		cdnDomain: stack.CDNURL,
	}
	ref, err := service.promoteSourceFileToPublicAsset(t.Context(), file.ID, "image")
	require.NoError(t, err)
	require.Equal(t, digest[:], ref.GetSha256())

	var asset model.PublicAsset
	require.NoError(t, stack.DB.Where("id = ?", ref.GetAssetId()).Take(&asset).Error)
	assetObjectKey = asset.ObjectKey
	head, err := client.HeadObject(t.Context(), &s3.HeadObjectInput{
		Bucket: aws.String(stack.S3MediaBucket),
		Key:    aws.String(asset.ObjectKey),
	})
	require.NoError(t, err)
	require.Equal(t, file.FileSize, aws.ToInt64(head.ContentLength))
	require.Regexp(t, `-[2-9][0-9]*$`, strings.Trim(aws.ToString(head.ETag), `"`))

	stored, err := client.GetObject(t.Context(), &s3.GetObjectInput{
		Bucket: aws.String(stack.S3MediaBucket),
		Key:    aws.String(asset.ObjectKey),
	})
	require.NoError(t, err)
	actualDigest := sha256.New()
	actualSize, readErr := io.Copy(actualDigest, stored.Body)
	closeErr := stored.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equal(t, file.FileSize, actualSize)
	require.Equal(t, digest[:], actualDigest.Sum(nil))
}

func TestSourceAssetPromotionStorageRunsOutsideDatabaseTransactionIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	file, store, service := seedSourceAssetPromotionIntegrationFixture(t, db)
	started, release := store.blockTransfers()
	result := make(chan error, 1)
	go func() {
		_, err := service.promoteSourceFileToPublicAsset(context.Background(), file.ID, "image")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("promotion did not reach object storage transfer")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT 1 FROM file WHERE id = ? FOR UPDATE", file.ID).Error; err != nil {
			return err
		}
		return tx.Exec("SELECT 1 FROM public_asset WHERE source_file_id = ? FOR UPDATE", file.ID).Error
	}))
	close(release)
	require.NoError(t, <-result)
}

func TestSourceAssetPromotionConcurrentReplicasConvergeOnOneReadyAssetIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	file, store, service := seedSourceAssetPromotionIntegrationFixture(t, db)
	_ = store

	const replicas = 8
	refs := make([]string, replicas)
	errs := make([]error, replicas)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			ref, err := service.promoteSourceFileToPublicAsset(context.Background(), file.ID, "image")
			errs[index] = err
			if ref != nil {
				refs[index] = ref.GetAssetId()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	for _, assetID := range refs {
		require.NotEmpty(t, assetID)
		require.Equal(t, refs[0], assetID)
	}

	var assets []model.PublicAsset
	require.NoError(t, db.Where("source_file_id = ? AND status <> ?", file.ID, model.PublicAssetStatusDeleted).Find(&assets).Error)
	require.Len(t, assets, 1)
	require.Equal(t, model.PublicAssetStatusReady, assets[0].Status)
	require.Empty(t, store.deletedKeys())
}

func TestSourceAssetPromotionCompletionSerializesAfterFileDeletionIntentIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	fileID := uuid.NewString()
	seedFileDeleteLifecycleFile(t, db, fileID, "source.webp", "image/webp", "webp")
	assetID := uuid.NewString()
	objectKey, err := mediaauth.AssetObjectKey(assetID, "webp")
	require.NoError(t, err)
	now := time.Now().UTC()
	asset := model.PublicAsset{
		ID: assetID, SourceFileID: &fileID, Kind: "image", ObjectKey: objectKey,
		Extension: "webp", MimeType: "image/webp", Disposition: "inline",
		Status: model.PublicAssetStatusAllocated, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&asset).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DELETE FROM public_asset WHERE id = ?", assetID).Error)
		require.NoError(t, db.Exec("DELETE FROM file WHERE id = ?", fileID).Error)
	})

	deleteTx := db.WithContext(t.Context()).Begin()
	require.NoError(t, deleteTx.Error)
	var locked model.File
	require.NoError(t, deleteTx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "delete_requested_at").Where("id = ?", fileID).Take(&locked).Error)
	require.NoError(t, deleteTx.Model(&model.File{}).Where("id = ?", fileID).
		Update("delete_requested_at", now).Error)

	digest := sha256.Sum256([]byte("verified public source"))
	completionDone := make(chan error, 1)
	go func() {
		completionDone <- (&FileService{db: db}).completeSourceAssetPromotion(
			context.Background(), fileID, assetID, 22, digest[:],
		)
	}()
	select {
	case err := <-completionDone:
		t.Fatalf("completion crossed the held File lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	require.NoError(t, deleteTx.Commit().Error)
	select {
	case err := <-completionDone:
		require.ErrorContains(t, err, "file is pending deletion")
	case <-time.After(3 * time.Second):
		t.Fatal("completion did not resume after deletion intent committed")
	}

	var stored model.PublicAsset
	require.NoError(t, db.Where("id = ?", assetID).Take(&stored).Error)
	require.Equal(t, model.PublicAssetStatusAllocated, stored.Status)
}

func seedSourceAssetPromotionIntegrationFixture(
	t *testing.T,
	db *gorm.DB,
) (model.File, *sourceAssetPromotionS3Fixture, *FileService) {
	t.Helper()
	body := []byte("concurrent verified public source")
	file := model.File{
		ID: uuid.NewString(), FileName: "source", MimeType: "image/webp", FileSize: int64(len(body)),
		Extension: "webp", CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&file).Error)
	t.Cleanup(func() {
		_ = db.Where("source_file_id = ?", file.ID).Delete(&model.PublicAsset{}).Error
		_ = db.Where("id = ?", file.ID).Delete(&model.File{}).Error
	})
	store := newSourceAssetPromotionS3Fixture(t, file, body)
	service := &FileService{db: db, s3Client: store.client, s3Bucket: "media-bucket", cdnDomain: "https://cdn.example.com"}
	return file, store, service
}
