package release

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepublic "github.com/echovisionlab/geul-api/internal/release/public"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type DownloadAccess struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	criteriaSet mediaassetpostgres.CriteriaConfigLoader
}

func NewDownloadAccess(db *gorm.DB, spiceDB *auth.SpiceDBClient, criteriaSet mediaassetpostgres.CriteriaConfigLoader) *DownloadAccess {
	if criteriaSet == nil {
		panic("release download access: criteria config loader is required")
	}
	return &DownloadAccess{db: db, spiceDB: spiceDB, criteriaSet: criteriaSet}
}

func (a *DownloadAccess) Resolve(
	ctx context.Context,
	releaseID string,
	expectedReleaseStatus string,
	ownerAuthorization mediaasset.ContentDownloadOwnerAuthorization,
	requests []releasepublic.TrackDownloadAccessRequest,
	signer releasepublic.TrackDownloadSigner,
) (map[string]releasepublic.TrackDownloadAuthorization, error) {
	trackIDs := make([]string, 0, len(requests))
	expectedFileByTrackID := make(map[string]string, len(requests))
	result := make(map[string]releasepublic.TrackDownloadAuthorization, len(requests))
	for _, request := range requests {
		trackIDs = append(trackIDs, request.TrackID)
		expectedFileByTrackID[request.TrackID] = request.FileID
		result[request.TrackID] = releasepublic.TrackDownloadAuthorization{Access: unavailableTrackDownloadAccess()}
	}
	if len(trackIDs) == 0 {
		return result, nil
	}

	user := auth.GetUser(ctx)
	var evaluatedSources map[string]mediaasset.FileDownloadSource
	var evaluatedAccess map[string]*releasepublic.DownloadAccess
	var evaluatedFingerprints map[string]string
	var evaluatedViewerFingerprint string
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ownerAuthorized, authorizeErr := a.validateReleaseDownloadOwnerAuthorization(
			ctx, tx, releaseID, expectedReleaseStatus, ownerAuthorization, false,
		)
		if authorizeErr != nil {
			return authorizeErr
		}
		if !ownerAuthorized {
			evaluatedSources = map[string]mediaasset.FileDownloadSource{}
			return nil
		}
		sources, loadErr := mediaasset.LoadTrackDownloadSources(ctx, mediaassetpostgres.NewDownloadStore(tx, nil), trackIDs)
		if loadErr != nil {
			return loadErr
		}
		for trackID, source := range sources {
			if source.FileID != expectedFileByTrackID[trackID] {
				delete(sources, trackID)
			}
		}
		allowed, evaluateErr := mediaasset.EvaluateFileDownloadAccessBatch(ctx, mediaassetpostgres.NewDownloadStore(tx, a.criteriaSet), a.spiceDB, sources, user)
		if evaluateErr != nil {
			return evaluateErr
		}
		signIn := make(map[string]bool, len(sources))
		if user == nil || !user.Authenticated || user.MemberID == "" {
			restricted := make(map[string]mediaasset.FileDownloadSource)
			for id, source := range sources {
				switch source.Access {
				case mediaasset.FileDownloadAccessAuthenticated:
					signIn[id] = true
				case mediaasset.FileDownloadAccessRestricted:
					restricted[id] = source
				case mediaasset.FileDownloadAccessDisabled, mediaasset.FileDownloadAccessPublic:
					// Signing in changes nothing: the download is off, or already open.
				}
			}
			presence, presenceErr := mediaasset.RestrictedFileDownloadCriteriaPresence(ctx, mediaassetpostgres.NewDownloadStore(tx, nil), restricted)
			if presenceErr != nil {
				return presenceErr
			}
			for id, present := range presence {
				signIn[id] = present
			}
		}
		access := make(map[string]*releasepublic.DownloadAccess, len(sources))
		for id, source := range sources {
			access[id] = trackDownloadAccess(source, user, allowed[id], signIn[id])
		}
		fingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, mediaassetpostgres.NewDownloadStore(tx, a.criteriaSet), sources)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		viewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, mediaassetpostgres.NewDownloadStore(tx, nil), sources, user)
		if viewerErr != nil {
			return viewerErr
		}
		evaluatedSources, evaluatedAccess = sources, access
		evaluatedFingerprints, evaluatedViewerFingerprint = fingerprints, viewerFingerprint
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if len(evaluatedSources) == 0 {
		return result, nil
	}

	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if lockErr := mediaasset.LockFileDownloadPolicyCriteriaSet(ctx, mediaassetpostgres.NewDownloadStore(tx, nil), evaluatedSources); lockErr != nil {
			return lockErr
		}
		var releaseRow struct {
			Status            string  `gorm:"column:status"`
			ContentDocumentID *string `gorm:"column:content_document_id"`
		}
		releaseQuery := tx.WithContext(ctx).Table("release").Select("status, content_document_id").Where("id = ?", releaseID).
			Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Take(&releaseRow)
		if errors.Is(releaseQuery.Error, gorm.ErrRecordNotFound) || releaseRow.Status != expectedReleaseStatus {
			return nil
		}
		if releaseQuery.Error != nil {
			return releaseQuery.Error
		}
		ownerAuthorized, authorizeErr := a.validateLockedReleaseDownloadOwnerAuthorization(
			ctx, tx, releaseID, releaseRow.Status, releaseRow.ContentDocumentID, ownerAuthorization, true,
		)
		if authorizeErr != nil {
			return authorizeErr
		}
		if !ownerAuthorized {
			return nil
		}
		viewerActive, lockErr := mediaasset.LockFileDownloadViewerFacts(ctx, mediaassetpostgres.NewDownloadStore(tx, nil), evaluatedSources, user)
		if lockErr != nil {
			return lockErr
		}
		var lockedTracks []struct {
			ID string `gorm:"column:id"`
		}
		if lockErr := tx.WithContext(ctx).Table("track").Select("id").
			Where("release_id = ? AND id IN ? AND audio_original_file_id IS NOT NULL", releaseID, trackIDs).
			Order("id").Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Find(&lockedTracks).Error; lockErr != nil {
			return lockErr
		}
		currentSources, loadErr := mediaasset.LoadTrackDownloadSources(ctx, mediaassetpostgres.NewDownloadStore(tx, nil), trackIDs)
		if loadErr != nil {
			return loadErr
		}
		currentFingerprints, fingerprintErr := mediaasset.LoadFileDownloadPolicyFingerprints(ctx, mediaassetpostgres.NewDownloadStore(tx, a.criteriaSet), currentSources)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		viewerFingerprint, viewerErr := mediaasset.LoadFileDownloadViewerFingerprint(ctx, mediaassetpostgres.NewDownloadStore(tx, nil), currentSources, user)
		if viewerErr != nil {
			return viewerErr
		}
		for _, request := range requests {
			evaluated, evaluatedExists := evaluatedSources[request.TrackID]
			current, currentExists := currentSources[request.TrackID]
			decision := evaluatedAccess[request.TrackID]
			dependsOnViewer := trackDownloadDecisionDependsOnActiveViewer(evaluated, decision)
			if !evaluatedExists || !currentExists || current.FileID != request.FileID ||
				evaluated.FileID != request.FileID || currentFingerprints[request.TrackID] != evaluatedFingerprints[request.TrackID] ||
				(dependsOnViewer && (!viewerActive || viewerFingerprint != evaluatedViewerFingerprint)) {
				continue
			}
			authorization := releasepublic.TrackDownloadAuthorization{Access: decision}
			if downloadAction(authorization.Access) == releasepublic.DownloadActionDownload && signer != nil {
				download, signErr := signer(releasepublic.MediaFile{
					ID: current.FileID, Extension: current.Extension, MimeType: current.MimeType, FileName: current.FileName,
				})
				if signErr != nil {
					return signErr
				}
				authorization.Download = download
			}
			result[request.TrackID] = authorization
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (a *DownloadAccess) validateReleaseDownloadOwnerAuthorization(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	expectedStatus string,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
	locked bool,
) (bool, error) {
	var row struct {
		Status            string  `gorm:"column:status"`
		ContentDocumentID *string `gorm:"column:content_document_id"`
	}
	query := db.WithContext(ctx).Table("release").Select("status, content_document_id").Where("id = ?", releaseID)
	if locked {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if row.Status != expectedStatus {
		return false, nil
	}
	return a.validateLockedReleaseDownloadOwnerAuthorization(
		ctx, db, releaseID, row.Status, row.ContentDocumentID, authorization, locked,
	)
}

func (a *DownloadAccess) validateLockedReleaseDownloadOwnerAuthorization(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	status string,
	contentDocumentID *string,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
	locked bool,
) (bool, error) {
	documentID := ""
	if contentDocumentID != nil {
		documentID = *contentDocumentID
	}
	if authorization.ResourceType != "release" || authorization.ResourceID != releaseID ||
		authorization.Status != status || authorization.DocumentID != documentID {
		return false, nil
	}
	switch authorization.Mode {
	case mediaasset.ContentDownloadOwnerAccessPublic:
		return status == string(model.ReleaseStatusPublished), nil
	case mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft:
		user := auth.GetUser(ctx)
		if user == nil || !user.Authenticated || user.Banned ||
			user.IdentityID.String() != authorization.IdentityID || user.MemberID.String() != authorization.MemberID {
			return false, nil
		}
		if locked {
			return identitystate.LockActivePrincipal(ctx, db, user)
		}
		can, err := policyv1.Release.View(releaseID)
		if err != nil {
			return false, err
		}
		decision, err := auth.AuthorizationDecision(ctx, can)
		if err != nil {
			return false, err
		}
		return a.spiceDB.Can(ctx, decision)
	case mediaasset.ContentDownloadOwnerAccessShare:
		return validateReleaseDownloadShareLink(ctx, db, releaseID, authorization.ShareLink, locked)
	default:
		return false, nil
	}
}

func validateReleaseDownloadShareLink(
	ctx context.Context,
	db *gorm.DB,
	releaseID string,
	witness *mediaasset.ContentDownloadShareLinkWitness,
	locked bool,
) (bool, error) {
	if witness == nil || strings.TrimSpace(witness.ID) == "" ||
		witness.EntityType != managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String() ||
		witness.EntityID != releaseID {
		return false, nil
	}
	var link model.ShareLink
	query := db.WithContext(ctx).Where("id = ?", witness.ID)
	if locked {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	}
	if err := query.Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if link.ID != witness.ID || link.EntityType != witness.EntityType || link.EntityID != witness.EntityID ||
		!sameReleaseDownloadTime(link.ExpiresAt, witness.ExpiresAt) {
		return false, nil
	}
	return link.ExpiresAt != nil && link.ExpiresAt.After(time.Now()), nil
}

func sameReleaseDownloadTime(left, right *time.Time) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && left.Equal(*right))
}

func trackDownloadAccess(source mediaasset.FileDownloadSource, user *auth.UserInfo, allowed, signIn bool) *releasepublic.DownloadAccess {
	if allowed {
		return &releasepublic.DownloadAccess{Availability: releasepublic.DownloadAvailable, Action: releasepublic.DownloadActionDownload}
	}
	if (source.Access == mediaasset.FileDownloadAccessAuthenticated || source.Access == mediaasset.FileDownloadAccessRestricted) &&
		(user == nil || !user.Authenticated || user.MemberID == "") && signIn {
		return &releasepublic.DownloadAccess{Availability: releasepublic.DownloadAvailable, Action: releasepublic.DownloadActionSignIn}
	}
	return unavailableTrackDownloadAccess()
}

func unavailableTrackDownloadAccess() *releasepublic.DownloadAccess {
	return &releasepublic.DownloadAccess{Availability: releasepublic.DownloadUnavailable, Action: releasepublic.DownloadActionNone}
}

// downloadAction is the action of access; none when there is no decision.
func downloadAction(access *releasepublic.DownloadAccess) releasepublic.DownloadAction {
	if access == nil {
		return releasepublic.DownloadActionNone
	}
	return access.Action
}

func trackDownloadDecisionDependsOnActiveViewer(
	source mediaasset.FileDownloadSource,
	access *releasepublic.DownloadAccess,
) bool {
	return downloadAction(access) == releasepublic.DownloadActionDownload &&
		(source.Access == mediaasset.FileDownloadAccessAuthenticated || source.Access == mediaasset.FileDownloadAccessRestricted)
}
