package artist

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ArtistService implements the ArtistService Connect handler
type ArtistService struct {
	managev1connect.UnimplementedArtistServiceHandler
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	kratosClient   auth.IdentityManager
	fileService    FileDeleter
	asyncPublisher AsyncPublisher
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
	runtime        Runtime
	translation    Translation
	members        MemberProjection
}

type ArtistServiceOption func(*ArtistService)

func WithArtistContentBlockStore(store *contentblock.Store) ArtistServiceOption {
	return func(service *ArtistService) {
		service.contentBlocks = store
	}
}

func NewAuditedArtistService(db *gorm.DB, spiceDB *auth.SpiceDBClient, kratosClient auth.IdentityManager, fileService FileDeleter, asyncPublisher AsyncPublisher, auditWriter domainaudit.Appender, dependencies Dependencies, options ...ArtistServiceOption) *ArtistService {
	if auditWriter == nil {
		panic("artist audit writer is required")
	}
	service := NewArtistService(db, spiceDB, kratosClient, fileService, asyncPublisher, dependencies, options...)
	service.auditWriter = auditWriter
	return service
}

// NewArtistService creates a new ArtistService
func NewArtistService(db *gorm.DB, spiceDB *auth.SpiceDBClient, kratosClient auth.IdentityManager, fileService FileDeleter, asyncPublisher AsyncPublisher, dependencies Dependencies, options ...ArtistServiceOption) *ArtistService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	if kratosClient == nil {
		panic("kratosClient is required")
	}
	if fileService == nil {
		panic("fileService is required")
	}
	if asyncPublisher == nil {
		panic("asyncPublisher is required")
	}
	if dependencies.Runtime == nil {
		panic("ArtistService: runtime is required")
	}
	if dependencies.Translation == nil {
		panic("ArtistService: translation is required")
	}
	if dependencies.Members == nil {
		panic("ArtistService: Member projection is required")
	}
	service := &ArtistService{
		db: db, spiceDB: spiceDB, kratosClient: kratosClient,
		fileService: fileService, asyncPublisher: asyncPublisher, runtime: dependencies.Runtime,
		translation: dependencies.Translation, members: dependencies.Members,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// ==================== Public APIs (published only) ====================

// GetArtist retrieves an artist by ID
// Published artists are public, draft artists require admin/owner/editor permission
// GetArtistBySlug retrieves an artist by slug
// Published artists are public, draft artists require admin/owner/editor permission
// GetArtistEditorData returns editor initial data in one request.
// Draft artists require admin/manager permission.
// Draft labels are always excluded.
func (s *ArtistService) GetArtistEditorData(
	ctx context.Context,
	req *connect.Request[managev1.GetArtistEditorDataRequest],
) (*connect.Response[managev1.GetArtistEditorDataResponse], error) {
	var artist model.Artist
	if err := s.db.WithContext(ctx).
		Where("id = ?", req.Msg.Id).
		First(&artist).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("artist", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	actions, err := artistAllowedActions(ctx, s.spiceDB, artist.ID, artist.Status)
	if err != nil {
		return nil, err
	}
	if auth.GetUser(ctx) == nil {
		return nil, errs.AuthenticationRequired()
	}
	if !hasArtistAction(actions, managev1.ArtistAction_ARTIST_ACTION_EDIT) {
		return nil, errs.NotFound("artist", artist.ID)
	}

	labelIDs, err := s.getArtistLabelIDs(ctx, artist.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}

	if err := s.overlayArtistSourceLocaleDocument(ctx, &artist); err != nil {
		return nil, err
	}
	images, err := loadArtistImages(ctx, s.runtime, s.db, artist.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, artist.OgAssetID)
	if err != nil {
		return nil, err
	}
	protoArtist := s.toProtoArtist(&artist, nil, ogAsset)
	applyManageArtistImages(protoArtist, images)
	return connect.NewResponse(&managev1.GetArtistEditorDataResponse{
		Artist:         protoArtist,
		LabelIds:       labelIDs,
		AllowedActions: actions,
		ImageRevision:  artistImageRevision(images),
	}), nil
}

// ListArtists returns a paginated list of published artists
func (s *ArtistService) ListArtists(
	ctx context.Context,
	req *connect.Request[managev1.ListArtistsRequest],
) (*connect.Response[managev1.ListArtistsResponse], error) {
	var artists []model.Artist
	var total int64

	query := s.db.WithContext(ctx).
		Model(&model.Artist{}).
		Where("status = ?", managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String())

	// Handle label_id filter separately (requires JOIN query)
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "label_id" && f.GetValue() != "" {
			query = query.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
		} else {
			remainingFilters = append(remainingFilters, f)
		}
	}

	// Apply filters using FilterConfig
	var err error
	query, err = ArtistFilterConfig.ApplyFilters(query, remainingFilters)
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
	query, err = artistSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&artists).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayArtistSourceLocaleDocuments(ctx, artists); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadyArtistOgAssets(ctx, artists)
	if err != nil {
		return nil, err
	}
	artistIDs := collectManageArtistIDs(artists)
	readyImages, err := s.runtime.LoadArtistImages(ctx, s.db, artistIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Convert to proto
	protoArtists := make([]*managev1.Artist, len(artists))
	for i, artist := range artists {
		protoArtists[i] = s.toProtoArtist(&artist, nil, manageOgAssetFromReadyMap(readyOgAssets, artist.OgAssetID))
		applyManageArtistImages(protoArtists[i], readyImages[artist.ID])
	}

	return connect.NewResponse(&managev1.ListArtistsResponse{
		Artists: protoArtists,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// ==================== Admin APIs ====================

// ListArtistsAdmin returns a paginated list of all artists with stats (admin only)
func (s *ArtistService) ListArtistsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListArtistsAdminRequest],
) (*connect.Response[managev1.ListArtistsAdminResponse], error) {
	// Check admin permission
	if err := requireArtistList(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	var artists []model.Artist
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Artist{})

	// Handle label_id filter separately (requires JOIN query)
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range req.Msg.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "label_id" && f.GetValue() != "" {
			query = query.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
		} else {
			remainingFilters = append(remainingFilters, f)
		}
	}

	// Apply filters using FilterConfig
	var err error
	query, err = ArtistFilterConfig.ApplyFilters(query, remainingFilters)
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
	query, err = artistSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&artists).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayArtistSourceLocaleDocuments(ctx, artists); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadyArtistOgAssets(ctx, artists)
	if err != nil {
		return nil, err
	}
	readyImages, err := s.runtime.LoadArtistImages(ctx, s.db, collectManageArtistIDs(artists))
	if err != nil {
		return nil, errs.Internal(err)
	}

	// Get release counts for all artists
	artistIDs := make([]string, len(artists))
	for i, a := range artists {
		artistIDs[i] = a.ID
	}

	var releaseCounts []struct {
		ArtistID string `gorm:"column:artist_id"`
		Count    int32  `gorm:"column:count"`
	}
	if len(artistIDs) > 0 {
		if err := s.db.WithContext(ctx).
			Table("release_artist").
			Select("artist_id, COUNT(DISTINCT release_id) as count").
			Where("artist_id IN ?", artistIDs).
			Group("artist_id").
			Scan(&releaseCounts).Error; err != nil {
			return nil, errs.Internal(err)
		}
	}

	countMap := make(map[string]int32)
	for _, rc := range releaseCounts {
		countMap[rc.ArtistID] = rc.Count
	}

	// Convert to proto
	protoArtists := make([]*managev1.ArtistWithStats, len(artists))
	for i, artist := range artists {
		protoArtist := s.toProtoArtist(&artist, nil, manageOgAssetFromReadyMap(readyOgAssets, artist.OgAssetID))
		applyManageArtistImages(protoArtist, readyImages[artist.ID])
		protoArtists[i] = &managev1.ArtistWithStats{
			Artist:       protoArtist,
			ReleaseCount: countMap[artist.ID],
		}
	}

	return connect.NewResponse(&managev1.ListArtistsAdminResponse{
		Artists: protoArtists,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreateArtist creates a new Artist and its first durable Owner atomically.
func (s *ArtistService) CreateArtist(
	ctx context.Context,
	req *connect.Request[managev1.CreateArtistRequest],
) (*connect.Response[managev1.Artist], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Artist content Block store is not configured")
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, errs.Required("name")
	}
	if err := s.validateNewArtistSlug(ctx, req.Msg.Slug); err != nil {
		return nil, err
	}
	artist := newArtistFromRequest(req.Msg, name)
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		return s.createArtistWithDB(
			ctx, tx, &artist, req.Msg, user.MemberID.String(), req.Header().Get("Accept-Language"), write,
		)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	if err := s.overlayArtistSourceLocaleDocument(ctx, &artist); err != nil {
		return nil, err
	}
	return s.artistResponseWithReadyOg(ctx, &artist, nil)
}

func (s *ArtistService) validateNewArtistSlug(ctx context.Context, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	if err := s.checkSlugAvailable(ctx, *slug, ""); err != nil {
		return err
	}
	return s.runtime.EnsureResourceRouteAvailable(ctx, s.db, "artist", "artists", *slug)
}

func newArtistFromRequest(request *managev1.CreateArtistRequest, name string) model.Artist {
	artist := model.Artist{
		Name: name, Status: managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String(),
		RealName: request.RealName, CountryCode: request.CountryCode,
		Website: request.Website, SocialLinks: request.SocialLinks,
	}
	if request.Slug != nil && *request.Slug != "" {
		artist.Slug = request.Slug
	}
	if request.ParentArtistId != nil && *request.ParentArtistId != "" {
		artist.ParentArtistID = request.ParentArtistId
	}
	return artist
}

func (s *ArtistService) createArtistWithDB(
	ctx context.Context,
	tx *gorm.DB,
	artist *model.Artist,
	request *managev1.CreateArtistRequest,
	ownerMemberID string,
	acceptLanguage string,
	write authzmutation.WriteRelationships,
) error {
	createCan, err := policyv1.Artist.Create()
	if err != nil {
		return errs.Internal(err)
	}
	// A new Artist has no resource row to lock yet. Lock and recheck the
	// canonical principal before the first durable state change; the lock is
	// retained for the rest of this transaction.
	if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, createCan); err != nil {
		return err
	}
	if err := requireArtistRouteAvailableWithDB(ctx, s.runtime, tx, artist.Slug); err != nil {
		return err
	}
	sourceLocale := s.translation.ResolveInitialSourceLocale(ctx, tx, s.kratosClient, acceptLanguage)
	artist.SourceLocale = sourceLocale
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      creativeContentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	contentDocumentID := document.Document.ID.String()
	artist.ContentDocumentID = &contentDocumentID
	if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}}).
		Create(artist).Error; err != nil {
		return err
	}
	if artist.ParentArtistID != nil {
		if err := validateArtistParentTransition(ctx, tx, artist.ID, *artist.ParentArtistID); err != nil {
			return err
		}
	}
	_, err = initializeCreativeContentDocument(
		ctx,
		tx,
		s.contentBlocks,
		artistContentEntity,
		artist.ID,
		sourceLocale,
		document,
		request.Document,
		func(context.Context, *gorm.DB) error { return nil },
	)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := s.createArtistSourceLocaleWithDB(
		ctx,
		tx,
		artist,
		sourceLocale,
		now,
	); err != nil {
		return err
	}
	if err := insertArtistOwner(tx, artist.ID, ownerMemberID, now); err != nil {
		return err
	}
	owner, err := authorizationtarget.RequireLocked(ctx, tx, ownerMemberID)
	if err != nil {
		return err
	}
	_, err = s.runtime.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST,
		artist.ID, "", false, "artist_created",
	)
	if err != nil {
		return err
	}
	if err := s.appendArtistCreatedAudit(ctx, tx, artist.ID); err != nil {
		return err
	}
	apply, compensate, err := createArtistRelationships(artist.ID, owner.IdentityID, artist.ParentArtistID)
	if err != nil {
		return errs.Internal(err)
	}
	return write(apply, compensate)
}

func requireArtistRouteAvailableWithDB(ctx context.Context, runtime Runtime, tx *gorm.DB, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	return runtime.EnsureResourceRouteAvailableInTx(ctx, tx, "artist", "artists", *slug)
}

func (s *ArtistService) createArtistSourceLocaleWithDB(
	ctx context.Context,
	tx *gorm.DB,
	artist *model.Artist,
	sourceLocale string,
	now time.Time,
) error {
	if err := saveArtistSourceLocaleDocumentState(ctx, tx, artist.ID, sourceLocale, translationLocaleDocumentSaveInput{
		SourceLocale: sourceLocale, Title: &artist.Name, Now: now,
	}); err != nil {
		return err
	}
	return touchArtistRootUpdatedAt(ctx, tx, artist.ID, now)
}

func insertArtistOwner(tx *gorm.DB, artistID, memberID string, now time.Time) error {
	return tx.Exec(`
		INSERT INTO artist_owner (artist_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, artistID, memberID, now).Error
}

// UpdateArtist updates an existing artist (admin or editor)
// DeleteArtist deletes an artist (owner or admin only)
func (s *ArtistService) DeleteArtist(
	ctx context.Context,
	req *connect.Request[managev1.DeleteArtistRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Artist content Block store is not configured")
	}
	if strings.TrimSpace(req.Msg.ExpectedRevision) == "" {
		return nil, errs.Required("expected_revision")
	}

	snapshotPlan, err := policyv1.Artist.Snapshot(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", "must identify an Artist")
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := lockArtistParticipantRoot(ctx, tx, req.Msg.Id); err != nil {
			return err
		}
		// Serialize the SpiceDB relationship snapshot with every Artist
		// reparent. The root lock is acquired first everywhere to avoid a
		// delete/reparent lock-order inversion for the same Artist.
		if err := lockArtistParentGraph(ctx, tx); err != nil {
			return err
		}
		if err := requireLockedArtistPermission(ctx, tx, s.spiceDB, req.Msg.Id, policyv1.Artist.Delete); err != nil {
			return err
		}
		impacts, err := loadArtistDeletionImpacts(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if artistDeletionRevision(impacts) != req.Msg.ExpectedRevision {
			return errs.FailedPrecondition("Artist relations changed; preview deletion again")
		}
		snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		deleteRelationships, restoreRelationships, err := artistDeletionRelationshipMutations(req.Msg.Id, snapshots)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		if err := validateResourceDeletionAuthorizationBatchSize("artist", deleteRelationships, restoreRelationships); err != nil {
			return err
		}
		if err := tx.
			Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST.String(), req.Msg.Id).
			Delete(&model.ShareLink{}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.runtime.CancelAndReleaseEntityWithDB(
			ctx, tx,
			managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST,
			"artist", req.Msg.Id,
		); err != nil {
			return err
		}
		if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "artist", req.Msg.Id, "image"); err != nil {
			return err
		}
		documentID, err := loadCreativeContentDocumentID(ctx, tx, artistContentEntity, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			documentID,
			creativeContentDocumentFence(artistContentEntity, req.Msg.Id, func(context.Context, *gorm.DB) error { return nil }),
		); err != nil {
			return normalizeCreativeContentBlockError(artistContentEntity, err)
		}
		result := tx.Delete(&model.Artist{}, "id = ?", req.Msg.Id)
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected == 0 {
			return errs.NotFound("artist", req.Msg.Id)
		}
		if err := s.appendArtistDeletedAudit(ctx, tx, req.Msg.Id); err != nil {
			return err
		}
		return write(deleteRelationships, restoreRelationships)
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// PublishArtist publishes an artist (Owner or Site Admin).
func (s *ArtistService) PublishArtist(
	ctx context.Context,
	req *connect.Request[managev1.PublishArtistRequest],
) (*connect.Response[managev1.ArtistLifecycleMutationResponse], error) {
	var artist model.Artist
	transitioned := false
	now := time.Now()
	updates := structured.Fields{
		"status":       managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String(),
		"published_at": now,
		"updated_at":   now,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockArtistParticipantRoot(ctx, tx, req.Msg.Id); err != nil {
			return err
		}
		if err := requireLockedArtistPermission(ctx, tx, s.spiceDB, req.Msg.Id, policyv1.Artist.Publish); err != nil {
			return err
		}
		if err := tx.First(&artist, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		if artist.Status == managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String() {
			return nil
		}
		if err := tx.Model(&artist).Updates(updates).Error; err != nil {
			return err
		}
		transitioned = true
		return s.appendArtistLifecycleAudit(ctx, tx, artist.ID, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStatePublished)
	}); err != nil {
		return nil, err
	}

	if transitioned {
		artist.Status = managev1.ArtistStatus_ARTIST_STATUS_PUBLISHED.String()
		artist.PublishedAt = &now
		artist.UpdatedAt = &now
	}
	if transitioned {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildManageStateTransitionContentUpdatedEvent(managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST, artist.ID, []string{"state.status", "state.published_at"}))
	}
	return connect.NewResponse(&managev1.ArtistLifecycleMutationResponse{
		Id: artist.ID, Changed: transitioned,
		Status:      managev1.ArtistStatus(managev1.ArtistStatus_value[artist.Status]),
		PublishedAt: timestampProtoPtr(artist.PublishedAt), UpdatedAt: timestampProtoPtr(artist.UpdatedAt),
	}), nil
}

// UnpublishArtist unpublishes an artist (Owner or Site Admin).
func (s *ArtistService) UnpublishArtist(
	ctx context.Context,
	req *connect.Request[managev1.UnpublishArtistRequest],
) (*connect.Response[managev1.ArtistLifecycleMutationResponse], error) {
	var artist model.Artist
	transitioned := false
	now := time.Now()
	updates := structured.Fields{
		"status":     managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String(),
		"updated_at": now,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockArtistParticipantRoot(ctx, tx, req.Msg.Id); err != nil {
			return err
		}
		if err := requireLockedArtistPermission(ctx, tx, s.spiceDB, req.Msg.Id, policyv1.Artist.Publish); err != nil {
			return err
		}
		if err := tx.First(&artist, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		if artist.Status == managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String() {
			return nil
		}
		if err := tx.Model(&artist).Updates(updates).Error; err != nil {
			return err
		}
		transitioned = true
		return s.appendArtistLifecycleAudit(ctx, tx, artist.ID, sharedtelemetry.AuditStatePublished, sharedtelemetry.AuditStateDraft)
	}); err != nil {
		return nil, err
	}

	if transitioned {
		artist.Status = managev1.ArtistStatus_ARTIST_STATUS_DRAFT.String()
		artist.UpdatedAt = &now
	}
	if transitioned {
		publishContentUpdatedEvent(ctx, s.asyncPublisher, buildManageStateTransitionContentUpdatedEvent(managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST, artist.ID, []string{"state.status"}))
	}
	return connect.NewResponse(&managev1.ArtistLifecycleMutationResponse{
		Id: artist.ID, Changed: transitioned,
		Status:      managev1.ArtistStatus(managev1.ArtistStatus_value[artist.Status]),
		PublishedAt: timestampProtoPtr(artist.PublishedAt), UpdatedAt: timestampProtoPtr(artist.UpdatedAt),
	}), nil
}

// ==================== Image Management ====================

// SetArtistImage sets the artist's profile image (admin or manager)
