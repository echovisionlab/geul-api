package public

import (
	"context"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AuthorizeDownloadQuery names the File a public viewer asks to download:
// a Release Track's original audio (EntityType release, TrackID), or a File
// Block attachment of a document (ContentBlock).
type AuthorizeDownloadQuery struct {
	EntityType   MediaEntityType
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
// then locks the policy Member criteria, the owner, the draft principal and the
// viewer, re-reads the attachment and signs only when nothing it evaluated
// changed.
func (s *Service) AuthorizeDownload(ctx context.Context, query AuthorizeDownloadQuery) (*DownloadResult, error) {
	entityID := strings.TrimSpace(query.EntityID)
	if entityID == "" {
		return nil, domainerr.Required("entity_id")
	}
	if _, err := uuid.Parse(entityID); err != nil {
		return nil, domainerr.InvalidField("entity_id", "must be a valid UUID")
	}
	var target fileDownloadTarget
	var err error
	if query.EntityType == EntityRelease {
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
	resolve func(ctx context.Context, tx Store, locked bool) (mediaasset.FileDownloadSource, string, bool, error)
	// ownerAccess decides whether the viewer may see the owner in
	// ownerStatus, and how.
	ownerAccess func(ctx context.Context, tx Store, ownerStatus string) (resolvedMediaAccess, bool, error)
	// lockOwner locks the owner row and reads its status.
	lockOwner func(ctx context.Context, tx Store) (string, bool, error)
	// ownerVisible, when set, re-checks owner visibility under the owner
	// lock.
	ownerVisible func(ctx context.Context, tx Store, ownerStatus string) (bool, error)
	// samePolicyKey also requires the re-read attachment to keep the
	// evaluated policy key.
	samePolicyKey bool
}

func (s *Service) contentBlockDownloadTarget(entityType MediaEntityType, entityID string, selector *ContentBlockFileSelector) (fileDownloadTarget, error) {
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
		resolve: func(ctx context.Context, tx Store, locked bool) (mediaasset.FileDownloadSource, string, bool, error) {
			return tx.ResolveContentSource(ctx, entityType, entityID, blockID, referencePath, locked)
		},
		ownerAccess: func(ctx context.Context, tx Store, ownerStatus string) (resolvedMediaAccess, bool, error) {
			return s.resolveContentOwnerDownloadAccess(ctx, tx, entityType, entityID, ownerStatus)
		},
		lockOwner: func(ctx context.Context, tx Store) (string, bool, error) {
			return tx.LockContentOwner(ctx, entityType, entityID)
		},
		ownerVisible: func(ctx context.Context, tx Store, ownerStatus string) (bool, error) {
			if entityType != EntityPage || ownerStatus != ownerPagePublished {
				return true, nil
			}
			reason, err := tx.PageAccess(ctx, s.spiceDB, entityID)
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
		resolve: func(ctx context.Context, tx Store, locked bool) (mediaasset.FileDownloadSource, string, bool, error) {
			return tx.ResolveTrackSource(ctx, releaseID, trackID, locked)
		},
		ownerAccess: func(ctx context.Context, _ Store, ownerStatus string) (resolvedMediaAccess, bool, error) {
			mediaAccess := resolvedMediaAccess{}
			if ownerStatus == ownerReleasePublished {
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
		lockOwner: func(ctx context.Context, tx Store) (string, bool, error) {
			return tx.LockReleaseOwner(ctx, releaseID)
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
	access            *DownloadAccess
	fingerprint       string
	viewerFingerprint string
}

func (s *Service) authorizeFileDownload(ctx context.Context, target fileDownloadTarget) (*DownloadResult, error) {
	var evaluation fileDownloadEvaluation
	err := s.units.ReadLocked(ctx, func(tx Store) error {
		var evaluateErr error
		evaluation, evaluateErr = s.evaluateFileDownload(ctx, tx, target)
		return evaluateErr
	})
	if err != nil {
		return nil, err
	}
	if evaluation.source.PolicyKey == "" {
		return &DownloadResult{Access: evaluation.access}, nil
	}
	var response *DownloadResult
	err = s.units.ReadLocked(ctx, func(tx Store) error {
		var revalidateErr error
		response, revalidateErr = s.revalidateFileDownload(ctx, tx, target, evaluation)
		return revalidateErr
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Service) evaluateFileDownload(ctx context.Context, tx Store, target fileDownloadTarget) (fileDownloadEvaluation, error) {
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
func (s *Service) revalidateFileDownload(ctx context.Context, tx Store, target fileDownloadTarget, evaluation fileDownloadEvaluation) (*DownloadResult, error) {
	evaluatedSources := map[string]mediaasset.FileDownloadSource{evaluation.source.PolicyKey: evaluation.source}
	if err := mediaasset.LockFileDownloadPolicyCriteriaSet(ctx, tx, evaluatedSources); err != nil {
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
func (s *Service) fileDownloadFingerprints(ctx context.Context, tx Store, source mediaasset.FileDownloadSource) (string, string, error) {
	sources := map[string]mediaasset.FileDownloadSource{source.PolicyKey: source}
	fingerprints, err := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, sources)
	if err != nil {
		return "", "", err
	}
	viewerFingerprint, err := mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, sources, auth.GetUser(ctx))
	if err != nil {
		return "", "", err
	}
	return fingerprints[source.PolicyKey], viewerFingerprint, nil
}

func (s *Service) resolveContentOwnerDownloadAccess(
	ctx context.Context,
	store Store,
	entityType MediaEntityType,
	entityID string,
	ownerStatus string,
) (resolvedMediaAccess, bool, error) {
	var viewAction auth.ResourceAction
	var publicStatuses []string
	switch entityType {
	case EntityPost:
		viewAction = policyv1.Post.View
		publicStatuses = []string{ownerPostPublished, ownerPostArchived}
	case EntityPage:
		viewAction = policyv1.Page.View
		publicStatuses = []string{ownerPagePublished}
	case EntityWork:
		viewAction = policyv1.Work.View
		publicStatuses = []string{ownerWorkPublished, ownerWorkArchived}
	case EntityProgramEvent:
		viewAction = policyv1.ProgramEvent.View
		publicStatuses = []string{ownerProgramEventPublished, ownerProgramEventArchived}
	default:
		return resolvedMediaAccess{}, false, domainerr.InvalidField("entity_type", "unsupported Content Block owner")
	}
	if slices.Contains(publicStatuses, ownerStatus) {
		if entityType == EntityPage {
			reason, err := store.PageAccess(ctx, s.spiceDB, entityID)
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
	tx Store,
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
	return tx.LockActivePrincipal(ctx, principal)
}

func (s *Service) evaluateReferencedFileDownload(
	ctx context.Context,
	store Store,
	source mediaasset.FileDownloadSource,
) (*DownloadAccess, error) {

	user := auth.GetUser(ctx)
	allowed, err := mediaasset.EvaluateFileDownloadAccess(ctx, store, s.spiceDB, source, user)
	if err != nil {
		return nil, err
	}
	signInActionable := source.Access == mediaasset.FileDownloadAccessAuthenticated
	if source.Access == mediaasset.FileDownloadAccessRestricted &&
		(user == nil || !user.Authenticated || user.MemberID == "") {
		presence, err := mediaasset.RestrictedFileDownloadCriteriaPresence(ctx, store, map[string]mediaasset.FileDownloadSource{source.PolicyKey: source})
		if err != nil {
			return nil, err
		}
		signInActionable = presence[source.PolicyKey]
	}
	return effectiveFileDownloadAccess(
		source.Access,
		user,
		allowed,
		signInActionable,
	), nil
}

func (s *Service) signReferencedFileDownload(
	source mediaasset.FileDownloadSource,
	mediaAccess resolvedMediaAccess,
	access *DownloadAccess,
) (*DownloadResult, error) {
	response := &DownloadResult{Access: access}
	if downloadAction(access) != ActionDownload {
		return response, nil
	}

	downloadTTL := s.effectiveDownloadTTL()
	if mediaAccess.directDraft {
		downloadTTL = directDraftMediaTTL(downloadTTL)
	}
	download, err := mediaauth.ExpiringReference(
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
	access mediaasset.FileDownloadAccess,
	user *auth.UserInfo,
	allowed bool,
	signInActionable bool,
) *DownloadAccess {
	if allowed {
		return &DownloadAccess{
			Availability: DownloadAvailable,
			Action:       ActionDownload,
		}
	}
	if (access == mediaasset.FileDownloadAccessAuthenticated ||
		access == mediaasset.FileDownloadAccessRestricted) &&
		(user == nil || !user.Authenticated || user.MemberID == "") &&
		signInActionable {
		return &DownloadAccess{
			Availability: DownloadAvailable,
			Action:       ActionSignIn,
		}
	}
	return unavailableFileDownloadAccess()
}

func fileDownloadDecisionDependsOnActiveViewer(
	source mediaasset.FileDownloadSource,
	access *DownloadAccess,
) bool {
	return downloadAction(access) == ActionDownload &&
		(source.Access == mediaasset.FileDownloadAccessAuthenticated || source.Access == mediaasset.FileDownloadAccessRestricted)
}

func unavailableFileDownloadAccess() *DownloadAccess {
	return &DownloadAccess{
		Availability: DownloadUnavailable,
		Action:       ActionNone,
	}
}

func unavailableFileDownloadResponse() *DownloadResult {
	return &DownloadResult{
		Access: unavailableFileDownloadAccess(),
	}
}
