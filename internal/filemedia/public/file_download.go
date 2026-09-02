package public

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"unicode"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

func (s *FileService) AuthorizeDownload(
	ctx context.Context,
	req *connect.Request[openv1.AuthorizeDownloadRequest],
) (*connect.Response[openv1.AuthorizeDownloadResponse], error) {
	entityID := strings.TrimSpace(req.Msg.EntityId)
	if entityID == "" {
		return nil, errs.Required("entity_id")
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return nil, errs.InvalidArgument("entity_id", "must be a valid UUID")
	}
	if req.Msg.EntityType == openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_RELEASE {
		return s.authorizeReleaseTrackDownload(ctx, entityID, req.Msg.GetTrackId())
	}
	selector := req.Msg.GetContentBlock()
	if selector == nil {
		return nil, errs.Required("content_block")
	}
	blockID := strings.TrimSpace(selector.GetBlockId())
	if _, err := uuid.Parse(blockID); err != nil {
		return nil, errs.InvalidArgument("selector.block_id", "must be a valid UUID")
	}
	referencePath := strings.TrimSpace(selector.GetReferencePath())
	if referencePath == "" || len(referencePath) > 512 || strings.IndexFunc(referencePath, unicode.IsControl) >= 0 {
		return nil, errs.InvalidArgument("selector.reference_path", "must be non-empty, control-free, and at most 512 bytes")
	}
	var evaluatedSource mediaasset.FileDownloadSource
	var evaluatedStatus string
	var evaluatedMediaAccess resolvedMediaAccess
	var evaluatedAccess *openv1.FileDownloadAccess
	var evaluatedFingerprint string
	var evaluatedViewerFingerprint string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, ownerStatus, found, resolveErr := s.resolveContentBlockDownloadSource(
			ctx,
			tx,
			req.Msg.EntityType,
			entityID,
			blockID,
			referencePath,
			false,
		)
		if resolveErr != nil {
			return resolveErr
		}
		if !found {
			evaluatedAccess = unavailableFileDownloadAccess()
			return nil
		}
		mediaAccess, allowed, accessErr := s.resolveContentOwnerDownloadAccess(
			ctx,
			req.Msg.EntityType,
			entityID,
			ownerStatus,
		)
		if accessErr != nil {
			return accessErr
		}
		if !allowed {
			evaluatedAccess = unavailableFileDownloadAccess()
			return nil
		}
		access, accessErr := s.evaluateReferencedFileDownload(ctx, tx, source)
		if accessErr != nil {
			return accessErr
		}
		fingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, map[string]mediaasset.FileDownloadSource{source.PolicyKey: source}, s.segments)
		if fingerprintErr != nil {
			return errs.Internal(fingerprintErr)
		}
		viewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, map[string]mediaasset.FileDownloadSource{source.PolicyKey: source}, auth.GetUser(ctx))
		if viewerErr != nil {
			return errs.Internal(viewerErr)
		}
		evaluatedSource, evaluatedStatus, evaluatedMediaAccess = source, ownerStatus, mediaAccess
		evaluatedAccess, evaluatedFingerprint = access, fingerprints[source.PolicyKey]
		evaluatedViewerFingerprint = viewerFingerprint
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if evaluatedSource.PolicyKey == "" {
		return connect.NewResponse(&openv1.AuthorizeDownloadResponse{Access: evaluatedAccess}), nil
	}
	var response *connect.Response[openv1.AuthorizeDownloadResponse]
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		evaluatedSources := map[string]mediaasset.FileDownloadSource{evaluatedSource.PolicyKey: evaluatedSource}
		if lockErr := mediaasset.LockFileDownloadPolicySegments(ctx, tx, evaluatedSources); lockErr != nil {
			return errs.Internal(lockErr)
		}
		ownerStatus, ownerFound, ownerErr := s.lockContentDownloadOwner(ctx, tx, req.Msg.EntityType, entityID)
		if ownerErr != nil {
			return ownerErr
		}
		if !ownerFound || ownerStatus != evaluatedStatus {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		draftPrincipalActive, principalErr := lockDirectDraftDownloadPrincipal(ctx, tx, evaluatedMediaAccess)
		if principalErr != nil {
			return errs.Internal(principalErr)
		}
		if !draftPrincipalActive {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		viewerActive, lockErr := mediaasset.LockFileDownloadViewerFacts(ctx, tx, evaluatedSources, auth.GetUser(ctx))
		if lockErr != nil {
			return errs.Internal(lockErr)
		}
		if fileDownloadDecisionDependsOnActiveViewer(evaluatedSource, evaluatedAccess) && !viewerActive {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		current, ownerStatus, found, resolveErr := s.resolveContentBlockDownloadSource(
			ctx, tx, req.Msg.EntityType, entityID, blockID, referencePath, true,
		)
		if resolveErr != nil {
			return resolveErr
		}
		if !found || ownerStatus != evaluatedStatus {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		fingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, map[string]mediaasset.FileDownloadSource{current.PolicyKey: current}, s.segments)
		if fingerprintErr != nil {
			return errs.Internal(fingerprintErr)
		}
		viewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, map[string]mediaasset.FileDownloadSource{current.PolicyKey: current}, auth.GetUser(ctx))
		if viewerErr != nil {
			return errs.Internal(viewerErr)
		}
		if current.PolicyKey != evaluatedSource.PolicyKey || fingerprints[current.PolicyKey] != evaluatedFingerprint ||
			(fileDownloadDecisionDependsOnActiveViewer(evaluatedSource, evaluatedAccess) && viewerFingerprint != evaluatedViewerFingerprint) {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		response, resolveErr = s.signReferencedFileDownload(current, evaluatedMediaAccess, evaluatedAccess)
		return resolveErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *FileService) authorizeReleaseTrackDownload(
	ctx context.Context,
	releaseID string,
	trackID string,
) (*connect.Response[openv1.AuthorizeDownloadResponse], error) {
	trackID = strings.TrimSpace(trackID)
	if _, err := uuid.Parse(trackID); err != nil {
		return nil, errs.InvalidArgument("track_id", "must be a valid UUID")
	}
	var evaluatedSource mediaasset.FileDownloadSource
	var evaluatedStatus string
	var evaluatedMediaAccess resolvedMediaAccess
	var evaluatedAccess *openv1.FileDownloadAccess
	var evaluatedFingerprint string
	var evaluatedViewerFingerprint string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, ownerStatus, found, loadErr := s.resolveReleaseTrackDownloadSource(ctx, tx, releaseID, trackID, false)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			evaluatedAccess = unavailableFileDownloadAccess()
			return nil
		}
		mediaAccess := resolvedMediaAccess{}
		if ownerStatus != managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String() {
			allowed, viewErr := hasDraftResourceView(ctx, s.spiceDB, policyv1.Release.View, releaseID)
			if viewErr != nil {
				return errs.Internal(viewErr)
			}
			if !allowed {
				evaluatedAccess = unavailableFileDownloadAccess()
				return nil
			}
			mediaAccess.directDraft = true
			if user := auth.GetUser(ctx); user != nil {
				mediaAccess.directDraftIdentityID = user.IdentityID.String()
				mediaAccess.directDraftMemberID = user.MemberID.String()
			}
		}
		access, evaluateErr := s.evaluateReferencedFileDownload(ctx, tx, source)
		if evaluateErr != nil {
			return evaluateErr
		}
		fingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, map[string]mediaasset.FileDownloadSource{source.PolicyKey: source}, s.segments)
		if fingerprintErr != nil {
			return errs.Internal(fingerprintErr)
		}
		viewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, map[string]mediaasset.FileDownloadSource{source.PolicyKey: source}, auth.GetUser(ctx))
		if viewerErr != nil {
			return errs.Internal(viewerErr)
		}
		evaluatedSource, evaluatedStatus, evaluatedMediaAccess = source, ownerStatus, mediaAccess
		evaluatedAccess, evaluatedFingerprint = access, fingerprints[source.PolicyKey]
		evaluatedViewerFingerprint = viewerFingerprint
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if evaluatedSource.PolicyKey == "" {
		return connect.NewResponse(&openv1.AuthorizeDownloadResponse{Access: evaluatedAccess}), nil
	}
	var response *connect.Response[openv1.AuthorizeDownloadResponse]
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		evaluatedSources := map[string]mediaasset.FileDownloadSource{evaluatedSource.PolicyKey: evaluatedSource}
		if lockErr := mediaasset.LockFileDownloadPolicySegments(ctx, tx, evaluatedSources); lockErr != nil {
			return errs.Internal(lockErr)
		}
		ownerStatus, ownerFound, ownerErr := s.lockReleaseDownloadOwner(ctx, tx, releaseID)
		if ownerErr != nil {
			return ownerErr
		}
		if !ownerFound || ownerStatus != evaluatedStatus {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		draftPrincipalActive, principalErr := lockDirectDraftDownloadPrincipal(ctx, tx, evaluatedMediaAccess)
		if principalErr != nil {
			return errs.Internal(principalErr)
		}
		if !draftPrincipalActive {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		viewerActive, lockErr := mediaasset.LockFileDownloadViewerFacts(ctx, tx, evaluatedSources, auth.GetUser(ctx))
		if lockErr != nil {
			return errs.Internal(lockErr)
		}
		if fileDownloadDecisionDependsOnActiveViewer(evaluatedSource, evaluatedAccess) && !viewerActive {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		current, ownerStatus, found, loadErr := s.resolveReleaseTrackDownloadSource(ctx, tx, releaseID, trackID, true)
		if loadErr != nil {
			return loadErr
		}
		if !found || ownerStatus != evaluatedStatus {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		fingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, map[string]mediaasset.FileDownloadSource{current.PolicyKey: current}, s.segments)
		if fingerprintErr != nil {
			return errs.Internal(fingerprintErr)
		}
		viewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, map[string]mediaasset.FileDownloadSource{current.PolicyKey: current}, auth.GetUser(ctx))
		if viewerErr != nil {
			return errs.Internal(viewerErr)
		}
		if fingerprints[current.PolicyKey] != evaluatedFingerprint ||
			(fileDownloadDecisionDependsOnActiveViewer(evaluatedSource, evaluatedAccess) && viewerFingerprint != evaluatedViewerFingerprint) {
			response = connect.NewResponse(unavailableFileDownloadResponse())
			return nil
		}
		response, loadErr = s.signReferencedFileDownload(current, evaluatedMediaAccess, evaluatedAccess)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *FileService) lockContentDownloadOwner(
	ctx context.Context,
	tx *gorm.DB,
	entityType openv1.PublicMediaEntityType,
	entityID string,
) (string, bool, error) {
	ownerTable, ok := contentDownloadOwnerTable(entityType)
	if !ok {
		return "", false, errs.InvalidArgument("entity_type", "unsupported Content Block owner")
	}
	var owner struct {
		Status string `gorm:"column:status"`
	}
	result := tx.WithContext(ctx).Table(ownerTable).
		Select("status").Where("id = ?", entityID).
		Clauses(clause.Locking{Strength: "SHARE"}).Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if result.Error != nil {
		return "", false, errs.Internal(result.Error)
	}
	return owner.Status, true, nil
}

func (s *FileService) lockReleaseDownloadOwner(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
) (string, bool, error) {
	var owner struct {
		Status string `gorm:"column:status"`
	}
	result := tx.WithContext(ctx).Table("release").
		Select("status").Where("id = ?", releaseID).
		Clauses(clause.Locking{Strength: "SHARE"}).Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if result.Error != nil {
		return "", false, errs.Internal(result.Error)
	}
	return owner.Status, true, nil
}

func contentDownloadOwnerTable(entityType openv1.PublicMediaEntityType) (string, bool) {
	switch entityType {
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST:
		return "post", true
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PAGE:
		return "page", true
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_WORK:
		return "work", true
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PROGRAM_EVENT:
		return "program_event", true
	default:
		return "", false
	}
}

func (s *FileService) resolveContentBlockDownloadSource(
	ctx context.Context,
	db *gorm.DB,
	entityType openv1.PublicMediaEntityType,
	entityID string,
	blockID string,
	referencePath string,
	lock bool,
) (mediaasset.FileDownloadSource, string, bool, error) {
	ownerTable, ok := contentDownloadOwnerTable(entityType)
	if !ok {
		return mediaasset.FileDownloadSource{}, "", false, errs.InvalidArgument("entity_type", "unsupported Content Block owner")
	}

	var owner struct {
		Status string `gorm:"column:status"`
	}
	query := db.WithContext(ctx).
		Table("content_block_attachment AS cbf").
		Select("owner.status").
		Joins("JOIN content_block AS cb ON cb.id = cbf.block_id").
		Joins("JOIN "+ownerTable+" AS owner ON owner.content_document_id = cb.document_id").
		Where("owner.id = ? AND cb.id = ? AND cb.kind = 'file' AND cbf.reference_path = ? AND cbf.selector_kind = 'active'", entityID, blockID, referencePath)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	result := query.Take(&owner)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return mediaasset.FileDownloadSource{}, "", false, nil
		}
		return mediaasset.FileDownloadSource{}, "", false, errs.Internal(result.Error)
	}
	sources, err := mediaasset.LoadContentBlockDownloadSources(ctx, db, []mediaasset.ContentBlockDownloadSelector{{
		BlockID: blockID, ReferencePath: referencePath,
	}})
	if err != nil {
		return mediaasset.FileDownloadSource{}, "", false, errs.Internal(err)
	}
	source, found := sources[mediaasset.ContentBlockDownloadPolicyKey(blockID, referencePath)]
	return source, owner.Status, found, nil
}

func (s *FileService) resolveReleaseTrackDownloadSource(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	trackID string,
	lock bool,
) (mediaasset.FileDownloadSource, string, bool, error) {
	var owner struct {
		Status string `gorm:"column:status"`
	}
	query := db.WithContext(ctx).
		Table("track").
		Select("release.status").
		Joins("JOIN release ON release.id = track.release_id").
		Where("track.id = ? AND release.id = ? AND track.audio_original_file_id IS NOT NULL", trackID, releaseID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	result := query.Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return mediaasset.FileDownloadSource{}, "", false, nil
	}
	if result.Error != nil {
		return mediaasset.FileDownloadSource{}, "", false, errs.Internal(result.Error)
	}
	sources, err := mediaasset.LoadTrackDownloadSources(ctx, db, []string{trackID})
	if err != nil {
		return mediaasset.FileDownloadSource{}, "", false, errs.Internal(err)
	}
	source, found := sources[trackID]
	return source, owner.Status, found, nil
}

func (s *FileService) resolveContentOwnerDownloadAccess(
	ctx context.Context,
	entityType openv1.PublicMediaEntityType,
	entityID string,
	ownerStatus string,
) (resolvedMediaAccess, bool, error) {
	var viewAction auth.ResourceAction
	var publicStatuses []string
	switch entityType {
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST:
		viewAction = policyv1.Post.View
		publicStatuses = []string{managev1.PostStatus_POST_STATUS_PUBLISHED.String(), managev1.PostStatus_POST_STATUS_ARCHIVED.String()}
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PAGE:
		viewAction = policyv1.Page.View
		publicStatuses = []string{managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()}
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_WORK:
		viewAction = policyv1.Work.View
		publicStatuses = []string{managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(), managev1.WorkStatus_WORK_STATUS_ARCHIVED.String()}
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PROGRAM_EVENT:
		viewAction = policyv1.ProgramEvent.View
		publicStatuses = []string{managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(), managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()}
	default:
		return resolvedMediaAccess{}, false, errs.InvalidArgument("entity_type", "unsupported Content Block owner")
	}
	if slices.Contains(publicStatuses, ownerStatus) {
		return resolvedMediaAccess{}, true, nil
	}
	allowed, err := hasDraftResourceView(ctx, s.spiceDB, viewAction, entityID)
	if err != nil {
		return resolvedMediaAccess{}, false, err
	}
	access := resolvedMediaAccess{directDraft: allowed}
	if allowed {
		if user := auth.GetUser(ctx); user != nil {
			access.directDraftIdentityID = user.IdentityID.String()
			access.directDraftMemberID = user.MemberID.String()
		}
	}
	return access, allowed, nil
}

func lockDirectDraftDownloadPrincipal(
	ctx context.Context,
	tx *gorm.DB,
	access resolvedMediaAccess,
) (bool, error) {
	if !access.directDraft {
		return true, nil
	}
	principal := &auth.UserInfo{
		IdentityID:    auth.IdentityID(access.directDraftIdentityID),
		MemberID:      auth.MemberID(access.directDraftMemberID),
		Authenticated: true,
		Onboarded:     true,
	}
	return identitystate.LockActivePrincipal(ctx, tx, principal)
}

func (s *FileService) evaluateReferencedFileDownload(
	ctx context.Context,
	db *gorm.DB,
	source mediaasset.FileDownloadSource,
) (*openv1.FileDownloadAccess, error) {

	user := auth.GetUser(ctx)
	allowed, err := mediaasset.EvaluateFileDownloadAccess(ctx, db, s.spiceDB, source, user, s.segments)
	if err != nil {
		return nil, errs.Internal(err)
	}
	signInActionable := source.Audience == mediaasset.FileDownloadAudienceAuthenticated
	if source.Audience == mediaasset.FileDownloadAudienceRestricted &&
		(user == nil || !user.Authenticated || user.MemberID == "") {
		presence, err := mediaasset.RestrictedFileDownloadSegmentPresence(ctx, db, map[string]mediaasset.FileDownloadSource{source.PolicyKey: source})
		if err != nil {
			return nil, errs.Internal(err)
		}
		signInActionable = presence[source.PolicyKey]
	}
	return effectiveFileDownloadAccess(
		source.Audience,
		user,
		allowed,
		signInActionable,
	), nil
}

func (s *FileService) signReferencedFileDownload(
	source mediaasset.FileDownloadSource,
	mediaAccess resolvedMediaAccess,
	access *openv1.FileDownloadAccess,
) (*connect.Response[openv1.AuthorizeDownloadResponse], error) {
	response := &openv1.AuthorizeDownloadResponse{Access: access}
	if access.GetAction() != openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD {
		return connect.NewResponse(response), nil
	}

	downloadTTL := s.effectiveDownloadTTL()
	if mediaAccess.directDraft {
		downloadTTL = directDraftMediaTTL(downloadTTL)
	}
	download, err := buildExpiringFileRef(
		s.mediaDomain,
		s.mediaSecret,
		source.FileID,
		source.Extension,
		source.MimeType,
		source.FileName,
		mediaauth.PurposeDownload,
		downloadTTL,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	response.Download = download
	return connect.NewResponse(response), nil
}

func effectiveFileDownloadAccess(
	audience mediaasset.FileDownloadAudience,
	user *auth.UserInfo,
	allowed bool,
	signInActionable bool,
) *openv1.FileDownloadAccess {
	if allowed {
		return &openv1.FileDownloadAccess{
			Availability: openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
			Action:       openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD,
		}
	}
	if (audience == mediaasset.FileDownloadAudienceAuthenticated ||
		audience == mediaasset.FileDownloadAudienceRestricted) &&
		(user == nil || !user.Authenticated || user.MemberID == "") &&
		signInActionable {
		return &openv1.FileDownloadAccess{
			Availability: openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_AVAILABLE,
			Action:       openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_SIGN_IN,
		}
	}
	return unavailableFileDownloadAccess()
}

func fileDownloadDecisionDependsOnActiveViewer(
	source mediaasset.FileDownloadSource,
	access *openv1.FileDownloadAccess,
) bool {
	return access.GetAction() == openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD &&
		(source.Audience == mediaasset.FileDownloadAudienceAuthenticated || source.Audience == mediaasset.FileDownloadAudienceRestricted)
}

func unavailableFileDownloadAccess() *openv1.FileDownloadAccess {
	return &openv1.FileDownloadAccess{
		Availability: openv1.FileDownloadAvailability_FILE_DOWNLOAD_AVAILABILITY_UNAVAILABLE,
		Action:       openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_NONE,
	}
}

func unavailableFileDownloadResponse() *openv1.AuthorizeDownloadResponse {
	return &openv1.AuthorizeDownloadResponse{
		Access: unavailableFileDownloadAccess(),
	}
}
