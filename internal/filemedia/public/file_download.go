package public

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	pageaccesspostgres "github.com/echovisionlab/geul-api/internal/pageaccess/postgres"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AuthorizeDownloadQuery names the File a public viewer asks to download:
// a Release Track's original audio (EntityType release, TrackID), or a File
// Block attachment of a document (ContentBlock).
type AuthorizeDownloadQuery struct {
	EntityType   openv1.PublicMediaEntityType
	EntityID     string
	TrackID      string
	ContentBlock *ContentBlockFileSelector
}

// ContentBlockFileSelector names one File reference of a document Block.
type ContentBlockFileSelector struct {
	BlockID       string
	ReferencePath string
}

// AuthorizeDownload evaluates the viewer's download access in one snapshot,
// then locks the policy segments, the owner, the draft principal and the
// viewer, re-reads the attachment and signs only when nothing it evaluated
// changed.
func (s *Service) AuthorizeDownload(ctx context.Context, query AuthorizeDownloadQuery) (*openv1.AuthorizeDownloadResponse, error) {
	entityID := strings.TrimSpace(query.EntityID)
	if entityID == "" {
		return nil, domainerr.Required("entity_id")
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return nil, domainerr.InvalidField("entity_id", "must be a valid UUID")
	}
	var target fileDownloadTarget
	var err error
	if query.EntityType == openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_RELEASE {
		target, err = s.releaseTrackDownloadTarget(entityID, query.TrackID)
	} else {
		target, err = s.contentBlockDownloadTarget(query.EntityType, entityID, query.ContentBlock)
	}
	if err != nil {
		return nil, err
	}
	return s.authorizeFileDownload(ctx, target)
}

// fileDownloadTarget is how one kind of attachment resolves its source and
// owner. The evaluate and revalidate passes are shared.
type fileDownloadTarget struct {
	// resolve reads the attachment's source and owner status, locking them
	// when locked is set.
	resolve func(ctx context.Context, tx *gorm.DB, locked bool) (mediaasset.FileDownloadSource, string, bool, error)
	// ownerAccess decides whether the viewer may see the owner in
	// ownerStatus, and how.
	ownerAccess func(ctx context.Context, tx *gorm.DB, ownerStatus string) (resolvedMediaAccess, bool, error)
	// lockOwner locks the owner row and reads its status.
	lockOwner func(ctx context.Context, tx *gorm.DB) (string, bool, error)
	// ownerVisible, when set, re-checks owner visibility under the owner
	// lock.
	ownerVisible func(ctx context.Context, tx *gorm.DB, ownerStatus string) (bool, error)
	// samePolicyKey also requires the re-read attachment to keep the
	// evaluated policy key.
	samePolicyKey bool
}

func (s *Service) contentBlockDownloadTarget(entityType openv1.PublicMediaEntityType, entityID string, selector *ContentBlockFileSelector) (fileDownloadTarget, error) {
	if selector == nil {
		return fileDownloadTarget{}, domainerr.Required("content_block")
	}
	blockID := strings.TrimSpace(selector.BlockID)
	if _, err := uuid.Parse(blockID); err != nil {
		return fileDownloadTarget{}, domainerr.InvalidField("selector.block_id", "must be a valid UUID")
	}
	referencePath := strings.TrimSpace(selector.ReferencePath)
	if referencePath == "" || len(referencePath) > 512 || strings.IndexFunc(referencePath, unicode.IsControl) >= 0 {
		return fileDownloadTarget{}, domainerr.InvalidField("selector.reference_path", "must be non-empty, control-free, and at most 512 bytes")
	}
	return fileDownloadTarget{
		resolve: func(ctx context.Context, tx *gorm.DB, locked bool) (mediaasset.FileDownloadSource, string, bool, error) {
			return s.resolveContentBlockDownloadSource(ctx, tx, entityType, entityID, blockID, referencePath, locked)
		},
		ownerAccess: func(ctx context.Context, tx *gorm.DB, ownerStatus string) (resolvedMediaAccess, bool, error) {
			return s.resolveContentOwnerDownloadAccess(ctx, tx, entityType, entityID, ownerStatus)
		},
		lockOwner: func(ctx context.Context, tx *gorm.DB) (string, bool, error) {
			return s.lockContentDownloadOwner(ctx, tx, entityType, entityID)
		},
		ownerVisible: func(ctx context.Context, tx *gorm.DB, ownerStatus string) (bool, error) {
			if entityType != openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PAGE || ownerStatus != string(model.PageStatusPublished) {
				return true, nil
			}
			reason, err := pageaccesspostgres.EvaluateStored(ctx, tx, s.spiceDB, entityID)
			if err != nil {
				return false, err
			}
			return reason == pageaccess.ReasonAllowed, nil
		},
		samePolicyKey: true,
	}, nil
}

func (s *Service) releaseTrackDownloadTarget(releaseID, trackID string) (fileDownloadTarget, error) {
	trackID = strings.TrimSpace(trackID)
	if _, err := uuid.Parse(trackID); err != nil {
		return fileDownloadTarget{}, domainerr.InvalidField("track_id", "must be a valid UUID")
	}
	return fileDownloadTarget{
		resolve: func(ctx context.Context, tx *gorm.DB, locked bool) (mediaasset.FileDownloadSource, string, bool, error) {
			return s.resolveReleaseTrackDownloadSource(ctx, tx, releaseID, trackID, locked)
		},
		ownerAccess: func(ctx context.Context, _ *gorm.DB, ownerStatus string) (resolvedMediaAccess, bool, error) {
			mediaAccess := resolvedMediaAccess{}
			if ownerStatus == string(model.ReleaseStatusPublished) {
				return mediaAccess, true, nil
			}
			allowed, err := hasDraftResourceView(ctx, s.spiceDB, policyv1.Release.View, releaseID)
			if err != nil {
				return mediaAccess, false, err
			}
			if !allowed {
				return mediaAccess, false, nil
			}
			mediaAccess.directDraft = true
			if user := auth.GetUser(ctx); user != nil {
				mediaAccess.directDraftIdentityID = user.IdentityID.String()
				mediaAccess.directDraftMemberID = user.MemberID.String()
			}
			return mediaAccess, true, nil
		},
		lockOwner: func(ctx context.Context, tx *gorm.DB) (string, bool, error) {
			return s.lockReleaseDownloadOwner(ctx, tx, releaseID)
		},
	}, nil
}

// fileDownloadEvaluation is what the evaluate pass decided and the facts the
// revalidate pass must find unchanged. A zero source means the attachment is
// unavailable.
type fileDownloadEvaluation struct {
	source            mediaasset.FileDownloadSource
	ownerStatus       string
	mediaAccess       resolvedMediaAccess
	access            *openv1.FileDownloadAccess
	fingerprint       string
	viewerFingerprint string
}

func (s *Service) authorizeFileDownload(ctx context.Context, target fileDownloadTarget) (*openv1.AuthorizeDownloadResponse, error) {
	var evaluation fileDownloadEvaluation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var evaluateErr error
		evaluation, evaluateErr = s.evaluateFileDownload(ctx, tx, target)
		return evaluateErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if evaluation.source.PolicyKey == "" {
		return &openv1.AuthorizeDownloadResponse{Access: evaluation.access}, nil
	}
	var response *openv1.AuthorizeDownloadResponse
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var revalidateErr error
		response, revalidateErr = s.revalidateFileDownload(ctx, tx, target, evaluation)
		return revalidateErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Service) evaluateFileDownload(ctx context.Context, tx *gorm.DB, target fileDownloadTarget) (fileDownloadEvaluation, error) {
	unavailable := fileDownloadEvaluation{access: unavailableFileDownloadAccess()}
	source, ownerStatus, found, err := target.resolve(ctx, tx, false)
	if err != nil || !found {
		return unavailable, err
	}
	mediaAccess, allowed, err := target.ownerAccess(ctx, tx, ownerStatus)
	if err != nil || !allowed {
		return unavailable, err
	}
	access, err := s.evaluateReferencedFileDownload(ctx, tx, source)
	if err != nil {
		return fileDownloadEvaluation{}, err
	}
	fingerprint, viewerFingerprint, err := s.fileDownloadFingerprints(ctx, tx, source)
	if err != nil {
		return fileDownloadEvaluation{}, err
	}
	return fileDownloadEvaluation{
		source: source, ownerStatus: ownerStatus, mediaAccess: mediaAccess, access: access,
		fingerprint: fingerprint, viewerFingerprint: viewerFingerprint,
	}, nil
}

// revalidateFileDownload signs the download only when the locked owner,
// draft principal, viewer and attachment still match the evaluation.
func (s *Service) revalidateFileDownload(ctx context.Context, tx *gorm.DB, target fileDownloadTarget, evaluation fileDownloadEvaluation) (*openv1.AuthorizeDownloadResponse, error) {
	evaluatedSources := map[string]mediaasset.FileDownloadSource{evaluation.source.PolicyKey: evaluation.source}
	if err := mediaasset.LockFileDownloadPolicySegments(ctx, tx, evaluatedSources); err != nil {
		return nil, err
	}
	ownerStatus, ownerFound, err := target.lockOwner(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !ownerFound || ownerStatus != evaluation.ownerStatus {
		return unavailableFileDownloadResponse(), nil
	}
	if target.ownerVisible != nil {
		visible, err := target.ownerVisible(ctx, tx, ownerStatus)
		if err != nil {
			return nil, err
		}
		if !visible {
			return unavailableFileDownloadResponse(), nil
		}
	}
	draftPrincipalActive, err := lockDirectDraftDownloadPrincipal(ctx, tx, evaluation.mediaAccess)
	if err != nil {
		return nil, err
	}
	if !draftPrincipalActive {
		return unavailableFileDownloadResponse(), nil
	}
	viewerActive, err := mediaasset.LockFileDownloadViewerFacts(ctx, tx, evaluatedSources, auth.GetUser(ctx))
	if err != nil {
		return nil, err
	}
	dependsOnViewer := fileDownloadDecisionDependsOnActiveViewer(evaluation.source, evaluation.access)
	if dependsOnViewer && !viewerActive {
		return unavailableFileDownloadResponse(), nil
	}
	current, ownerStatus, found, err := target.resolve(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	if !found || ownerStatus != evaluation.ownerStatus {
		return unavailableFileDownloadResponse(), nil
	}
	fingerprint, viewerFingerprint, err := s.fileDownloadFingerprints(ctx, tx, current)
	if err != nil {
		return nil, err
	}
	if (target.samePolicyKey && current.PolicyKey != evaluation.source.PolicyKey) || fingerprint != evaluation.fingerprint ||
		(dependsOnViewer && viewerFingerprint != evaluation.viewerFingerprint) {
		return unavailableFileDownloadResponse(), nil
	}
	return s.signReferencedFileDownload(current, evaluation.mediaAccess, evaluation.access)
}

// fileDownloadFingerprints reads the policy and viewer fingerprints of one
// attachment source.
func (s *Service) fileDownloadFingerprints(ctx context.Context, tx *gorm.DB, source mediaasset.FileDownloadSource) (string, string, error) {
	sources := map[string]mediaasset.FileDownloadSource{source.PolicyKey: source}
	fingerprints, err := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, sources, s.segments)
	if err != nil {
		return "", "", err
	}
	viewerFingerprint, err := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, sources, auth.GetUser(ctx))
	if err != nil {
		return "", "", err
	}
	return fingerprints[source.PolicyKey], viewerFingerprint, nil
}

func (s *Service) lockContentDownloadOwner(
	ctx context.Context,
	tx *gorm.DB,
	entityType openv1.PublicMediaEntityType,
	entityID string,
) (string, bool, error) {
	ownerTable, ok := contentDownloadOwnerTable(entityType)
	if !ok {
		return "", false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
	}
	var owner struct {
		Status string `gorm:"column:status"`
	}
	result := tx.WithContext(ctx).Table(ownerTable).
		Select("status").Where("id = ?", entityID).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if result.Error != nil {
		return "", false, result.Error
	}
	return owner.Status, true, nil
}

func (s *Service) lockReleaseDownloadOwner(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
) (string, bool, error) {
	var owner struct {
		Status string `gorm:"column:status"`
	}
	result := tx.WithContext(ctx).Table("release").
		Select("status").Where("id = ?", releaseID).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if result.Error != nil {
		return "", false, result.Error
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

func (s *Service) resolveContentBlockDownloadSource(
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
		return mediaasset.FileDownloadSource{}, "", false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	result := query.Take(&owner)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return mediaasset.FileDownloadSource{}, "", false, nil
		}
		return mediaasset.FileDownloadSource{}, "", false, result.Error
	}
	sources, err := mediaasset.LoadContentBlockDownloadSources(ctx, db, []mediaasset.ContentBlockDownloadSelector{{
		BlockID: blockID, ReferencePath: referencePath,
	}})
	if err != nil {
		return mediaasset.FileDownloadSource{}, "", false, err
	}
	source, found := sources[mediaasset.ContentBlockDownloadPolicyKey(blockID, referencePath)]
	return source, owner.Status, found, nil
}

func (s *Service) resolveReleaseTrackDownloadSource(
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	result := query.Take(&owner)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return mediaasset.FileDownloadSource{}, "", false, nil
	}
	if result.Error != nil {
		return mediaasset.FileDownloadSource{}, "", false, result.Error
	}
	sources, err := mediaasset.LoadTrackDownloadSources(ctx, db, []string{trackID})
	if err != nil {
		return mediaasset.FileDownloadSource{}, "", false, err
	}
	source, found := sources[trackID]
	return source, owner.Status, found, nil
}

func (s *Service) resolveContentOwnerDownloadAccess(
	ctx context.Context,
	db *gorm.DB,
	entityType openv1.PublicMediaEntityType,
	entityID string,
	ownerStatus string,
) (resolvedMediaAccess, bool, error) {
	var viewAction auth.ResourceAction
	var publicStatuses []string
	switch entityType {
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_POST:
		viewAction = policyv1.Post.View
		publicStatuses = []string{string(model.PostStatusPublished), string(model.PostStatusArchived)}
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PAGE:
		viewAction = policyv1.Page.View
		publicStatuses = []string{string(model.PageStatusPublished)}
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_WORK:
		viewAction = policyv1.Work.View
		publicStatuses = []string{string(model.WorkStatusPublished), string(model.WorkStatusArchived)}
	case openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PROGRAM_EVENT:
		viewAction = policyv1.ProgramEvent.View
		publicStatuses = []string{string(model.ProgramEventStatusPublished), string(model.ProgramEventStatusArchived)}
	default:
		return resolvedMediaAccess{}, false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
	}
	if slices.Contains(publicStatuses, ownerStatus) {
		if entityType == openv1.PublicMediaEntityType_PUBLIC_MEDIA_ENTITY_TYPE_PAGE {
			reason, err := pageaccesspostgres.EvaluateStored(ctx, db, s.spiceDB, entityID)
			return resolvedMediaAccess{}, reason == pageaccess.ReasonAllowed, err
		}
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

func (s *Service) evaluateReferencedFileDownload(
	ctx context.Context,
	db *gorm.DB,
	source mediaasset.FileDownloadSource,
) (*openv1.FileDownloadAccess, error) {

	user := auth.GetUser(ctx)
	allowed, err := mediaasset.EvaluateFileDownloadAccess(ctx, db, s.spiceDB, source, user, s.segments)
	if err != nil {
		return nil, err
	}
	signInActionable := source.Audience == mediaasset.FileDownloadAudienceAuthenticated
	if source.Audience == mediaasset.FileDownloadAudienceRestricted &&
		(user == nil || !user.Authenticated || user.MemberID == "") {
		presence, err := mediaasset.RestrictedFileDownloadSegmentPresence(ctx, db, map[string]mediaasset.FileDownloadSource{source.PolicyKey: source})
		if err != nil {
			return nil, err
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

func (s *Service) signReferencedFileDownload(
	source mediaasset.FileDownloadSource,
	mediaAccess resolvedMediaAccess,
	access *openv1.FileDownloadAccess,
) (*openv1.AuthorizeDownloadResponse, error) {
	response := &openv1.AuthorizeDownloadResponse{Access: access}
	if access.GetAction() != openv1.FileDownloadAction_FILE_DOWNLOAD_ACTION_DOWNLOAD {
		return response, nil
	}

	downloadTTL := s.effectiveDownloadTTL()
	if mediaAccess.directDraft {
		downloadTTL = directDraftMediaTTL(downloadTTL)
	}
	download, err := mediaauth.ExpiringFileRef(
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
		return nil, err
	}
	response.Download = download
	return response, nil
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
