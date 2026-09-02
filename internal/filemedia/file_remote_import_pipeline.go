package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

type preparedRemoteImport struct {
	opts               remoteFileImportOptions
	uploadType         managev1.UploadType
	config             *model.UploadConfig
	entityType         string
	entityID           string
	slotID             string
	projectionIdentity fileIngestProjectionIdentity
	identity           remoteImportOperationIdentity
}

func (r preparedRemoteImport) requiresAttachment() bool {
	return requiresDurableFileIngestAttachment(r.projectionIdentity)
}

func (s *FileService) importRemoteFile(ctx context.Context, opts remoteFileImportOptions) (*remoteFileImportResult, error) {
	request, err := s.prepareRemoteImport(ctx, opts)
	if err != nil {
		return nil, err
	}
	if request.identity.durable && !opts.operationLockHeld {
		return s.importRemoteFileWithLock(ctx, request)
	}

	emitter, err := s.newRemoteImportEmitter(ctx, request)
	if err != nil {
		return nil, err
	}
	fail := remoteImportFailureReporter(emitter)
	if restored, found, err := s.restoreRemoteImport(ctx, request, emitter, fail); err != nil || found {
		return restored, err
	}

	source, err := s.openRemoteImportSource(ctx, request, emitter, fail)
	if err != nil {
		return nil, err
	}
	defer source.close()
	stored, err := s.storeRemoteImportSource(ctx, request, source, emitter, fail)
	if err != nil {
		return nil, err
	}
	return s.finalizeRemoteImport(ctx, request, stored, emitter, fail)
}

func (s *FileService) prepareRemoteImport(ctx context.Context, opts remoteFileImportOptions) (preparedRemoteImport, error) {
	request := preparedRemoteImport{
		opts:       opts,
		uploadType: opts.uploadType,
		config:     s.getUploadConfig(opts.uploadType),
		entityType: strings.TrimSpace(opts.entityType),
		entityID:   strings.TrimSpace(opts.entityID),
		slotID:     strings.TrimSpace(opts.slotID),
	}
	if request.config == nil {
		return request, errs.InvalidArgument("upload_type", fmt.Sprintf("invalid upload type: %s", opts.uploadType.String()))
	}
	if (request.uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE ||
		isEditorFileIngestUploadType(request.uploadType)) && request.entityID != "" {
		return request, errs.InvalidArgument(
			"entity_id",
			"File library upload must omit document entity ID",
		)
	}
	resolvedSiteEntityID, err := resolveSiteUploadEntityID(
		s.legalRouteIdentity,
		request.uploadType,
		request.slotID,
	)
	if err != nil {
		return request, err
	}
	if resolvedSiteEntityID != "" {
		request.entityID = resolvedSiteEntityID
	}
	if err := s.checkRemoteImportPermission(ctx, request); err != nil {
		return request, err
	}
	request.projectionIdentity, err = normalizeFileIngestProjectionIdentity(
		request.uploadType,
		opts.transcodeEntityType,
		request.slotID,
		opts.expectedCurrentFileID,
	)
	if err != nil {
		return request, err
	}
	request.identity, err = resolveRemoteImportOperationIdentity(
		opts,
		request.entityID,
		request.slotID,
		request.projectionIdentity,
	)
	if err != nil {
		return request, errs.InvalidArgument("correlation_id", err.Error())
	}
	if opts.operationIdentity != nil {
		request.identity = *opts.operationIdentity
	}
	return request, nil
}

func (s *FileService) checkRemoteImportPermission(ctx context.Context, request preparedRemoteImport) error {
	if !request.opts.checkPermission {
		return nil
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return errs.AuthenticationRequired()
	}
	return s.checkEntityPermission(ctx, request.uploadType, request.entityType, request.entityID, user.MemberID.String())
}

func (s *FileService) importRemoteFileWithLock(ctx context.Context, request preparedRemoteImport) (*remoteFileImportResult, error) {
	nextOpts := request.opts
	nextOpts.correlationID = request.identity.attemptID
	nextOpts.operationIdentity = &request.identity
	nextOpts.operationLockHeld = true
	var result *remoteFileImportResult
	err := withRemoteImportAdvisoryLock(ctx, s.db, request.identity.fileID, func(connection *gorm.DB) error {
		lockedService := *s
		lockedService.db = connection
		var operationErr error
		result, operationErr = lockedService.importRemoteFile(ctx, nextOpts)
		return operationErr
	})
	return result, err
}

func (s *FileService) newRemoteImportEmitter(ctx context.Context, request preparedRemoteImport) (*fileIngestEventEmitter, error) {
	if !request.opts.emitLifecycle {
		if request.identity.durable && request.requiresAttachment() {
			return nil, errs.InternalMsg("durable remote file ingest emitter is required")
		}
		return nil, nil
	}
	correlationID := strings.TrimSpace(request.opts.correlationID)
	if request.identity.durable {
		correlationID = request.identity.attemptID
	}
	emitter := newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_REMOTE_URL,
		request.opts.transcodeEntityType,
		request.entityID,
		correlationID,
		request.identity.fileID,
		0,
	)
	if emitter != nil {
		emitter.setRequestIdentity(request.uploadType, request.slotID, request.identity.attemptID)
		if err := emitter.setProjectionIdentity(request.projectionIdentity); err != nil {
			return nil, errs.Internal(err)
		}
	}
	if request.identity.durable && request.requiresAttachment() && emitter == nil {
		return nil, errs.InternalMsg("durable remote file ingest emitter is required")
	}
	return emitter, nil
}

func remoteImportFailureReporter(emitter *fileIngestEventEmitter) func(error) error {
	return func(err error) error {
		if emitter != nil {
			emitter.publishFailed(err.Error(), max(emitter.lastProgress, 0), nil)
		}
		return err
	}
}

func (s *FileService) restoreRemoteImport(
	ctx context.Context,
	request preparedRemoteImport,
	emitter *fileIngestEventEmitter,
	fail func(error) error,
) (*remoteFileImportResult, bool, error) {
	if !request.identity.durable {
		return nil, false, nil
	}
	restored, found, err := s.restoreCompletedRemoteImport(
		ctx,
		request.opts,
		request.entityID,
		request.slotID,
		request.identity,
		request.projectionIdentity,
	)
	if err != nil {
		return nil, false, errs.Internal(fmt.Errorf("validate existing durable remote import: %w", err))
	}
	if !found {
		return nil, false, nil
	}
	if emitter != nil {
		emitter.totalBytes = restored.size
	}
	asset, err := s.promoteDedicatedPublicUploadAsset(ctx, request.uploadType, request.slotID, restored.fileID)
	if err != nil {
		return nil, false, fail(err)
	}
	asset, err = s.promoteFileScopedImageIfNeeded(
		ctx,
		request.uploadType,
		request.slotID,
		restored.fileID,
		restored.mimeType,
		asset,
	)
	if err != nil {
		return nil, false, fail(err)
	}
	restored.asset = asset
	objectKey, err := mediaauth.MediaObjectKey(restored.fileID, mediaExtension(&restored.mimeType))
	if err != nil {
		return nil, false, errs.Internal(fmt.Errorf("build existing durable remote import target: %w", err))
	}
	if emitter != nil {
		emitter.setTarget(remoteImportMediaTarget(restored.fileID, objectKey, restored.mimeType))
	}
	if request.requiresAttachment() {
		if err := emitter.publishAttachedConfirmed(restored.fileName, restored.mimeType, restored.size); err != nil {
			return nil, false, errs.Internal(fmt.Errorf("failed to reconfirm durable file attachment: %w", err))
		}
	} else if err := s.finishConfirmedRemoteImport(ctx, request.opts, restored); err != nil {
		return nil, false, err
	}
	return restored, true, nil
}

type remoteImportSource struct {
	parsedURL       *url.URL
	response        *http.Response
	client          *remoteImportHTTPClient
	prefix          []byte
	body            *io.LimitedReader
	detectedMime    string
	sourceMaxSize   int64
	shouldNormalize bool
}

func (s *remoteImportSource) close() {
	s.response.Body.Close()
	s.client.CloseIdleConnections()
}

func (s *FileService) openRemoteImportSource(
	ctx context.Context,
	request preparedRemoteImport,
	emitter *fileIngestEventEmitter,
	fail func(error) error,
) (*remoteImportSource, error) {
	parsedURL, err := url.Parse(request.opts.sourceURL)
	if err != nil {
		return nil, fail(errs.InvalidArgument("url", "invalid URL"))
	}
	resolver := s.remoteImportResolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dial := s.remoteImportDialer
	if dial == nil {
		dial = new(net.Dialer).DialContext
	}
	target, err := validateRemoteImportURL(ctx, resolver, parsedURL)
	if err != nil {
		return nil, fail(errs.InvalidArgument("url", err.Error()))
	}
	client := newRemoteImportHTTPClient(ctx, resolver, dial, s.remoteImportBaseTransport)
	requestContext := context.WithValue(ctx, remoteImportTargetContextKey{}, target)
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodGet, request.opts.sourceURL, nil)
	if err != nil {
		client.CloseIdleConnections()
		return nil, fail(errs.Internal(fmt.Errorf("failed to create request: %w", err)))
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		client.CloseIdleConnections()
		return nil, fail(errs.Internal(fmt.Errorf("failed to download: %w", err)))
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		client.CloseIdleConnections()
		return nil, fail(errs.Internal(fmt.Errorf("download failed with status: %d", response.StatusCode)))
	}
	source, err := validateRemoteImportSource(response, parsedURL, request, emitter)
	if err != nil {
		response.Body.Close()
		client.CloseIdleConnections()
		return nil, fail(err)
	}
	source.client = client
	return source, nil
}

func validateRemoteImportSource(
	response *http.Response,
	parsedURL *url.URL,
	request preparedRemoteImport,
	emitter *fileIngestEventEmitter,
) (*remoteImportSource, error) {
	selectionMaxSize := getRemoteImportSelectionMaxSize(request.uploadType, request.config.MaxSize)
	if response.ContentLength > selectionMaxSize {
		return nil, errs.InvalidArgument("url", fmt.Sprintf("remote file exceeds maximum size %d", selectionMaxSize))
	}
	if emitter != nil {
		emitter.totalBytes = response.ContentLength
	}
	prefix, body, err := readRemoteImportPrefix(response.Body, selectionMaxSize+1)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to read response prefix: %w", err))
	}
	if len(prefix) == 0 {
		return nil, errs.InvalidArgument("url", "remote file is empty")
	}
	allowed := buildAllowedMimeSet(request.config.PermittedMimeTypes)
	detectedMime := detectCanonicalMime(prefix, allowed)
	if detectedMime == "" {
		return nil, errs.InvalidArgument("mime_type", undeterminedMimeMessage(request.config.PermittedMimeTypes))
	}
	if _, ok := allowed[detectedMime]; !ok {
		return nil, errs.InvalidArgument("mime_type", unsupportedMimeMessage(detectedMime, request.config.PermittedMimeTypes))
	}
	if detectedMime == "model/gltf-binary" && response.ContentLength > 0 {
		if err := validateGLBUploadSize(prefix, response.ContentLength); err != nil {
			return nil, err
		}
	}
	if err := validateSiteEmailLogoMime(request.uploadType, request.slotID, detectedMime); err != nil {
		return nil, err
	}
	normalize := shouldNormalizeManagedRemoteImport(request.uploadType, detectedMime)
	sourceMaxSize := request.config.MaxSize
	if normalize {
		sourceMaxSize = selectionMaxSize
	}
	if response.ContentLength > sourceMaxSize {
		return nil, errs.InvalidArgument("url", fmt.Sprintf("remote file exceeds maximum size %d", sourceMaxSize))
	}
	return &remoteImportSource{
		parsedURL:       parsedURL,
		response:        response,
		prefix:          prefix,
		body:            body,
		detectedMime:    detectedMime,
		sourceMaxSize:   sourceMaxSize,
		shouldNormalize: normalize,
	}, nil
}

type storedRemoteImport struct {
	fileName        string
	mimeType        string
	objectKey       string
	size            int64
	digest          []byte
	downloadedBytes int64
}

func (s *FileService) storeRemoteImportSource(
	ctx context.Context,
	request preparedRemoteImport,
	source *remoteImportSource,
	emitter *fileIngestEventEmitter,
	fail func(error) error,
) (*storedRemoteImport, error) {
	fileID := request.identity.fileID
	fileName := canonicalRemoteImportFilename(remoteImportFileName(source.parsedURL), fileID, source.detectedMime)
	hasher := sha256.New()
	body := &countingReader{
		reader: io.TeeReader(io.MultiReader(bytes.NewReader(source.prefix), source.body), hasher),
		afterRead: func(total int64) {
			if emitter != nil {
				emitter.publishDownloadProgress(total)
			}
		},
	}
	var stored *storedRemoteImport
	var err error
	if source.shouldNormalize {
		stored, err = s.storeNormalizedRemoteImport(ctx, request, source, body, fileName)
	} else {
		stored, err = s.storeRawRemoteImport(ctx, request, source, body, hasher, fileName)
	}
	if err != nil {
		return nil, fail(err)
	}
	stored.downloadedBytes = body.count
	if emitter != nil {
		emitter.setTarget(remoteImportMediaTarget(fileID, stored.objectKey, stored.mimeType))
	}
	return stored, nil
}

func (s *FileService) storeNormalizedRemoteImport(
	ctx context.Context,
	request preparedRemoteImport,
	source *remoteImportSource,
	body *countingReader,
	fileName string,
) (*storedRemoteImport, error) {
	fileID := request.identity.fileID
	sourceKey, err := s.buildManagedFileKey(fileID, source.detectedMime)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to build source file key: %w", err))
	}
	if err := s.uploadRemoteImportObject(ctx, sourceKey, body, source.detectedMime); err != nil {
		s.deleteS3ObjectBestEffort(ctx, sourceKey)
		return nil, errs.Internal(err)
	}
	if body.count > source.sourceMaxSize {
		s.deleteS3ObjectBestEffort(ctx, sourceKey)
		return nil, errs.InvalidArgument("url", fmt.Sprintf("remote file exceeds maximum size %d", source.sourceMaxSize))
	}
	normalizedBody, mimeType, err := s.normalizeManagedRemoteImport(ctx, fileID, source.detectedMime)
	s.deleteS3ObjectBestEffort(ctx, sourceKey)
	if err != nil {
		return nil, errs.InvalidArgument("url", err.Error())
	}
	objectKey, err := s.buildManagedFileKey(fileID, mimeType)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to build file key: %w", err))
	}
	size := int64(len(normalizedBody))
	if size > request.config.MaxSize {
		return nil, errs.InvalidArgument("url", fmt.Sprintf("remote file exceeds maximum size %d", request.config.MaxSize))
	}
	if size < request.config.MinSize {
		return nil, errs.InvalidArgument("url", fmt.Sprintf("remote file is below minimum size %d", request.config.MinSize))
	}
	if err := s.uploadRemoteImportObject(ctx, objectKey, bytes.NewReader(normalizedBody), mimeType); err != nil {
		s.deleteS3ObjectBestEffort(ctx, objectKey)
		return nil, errs.Internal(err)
	}
	digest := sha256.Sum256(normalizedBody)
	return &storedRemoteImport{
		fileName:  canonicalRemoteImportFilename(fileName, fileID, mimeType),
		mimeType:  mimeType,
		objectKey: objectKey,
		size:      size,
		digest:    append([]byte(nil), digest[:]...),
	}, nil
}

func (s *FileService) storeRawRemoteImport(
	ctx context.Context,
	request preparedRemoteImport,
	source *remoteImportSource,
	body *countingReader,
	hasher hash.Hash,
	fileName string,
) (*storedRemoteImport, error) {
	objectKey, err := s.buildManagedFileKey(request.identity.fileID, source.detectedMime)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to build file key: %w", err))
	}
	if err := s.uploadRemoteImportObject(ctx, objectKey, body, source.detectedMime); err != nil {
		return nil, errs.Internal(err)
	}
	if body.count > source.sourceMaxSize {
		s.deleteS3ObjectBestEffort(ctx, objectKey)
		return nil, errs.InvalidArgument("url", fmt.Sprintf("remote file exceeds maximum size %d", source.sourceMaxSize))
	}
	if body.count < request.config.MinSize {
		s.deleteS3ObjectBestEffort(ctx, objectKey)
		return nil, errs.InvalidArgument("url", fmt.Sprintf("remote file is below minimum size %d", request.config.MinSize))
	}
	if source.detectedMime == "model/gltf-binary" {
		if err := validateGLBUploadSize(source.prefix, body.count); err != nil {
			s.deleteS3ObjectBestEffort(ctx, objectKey)
			return nil, err
		}
	}
	return &storedRemoteImport{
		fileName:  fileName,
		mimeType:  source.detectedMime,
		objectKey: objectKey,
		size:      body.count,
		digest:    hasher.Sum(nil),
	}, nil
}

func remoteImportMediaTarget(fileID, objectKey, mimeType string) *commonv1.MediaObjectTarget {
	return &commonv1.MediaObjectTarget{
		FileId:    fileID,
		ObjectKey: objectKey,
		Extension: mediaExtension(&mimeType),
		MimeType:  mimeType,
	}
}

func (s *FileService) finalizeRemoteImport(
	ctx context.Context,
	request preparedRemoteImport,
	stored *storedRemoteImport,
	emitter *fileIngestEventEmitter,
	fail func(error) error,
) (*remoteFileImportResult, error) {
	fileID := request.identity.fileID
	file := remoteImportFileRecord(request, stored)
	inline, err := buildExpiringMediaFileRef(
		s.mediaDomain,
		s.mediaSecret,
		fileID,
		mediaExtension(&stored.mimeType),
		stored.mimeType,
		nil,
		mediaauth.PurposeInline,
		mediaauth.InlineTTL,
	)
	if err != nil {
		s.deleteS3ObjectBestEffort(ctx, stored.objectKey)
		return nil, fail(errs.Internal(fmt.Errorf("failed to sign imported inline delivery: %w", err)))
	}
	download, err := buildExpiringMediaFileRef(
		s.mediaDomain,
		s.mediaSecret,
		fileID,
		mediaExtension(&stored.mimeType),
		stored.mimeType,
		&stored.fileName,
		mediaauth.PurposeDownload,
		s.effectiveDownloadTTL(),
	)
	if err != nil {
		s.deleteS3ObjectBestEffort(ctx, stored.objectKey)
		return nil, fail(errs.Internal(fmt.Errorf("failed to sign imported file delivery: %w", err)))
	}
	if err := s.createOrRestoreVerifiedRemoteImportRecord(
		ctx,
		file,
		request.entityID,
		request.slotID,
		request.identity,
		request.projectionIdentity,
		request.opts,
		stored.objectKey,
	); err != nil {
		return nil, fail(errs.Internal(fmt.Errorf("failed to create file record: %w", err)))
	}
	asset, err := s.promoteDedicatedPublicUploadAsset(ctx, request.uploadType, request.slotID, fileID)
	if err != nil {
		return nil, fail(err)
	}
	asset, err = s.promoteFileScopedImageIfNeeded(
		ctx,
		request.uploadType,
		request.slotID,
		fileID,
		stored.mimeType,
		asset,
	)
	if err != nil {
		return nil, fail(err)
	}
	if emitter != nil {
		emitter.publishFinalized(100, &stored.downloadedBytes)
		if request.requiresAttachment() {
			if err := emitter.publishAttachedConfirmed(stored.fileName, stored.mimeType, stored.size); err != nil {
				return nil, errs.Internal(fmt.Errorf("failed to confirm durable file attachment: %w", err))
			}
		}
	}
	result := &remoteFileImportResult{
		fileID:                fileID,
		fileName:              stored.fileName,
		mimeType:              stored.mimeType,
		size:                  stored.size,
		inline:                inline,
		download:              download,
		slotID:                request.slotID,
		attemptID:             request.identity.attemptID,
		expectedCurrentFileID: request.projectionIdentity.expectedCurrentFileID,
		asset:                 asset,
	}
	if !request.requiresAttachment() {
		if err := s.finishConfirmedRemoteImport(ctx, request.opts, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func remoteImportFileRecord(request preparedRemoteImport, stored *storedRemoteImport) structured.Fields {
	file := structured.Fields{
		"id":        request.identity.fileID,
		"mime_type": stored.mimeType,
		"file_size": stored.size,
		"extension": mediaExtension(&stored.mimeType),
		"sha256":    stored.digest,
	}
	if stored.fileName != "" {
		file["file_name"] = stored.fileName
	}
	if request.slotID != "" {
		file["ingest_slot_id"] = request.slotID
	}
	if request.identity.attemptID != "" {
		file["ingest_attempt_id"] = request.identity.attemptID
	}
	return file
}
