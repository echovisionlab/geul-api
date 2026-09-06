package release

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A Release cascade writes one policy deletion for the Release itself, leaving
// the rest of the shared SpiceDB atomic relationship batch for Track policies.
//
//lint:ignore U1000 Tests use the named domain limit to verify the atomic authorization boundary.
const maxReleaseCascadeAuthorizationTracks = maxSpiceDBAtomicRelationshipMutations - 1

func validateReleaseCascadeAuthorizationBatchSize(trackCount int) error {
	return validateAtomicAuthorizationRelationshipBatchSize(
		trackCount+1,
		trackCount+1,
		"release has too many tracks to delete atomically; delete tracks individually until at most 999 remain",
	)
}

// ReleaseService implements the ReleaseService Connect handler
type ReleaseService struct {
	managev1connect.UnimplementedReleaseServiceHandler
	db                        *gorm.DB
	spiceDB                   *auth.SpiceDBClient
	kratosClient              auth.IdentityManager
	fileService               TrackFileManager
	assets                    Assets
	og                        OG
	waveformJobs              WaveformJobs
	asyncPublisher            AsyncPublisher
	auditWriter               domainaudit.Appender
	contentBlocks             *contentblock.Store
	trackUploadSessionCleaner interface {
		CleanupTrackUploadSessions(context.Context, string, string) error
	}
}

type ReleaseServiceOption func(*ReleaseService)

func WithReleaseContentBlockStore(store *contentblock.Store) ReleaseServiceOption {
	return func(service *ReleaseService) {
		service.contentBlocks = store
	}
}

// NewAuditedReleaseService is the production constructor. It keeps each
// authoritative Release mutation and its Domain Audit record in one DB tx.
func NewAuditedReleaseService(db *gorm.DB, spiceDB *auth.SpiceDBClient, kratosClient auth.IdentityManager, fileService TrackFileManager, assets Assets, ogLifecycle OG, waveformJobs WaveformJobs, asyncPublisher AsyncPublisher, auditWriter domainaudit.Appender, options ...ReleaseServiceOption) *ReleaseService {
	if auditWriter == nil {
		panic("ReleaseService: audit writer is required")
	}
	s := NewReleaseService(db, spiceDB, kratosClient, fileService, assets, ogLifecycle, waveformJobs, asyncPublisher, options...)
	s.auditWriter = auditWriter
	return s
}

// NewReleaseService creates a new ReleaseService
func NewReleaseService(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	kratosClient auth.IdentityManager,
	fileService TrackFileManager,
	assets Assets,
	ogLifecycle OG,
	waveformJobs WaveformJobs,
	asyncPublisher AsyncPublisher,
	options ...ReleaseServiceOption,
) *ReleaseService {
	if db == nil {
		panic("ReleaseService: db is required")
	}
	if spiceDB == nil {
		panic("ReleaseService: spiceDB is required")
	}
	if kratosClient == nil {
		panic("ReleaseService: kratosClient is required")
	}
	if fileService == nil {
		panic("ReleaseService: fileService is required")
	}
	if assets == nil {
		panic("ReleaseService: assets are required")
	}
	if ogLifecycle == nil {
		panic("ReleaseService: OG lifecycle is required")
	}
	if waveformJobs == nil {
		panic("ReleaseService: waveform jobs are required")
	}
	if asyncPublisher == nil {
		panic("ReleaseService: asyncPublisher is required")
	}

	s := &ReleaseService{
		db:             db,
		spiceDB:        spiceDB,
		kratosClient:   kratosClient,
		fileService:    fileService,
		assets:         assets,
		og:             ogLifecycle,
		waveformJobs:   waveformJobs,
		asyncPublisher: asyncPublisher,
	}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	s.trackUploadSessionCleaner = fileService
	return s
}

func classifyReleaseMutationError(err error, releaseID string) error {
	if connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFound("release", releaseID)
	}
	return errs.Internal(err)
}

// =============================================================================
// Read Methods
// =============================================================================

// GetRelease retrieves a release by ID for a Site Admin.
func (s *ReleaseService) GetRelease(
	ctx context.Context,
	req *connect.Request[managev1.GetReleaseRequest],
) (*connect.Response[managev1.Release], error) {
	if err := requireReleaseAction(ctx, s.spiceDB, req.Msg.Id, releaseActionView); err != nil {
		return nil, err
	}

	var release model.Release
	if err := s.db.WithContext(ctx).
		Where("id = ?", req.Msg.Id).
		First(&release).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("release", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	if err := s.overlayReleaseSourceLocaleDocument(ctx, &release); err != nil {
		return nil, err
	}

	artworkAsset, err := s.getReleaseArtworkAsset(ctx, release.ID)
	if err != nil {
		return nil, err
	}
	return s.releaseResponseWithArtworkOg(&release, artworkAsset)
}

// =============================================================================
// Admin Methods (all releases, requires admin role)
// =============================================================================

// ListReleasesAdmin returns a paginated list of all releases with stats
func (s *ReleaseService) ListReleasesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListReleasesAdminRequest],
) (*connect.Response[managev1.ListReleasesAdminResponse], error) {
	if err := requireReleaseList(ctx, s.spiceDB); err != nil {
		return nil, err
	}

	var releases []model.Release
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Release{})

	// Handle join filters separately (label_id requires subquery)
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "label_id" {
			if f.GetOp() == commonv1.FilterOp_FILTER_OP_EQ && f.GetValue() != "" {
				query = query.Where("id IN (SELECT release_id FROM release_label WHERE label_id = ?)", f.GetValue())
			} else if f.GetOp() == commonv1.FilterOp_FILTER_OP_IN && len(f.GetValues()) > 0 {
				query = query.Where("id IN (SELECT release_id FROM release_label WHERE label_id IN ?)", f.GetValues())
			}
		} else {
			remainingFilters = append(remainingFilters, f)
		}
	}

	// Apply filters using FilterConfig
	query, err := ReleaseAdminFilterConfig.ApplyFilters(query, remainingFilters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(20)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	// Apply sorting
	query, err = releaseSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&releases).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayReleaseSourceLocaleDocuments(ctx, releases); err != nil {
		return nil, err
	}
	artworkAssets, err := s.loadReadyReleaseArtworkAssets(ctx, releases)
	if err != nil {
		return nil, err
	}

	// Get stats for each release
	releaseIDs := make([]string, len(releases))
	for i, r := range releases {
		releaseIDs[i] = r.ID
	}

	// Get track counts
	trackCounts := make(map[string]int32)
	if len(releaseIDs) > 0 {
		var trackStats []struct {
			ReleaseID string
			Count     int32
		}
		if err := s.db.WithContext(ctx).
			Table("track").
			Select("release_id, COUNT(*) as count").
			Where("release_id IN ?", releaseIDs).
			Group("release_id").
			Scan(&trackStats).Error; err != nil {
			return nil, errs.Internal(fmt.Errorf("failed to get track counts: %w", err))
		}
		for _, stat := range trackStats {
			trackCounts[stat.ReleaseID] = stat.Count
		}
	}

	// Get credit counts
	creditCounts := make(map[string]int32)
	if len(releaseIDs) > 0 {
		var creditStats []struct {
			ReleaseID string
			Count     int32
		}
		if err := s.db.WithContext(ctx).
			Table("release_credit").
			Select("release_id, COUNT(*) as count").
			Where("release_id IN ?", releaseIDs).
			Group("release_id").
			Scan(&creditStats).Error; err != nil {
			return nil, errs.Internal(fmt.Errorf("failed to get credit counts: %w", err))
		}
		for _, stat := range creditStats {
			creditCounts[stat.ReleaseID] = stat.Count
		}
	}

	// Convert to proto with stats
	protoReleases := make([]*managev1.ReleaseWithStats, len(releases))
	for i, release := range releases {
		protoReleases[i] = &managev1.ReleaseWithStats{
			Release:     s.toProtoRelease(&release, artworkAssets[release.ID]),
			TrackCount:  trackCounts[release.ID],
			CreditCount: creditCounts[release.ID],
		}
	}

	return connect.NewResponse(&managev1.ListReleasesAdminResponse{
		Releases: protoReleases,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreateRelease creates a new release (admin only)
func (s *ReleaseService) CreateRelease(
	ctx context.Context,
	req *connect.Request[managev1.CreateReleaseRequest],
) (*connect.Response[managev1.Release], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Release content Block store is not configured")
	}
	// Validate required fields
	title := strings.TrimSpace(req.Msg.Title)
	if title == "" {
		return nil, errs.Required("title")
	}

	release := model.Release{
		Type:   releaseTypeToString(req.Msg.Type),
		Status: managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(),
	}

	if req.Msg.Slug != nil {
		if err := validateSlugWithoutSlash(*req.Msg.Slug); err != nil {
			return nil, err
		}
		if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "release", "releases", *req.Msg.Slug); err != nil {
			return nil, err
		}
		release.Slug = req.Msg.Slug
	}
	if req.Msg.ReleaseDate != nil {
		t := req.Msg.ReleaseDate.AsTime()
		release.ReleaseDate = &t
	}
	if req.Msg.SpotifyUrl != nil {
		release.SpotifyURL = req.Msg.SpotifyUrl
	}
	if req.Msg.AppleMusicUrl != nil {
		release.AppleMusicURL = req.Msg.AppleMusicUrl
	}
	if req.Msg.BandcampUrl != nil {
		release.BandcampURL = req.Msg.BandcampUrl
	}
	if req.Msg.YoutubeMusicUrl != nil {
		release.YoutubeMusicURL = req.Msg.YoutubeMusicUrl
	}
	if req.Msg.CatalogNumber != nil {
		release.CatalogNumber = req.Msg.CatalogNumber
	}
	createCan, err := policyv1.Release.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		if release.Slug != nil && *release.Slug != "" {
			if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "release", "releases", *release.Slug); err != nil {
				return err
			}
		}
		// A new Release has no row to lock yet. Recheck the canonical principal
		// immediately before its first durable state change instead.
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, createCan); err != nil {
			return err
		}
		sourceLocale := resolveInitialSourceLocale(ctx, tx, s.kratosClient, req.Header().Get("Accept-Language"))
		release.SourceLocale = sourceLocale
		document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
			Profile:      releaseContentProfile,
			SourceLocale: sourceLocale,
		})
		if err != nil {
			return normalizeReleaseContentBlockError(err)
		}
		contentDocumentID := document.Document.ID.String()
		release.ContentDocumentID = &contentDocumentID
		if err := tx.
			Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"}}}).
			Create(&release).Error; err != nil {
			return err
		}
		_, err = initializeReleaseContentDocument(
			ctx,
			tx,
			s.contentBlocks,
			release.ID,
			sourceLocale,
			document,
			req.Msg.Document,
			func(context.Context, *gorm.DB) error { return nil },
		)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := saveReleaseSourceLocaleDocumentState(ctx, tx, release.ID, sourceLocale, translationLocaleDocumentSaveInput{
			Title:               &title,
			OverwriteNullFields: true,
			Now:                 now,
		}); err != nil {
			return err
		}
		if err := touchReleaseSourceLocaleState(ctx, tx, release.ID, now); err != nil {
			return err
		}
		touchPolicy, err := policyv1.Release.TouchPolicy(release.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := policyv1.Release.DeletePolicy(release.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{touchPolicy},
			[]policyv1.RelationshipMutation{deletePolicy},
		); err != nil {
			return err
		}
		if err := s.appendReleaseCreatedAudit(ctx, tx, release.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, errs.SlugAlreadyExists("release", "slug")
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	if err := s.overlayReleaseSourceLocaleDocument(ctx, &release); err != nil {
		return nil, err
	}
	return s.releaseResponseWithArtworkOg(&release, nil)
}

func (s *ReleaseService) cleanupReleaseTrackUploadSessions(ctx context.Context, releaseID string) error {
	for range MaxTrackUploadCleanupPasses {
		var trackIDs []string
		var sessionsRemaining bool
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if _, err := lockReleaseForUpdate(ctx, tx, releaseID); err != nil {
				return err
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Model(&model.Track{}).
				Where("release_id = ?", releaseID).
				Pluck("id", &trackIDs).Error; err != nil {
				return err
			}
			if err := validateReleaseCascadeAuthorizationBatchSize(len(trackIDs)); err != nil {
				return err
			}
			if len(trackIDs) == 0 {
				return nil
			}
			var count int64
			if err := tx.Model(&model.UploadSession{}).
				Where("upload_type = ? AND entity_id IN ?", managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackIDs).
				Count(&count).Error; err != nil {
				return err
			}
			sessionsRemaining = count != 0
			return nil
		}); err != nil {
			return err
		}
		if !sessionsRemaining {
			return nil
		}
		for _, trackID := range trackIDs {
			if err := s.trackUploadSessionCleaner.CleanupTrackUploadSessions(ctx, trackID, "Release deleted"); err != nil {
				return err
			}
		}
	}
	return ErrTrackUploadSessionsChanged
}

// DeleteRelease deletes a Release and every cascade-owned Track (Site Admin only).
func (s *ReleaseService) DeleteRelease(
	ctx context.Context,
	req *connect.Request[managev1.DeleteReleaseRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Release content Block store is not configured")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := requireLockedReleaseAction(ctx, tx, s.spiceDB, req.Msg.Id, releaseActionDelete)
		return err
	}); err != nil {
		return nil, err
	}

	var trackIDs []string
	deleted := false
	for range MaxTrackUploadCleanupPasses {
		if err := s.cleanupReleaseTrackUploadSessions(ctx, req.Msg.Id); err != nil {
			if errors.Is(err, ErrTrackUploadSessionNotAbortable) {
				return nil, errs.FailedPrecondition("track audio upload is finalizing; retry its completion before deleting the release")
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.NotFound("release", req.Msg.Id)
			}
			if connect.CodeOf(err) != connect.CodeUnknown {
				return nil, err
			}
			return nil, errs.Internal(fmt.Errorf("cleanup release track audio uploads: %w", err))
		}

		var err error
		_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(
			tx *gorm.DB,
			write authzmutation.WriteRelationships,
		) error {
			if _, err := lockReleaseForUpdate(ctx, tx, req.Msg.Id); err != nil {
				return err
			}
			trackIDs = nil
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Model(&model.Track{}).
				Where("release_id = ?", req.Msg.Id).
				Pluck("id", &trackIDs).Error; err != nil {
				return err
			}
			if err := validateReleaseCascadeAuthorizationBatchSize(len(trackIDs)); err != nil {
				return err
			}
			if len(trackIDs) > 0 {
				var count int64
				if err := tx.Model(&model.UploadSession{}).
					Where("upload_type = ? AND entity_id IN ?", managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackIDs).
					Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return ErrTrackUploadSessionsChanged
				}
			}
			if err := tx.
				Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), req.Msg.Id).
				Delete(&model.ShareLink{}).Error; err != nil {
				return errs.Internal(err)
			}
			if err := s.og.CancelAndRelease(ctx, tx, req.Msg.Id); err != nil {
				return err
			}
			if err := s.assets.ReleaseArtwork(ctx, tx, req.Msg.Id); err != nil {
				return err
			}
			documentID, err := loadReleaseContentDocumentID(ctx, tx, req.Msg.Id)
			if err != nil {
				return err
			}
			if err := s.contentBlocks.DeleteDocument(
				ctx,
				tx,
				documentID,
				releaseContentDocumentFence(req.Msg.Id, func(context.Context, *gorm.DB) error { return nil }),
			); err != nil {
				return normalizeReleaseContentBlockError(err)
			}
			result := tx.Delete(&model.Release{}, "id = ?", req.Msg.Id)
			if result.Error != nil {
				return errs.Internal(result.Error)
			}
			if result.RowsAffected == 0 {
				return errs.NotFound("release", req.Msg.Id)
			}
			removeReleasePolicy, err := policyv1.Release.DeletePolicy(req.Msg.Id)
			if err != nil {
				return err
			}
			restoreReleasePolicy, err := policyv1.Release.TouchPolicy(req.Msg.Id)
			if err != nil {
				return err
			}
			apply := make([]policyv1.RelationshipMutation, 0, len(trackIDs)+1)
			compensate := make([]policyv1.RelationshipMutation, 0, len(trackIDs)+1)
			apply = append(apply, removeReleasePolicy)
			compensate = append(compensate, restoreReleasePolicy)
			for _, trackID := range trackIDs {
				removePolicy, err := policyv1.Track.DeletePolicy(trackID)
				if err != nil {
					return err
				}
				restorePolicy, err := policyv1.Track.TouchPolicy(trackID)
				if err != nil {
					return err
				}
				apply = append(apply, removePolicy)
				compensate = append(compensate, restorePolicy)
			}
			if err := write(apply, compensate); err != nil {
				return err
			}
			return s.appendReleaseDeletedAudit(ctx, tx, req.Msg.Id)
		})
		if errors.Is(err, ErrTrackUploadSessionsChanged) {
			continue
		}
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.NotFound("release", req.Msg.Id)
			}
			if connect.CodeOf(err) != connect.CodeUnknown {
				return nil, err
			}
			return nil, errs.Internal(err)
		}
		deleted = true
		break
	}
	if !deleted {
		return nil, errs.FailedPrecondition("track audio uploads changed during deletion; retry the request")
	}
	if err := s.waveformJobs.CancelTracks(
		ctx,
		trackIDs,
		managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED,
	); err != nil {
		slog.Warn("Failed to cancel waveform jobs for deleted release", "releaseId", req.Msg.Id, "error", err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// =============================================================================
// Publish/Unpublish (admin only)
// =============================================================================

// PublishRelease publishes a release (admin or owner only)
func (s *ReleaseService) PublishRelease(
	ctx context.Context,
	req *connect.Request[managev1.PublishReleaseRequest],
) (*connect.Response[managev1.ReleaseLifecycleMutationResponse], error) {
	var release model.Release
	if err := s.db.WithContext(ctx).First(&release, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("release", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	now := time.Now().UTC()
	transitioned := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockReleaseForUpdate(ctx, tx, release.ID)
		if err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, release.ID, releaseActionPublish); err != nil {
			return err
		}
		if locked.Status == managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String() {
			release = *locked
			return nil
		}
		if err := tx.Model(&model.Release{}).Where("id = ?", release.ID).Updates(structured.Fields{"status": managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String(), "published_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		transitioned = true
		return s.appendReleaseLifecycleAudit(ctx, tx, release.ID, releaseAuditState(locked.Status), sharedtelemetry.AuditStatePublished)
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if transitioned {
		release.Status = managev1.ReleaseStatus_RELEASE_STATUS_PUBLISHED.String()
		release.PublishedAt = &now
		release.UpdatedAt = now
	}
	publishContentUpdatedEvent(
		ctx,
		s.asyncPublisher,
		buildReleaseStateTransitionContentUpdatedEvent(
			release.ID,
			[]string{"state.status", "state.published_at"},
		),
	)
	return connect.NewResponse(&managev1.ReleaseLifecycleMutationResponse{
		Id: release.ID, Changed: transitioned,
		Status:      managev1.ReleaseStatus(managev1.ReleaseStatus_value[release.Status]),
		PublishedAt: timestampProtoPtr(release.PublishedAt), UpdatedAt: timestamppb.New(release.UpdatedAt),
	}), nil
}

// UnpublishRelease unpublishes a release (admin or owner only)
func (s *ReleaseService) UnpublishRelease(
	ctx context.Context,
	req *connect.Request[managev1.UnpublishReleaseRequest],
) (*connect.Response[managev1.ReleaseLifecycleMutationResponse], error) {
	var release model.Release
	if err := s.db.WithContext(ctx).First(&release, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("release", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	now := time.Now().UTC()
	transitioned := false
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockReleaseForUpdate(ctx, tx, release.ID)
		if err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, release.ID, releaseActionPublish); err != nil {
			return err
		}
		if locked.Status == managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String() {
			release = *locked
			return nil
		}
		if err := tx.Model(&model.Release{}).Where("id = ?", release.ID).Updates(structured.Fields{"status": managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String(), "updated_at": now}).Error; err != nil {
			return err
		}
		transitioned = true
		return s.appendReleaseLifecycleAudit(ctx, tx, release.ID, releaseAuditState(locked.Status), sharedtelemetry.AuditStateDraft)
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if transitioned {
		release.Status = managev1.ReleaseStatus_RELEASE_STATUS_DRAFT.String()
		release.UpdatedAt = now
	}
	publishContentUpdatedEvent(
		ctx,
		s.asyncPublisher,
		buildReleaseStateTransitionContentUpdatedEvent(
			release.ID,
			[]string{"state.status"},
		),
	)
	return connect.NewResponse(&managev1.ReleaseLifecycleMutationResponse{
		Id: release.ID, Changed: transitioned,
		Status:      managev1.ReleaseStatus(managev1.ReleaseStatus_value[release.Status]),
		PublishedAt: timestampProtoPtr(release.PublishedAt), UpdatedAt: timestamppb.New(release.UpdatedAt),
	}), nil
}

// =============================================================================
// Artwork Management (Site Admin only)
// =============================================================================

// SetReleaseArtwork sets the release artwork
