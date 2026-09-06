package label

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type labelDeletionImpactRow struct {
	Kind        int32  `gorm:"column:kind"`
	EntityID    string `gorm:"column:entity_id"`
	DisplayName string `gorm:"column:display_name"`
}

func loadLabelDeletionImpacts(ctx context.Context, db *gorm.DB, labelID string) ([]labelDeletionImpactRow, error) {
	var rows []labelDeletionImpactRow
	query := fmt.Sprintf(`
		SELECT 1::integer AS kind, child.id::text AS entity_id, %s AS display_name
		FROM label AS child
		WHERE child.parent_label_id = ?::uuid
		UNION ALL
		SELECT 2::integer, artist.id::text, %s
		FROM artist_label AS relation
		JOIN artist ON artist.id = relation.artist_id
		WHERE relation.label_id = ?::uuid
		UNION ALL
		SELECT 3::integer, event.id::text, COALESCE(NULLIF(event.title, ''), event.slug)
		FROM program_event_label AS relation
		JOIN program_event AS event ON event.id = relation.event_id
		WHERE relation.label_id = ?::uuid
		UNION ALL
		SELECT 4::integer, release.id::text, %s
		FROM release_label AS relation
		JOIN release ON release.id = relation.release_id
		WHERE relation.label_id = ?::uuid
		ORDER BY 1, 2
	`, LabelSourceTitleSQL("child"), ArtistSourceTitleSQL("artist"), ReleaseSourceTitleSQL("release"))
	if err := db.WithContext(ctx).Raw(query, labelID, labelID, labelID, labelID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func labelDeletionRevision(rows []labelDeletionImpactRow) string {
	hash := sha256.New()
	for _, row := range rows {
		_, _ = fmt.Fprintf(hash, "%d\x00%s\n", row.Kind, row.EntityID)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func labelDeletionPreview(rows []labelDeletionImpactRow) *managev1.PreviewDeleteLabelResponse {
	response := &managev1.PreviewDeleteLabelResponse{Revision: labelDeletionRevision(rows)}
	for _, row := range rows {
		kind := managev1.LabelDeletionImpactKind(row.Kind)
		response.Impacts = append(response.Impacts, &managev1.LabelDeletionImpact{
			Kind: kind, EntityId: row.EntityID, DisplayName: row.DisplayName,
		})
		switch kind {
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_CHILD_LABEL:
			response.ChildLabelCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_ARTIST:
			response.ArtistCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_PROGRAM_EVENT:
			response.ProgramEventCount++
		case managev1.LabelDeletionImpactKind_LABEL_DELETION_IMPACT_KIND_RELEASE:
			response.ReleaseCount++
		}
	}
	return response
}

// labelSortConfig defines allowed sort fields for labels
var labelSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         LabelSourceTitleSQL("label"),
		"slug":         "slug",
		"status":       "status",
		"country_code": "country_code",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
	},
	DefaultSort: LabelSourceTitleSQL("label") + " ASC",
}

// LabelService implements the LabelService Connect handler
type LabelService struct {
	managev1connect.UnimplementedLabelServiceHandler
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

type LabelServiceOption func(*LabelService)

func WithLabelContentBlockStore(store *contentblock.Store) LabelServiceOption {
	return func(service *LabelService) {
		service.contentBlocks = store
	}
}

func NewAuditedLabelService(db *gorm.DB, spiceDB *auth.SpiceDBClient, kratosClient auth.IdentityManager, fileService FileDeleter, asyncPublisher AsyncPublisher, auditWriter domainaudit.Appender, dependencies Dependencies, options ...LabelServiceOption) *LabelService {
	if auditWriter == nil {
		panic("label audit writer is required")
	}
	service := NewLabelService(db, spiceDB, kratosClient, fileService, asyncPublisher, dependencies, options...)
	service.auditWriter = auditWriter
	return service
}

// NewLabelService creates a new LabelService
func NewLabelService(db *gorm.DB, spiceDB *auth.SpiceDBClient, kratosClient auth.IdentityManager, fileService FileDeleter, asyncPublisher AsyncPublisher, dependencies Dependencies, options ...LabelServiceOption) *LabelService {
	if db == nil {
		panic("LabelService: db is required")
	}
	if spiceDB == nil {
		panic("LabelService: spiceDB is required")
	}
	if kratosClient == nil {
		panic("LabelService: kratosClient is required")
	}
	if fileService == nil {
		panic("LabelService: fileService is required")
	}
	if asyncPublisher == nil {
		panic("LabelService: asyncPublisher is required")
	}
	if dependencies.Translation == nil {
		panic("LabelService: translation is required")
	}
	if dependencies.Members == nil {
		panic("LabelService: Member projection is required")
	}
	if dependencies.Runtime == nil {
		panic("LabelService: media and OG runtime is required")
	}
	service := &LabelService{
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

// =============================================================================
// Public Read Methods (published labels only)
// =============================================================================

// GetLabel retrieves a label by ID
// Published labels are accessible to everyone, draft labels require admin or manager permission
func (s *LabelService) GetLabelEditorData(
	ctx context.Context,
	req *connect.Request[managev1.GetLabelEditorDataRequest],
) (*connect.Response[managev1.GetLabelEditorDataResponse], error) {
	var label model.Label
	if err := s.db.WithContext(ctx).
		Where("id = ?", req.Msg.Id).
		First(&label).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("label", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	if err := requireLabelView(ctx, s.spiceDB, label.ID); err != nil {
		return nil, err
	}
	actions, err := labelAllowedActions(ctx, s.spiceDB, label.ID, label.Status)
	if err != nil {
		return nil, err
	}
	if err := s.overlayLabelSourceLocaleDocument(ctx, &label); err != nil {
		return nil, err
	}

	imageAssets := s.getLabelImageAssets(ctx, label.ID)
	response, err := s.labelResponseWithReadyOg(ctx, &label, imageAssets)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetLabelEditorDataResponse{
		Label:          response.Msg,
		AllowedActions: actions,
	}), nil
}

// GetLabelBySlug retrieves a label by slug
// Published labels are accessible to everyone, draft labels require admin or manager permission
// ListLabels returns a paginated list of published labels
func (s *LabelService) ListLabels(
	ctx context.Context,
	req *connect.Request[managev1.ListLabelsRequest],
) (*connect.Response[managev1.ListLabelsResponse], error) {
	var labels []model.Label
	var total int64

	// This selector surface is published-only. Draft access belongs to exact
	// resource reads with an authorization check or to the admin list.
	query := s.db.WithContext(ctx).
		Model(&model.Label{}).
		Where("status = ?", managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String())

	// Apply filters using FilterConfig
	query, err := LabelFilterConfig.ApplyFilters(query, req.Msg.Filters)
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
	query, err = labelSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&labels).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayLabelSourceLocaleDocuments(ctx, labels); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadyLabelOgAssets(ctx, labels)
	if err != nil {
		return nil, err
	}
	logoAssets, err := s.loadReadyLabelLogoAssets(ctx, labels)
	if err != nil {
		return nil, err
	}

	// Convert to proto
	protoLabels := make([]*managev1.Label, len(labels))
	for i, label := range labels {
		imageAssets := labelImageAssetsFromReadyMap(&label, logoAssets)
		protoLabels[i] = s.toProtoLabel(&label, imageAssets, manageOgAssetFromReadyMap(readyOgAssets, label.OgAssetID))
	}

	return connect.NewResponse(&managev1.ListLabelsResponse{
		Labels: protoLabels,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// =============================================================================
// Admin Methods (all labels, requires admin role)
// =============================================================================

// ListLabelsAdmin returns a paginated list of all labels with stats
func (s *LabelService) ListLabelsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListLabelsAdminRequest],
) (*connect.Response[managev1.ListLabelsAdminResponse], error) {
	// Check admin role
	if err := requireLabelList(ctx, s.spiceDB); err != nil {
		return nil, err
	}

	var labels []model.Label
	var total int64

	query := s.db.WithContext(ctx).Model(&model.Label{})

	// Apply filters using FilterConfig
	query, err := LabelFilterConfig.ApplyFilters(query, req.Msg.Filters)
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
	query, err = labelSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}

	if err := query.Limit(int(limit)).Offset(int(offset)).Find(&labels).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if err := s.overlayLabelSourceLocaleDocuments(ctx, labels); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadyLabelOgAssets(ctx, labels)
	if err != nil {
		return nil, err
	}
	logoAssets, err := s.loadReadyLabelLogoAssets(ctx, labels)
	if err != nil {
		return nil, err
	}

	// Get stats for each label
	labelIDs := make([]string, len(labels))
	for i, l := range labels {
		labelIDs[i] = l.ID
	}

	// Get artist counts with proper error handling
	artistCounts := make(map[string]int32)
	if len(labelIDs) > 0 {
		var artistStats []struct {
			LabelID string
			Count   int32
		}
		if err := s.db.WithContext(ctx).
			Table("artist_label").
			Select("label_id, COUNT(*) as count").
			Where("label_id IN ?", labelIDs).
			Group("label_id").
			Scan(&artistStats).Error; err != nil {
			return nil, errs.Internal(fmt.Errorf("failed to fetch artist counts: %w", err))
		}
		for _, stat := range artistStats {
			artistCounts[stat.LabelID] = stat.Count
		}
	}

	// Get release counts with proper error handling
	releaseCounts := make(map[string]int32)
	if len(labelIDs) > 0 {
		var releaseStats []struct {
			LabelID string
			Count   int32
		}
		if err := s.db.WithContext(ctx).
			Table("release_label").
			Select("label_id, COUNT(*) as count").
			Where("label_id IN ?", labelIDs).
			Group("label_id").
			Scan(&releaseStats).Error; err != nil {
			return nil, errs.Internal(fmt.Errorf("failed to fetch release counts: %w", err))
		}
		for _, stat := range releaseStats {
			releaseCounts[stat.LabelID] = stat.Count
		}
	}

	// Convert to proto with stats
	protoLabels := make([]*managev1.LabelWithStats, len(labels))
	for i, label := range labels {
		imageAssets := labelImageAssetsFromReadyMap(&label, logoAssets)
		protoLabels[i] = &managev1.LabelWithStats{
			Label:        s.toProtoLabel(&label, imageAssets, manageOgAssetFromReadyMap(readyOgAssets, label.OgAssetID)),
			ArtistCount:  artistCounts[label.ID],
			ReleaseCount: releaseCounts[label.ID],
		}
	}

	return connect.NewResponse(&managev1.ListLabelsAdminResponse{
		Labels: protoLabels,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// CreateLabel creates a new label (admin only)
func (s *LabelService) CreateLabel(
	ctx context.Context,
	req *connect.Request[managev1.CreateLabelRequest],
) (*connect.Response[managev1.Label], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Label content Block store is not configured")
	}
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}

	// Validate required fields
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, errs.Required("name")
	}
	if err := s.validateNewLabelSlug(ctx, req.Msg.Slug); err != nil {
		return nil, err
	}
	label := newLabelFromRequest(req.Msg)
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		return s.createLabelWithDB(
			ctx, tx, &label, name, req.Msg.Document, user.MemberID.String(),
			req.Header().Get("Accept-Language"), write,
		)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, errs.AlreadyExistsMsg("label with this slug already exists")
		}
		return nil, errs.Internal(err)
	}
	if err := s.overlayLabelSourceLocaleDocument(ctx, &label); err != nil {
		return nil, err
	}

	return s.labelResponseWithReadyOg(ctx, &label, nil)
}

func (s *LabelService) validateNewLabelSlug(ctx context.Context, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	if err := s.checkSlugAvailable(ctx, *slug, ""); err != nil {
		return err
	}
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "label", "labels", *slug)
}

func newLabelFromRequest(request *managev1.CreateLabelRequest) model.Label {
	label := model.Label{
		Status: managev1.LabelStatus_LABEL_STATUS_DRAFT.String(),
		Slug:   request.Slug, SocialLinks: request.SocialLinks,
	}
	if countryCode, present := normalizeOptionalNullableString(request.CountryCode); present {
		label.CountryCode = countryCode
	}
	if website, present := normalizeOptionalNullableString(request.Website); present {
		label.Website = website
	}
	if parentLabelID, present := normalizeOptionalNullableString(request.ParentLabelId); present {
		label.ParentLabelID = parentLabelID
	}
	return label
}

func (s *LabelService) createLabelWithDB(
	ctx context.Context,
	tx *gorm.DB,
	label *model.Label,
	name string,
	documentInput *contentv1.RichTextDocument,
	ownerMemberID string,
	acceptLanguage string,
	write authzmutation.WriteRelationships,
) error {
	createCan, err := policyv1.Label.Create()
	if err != nil {
		return errs.Internal(err)
	}
	// A new Label has no resource row to lock yet. Lock and recheck the
	// canonical principal before the first durable state change; the lock is
	// retained for the rest of this transaction.
	if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, createCan); err != nil {
		return err
	}
	if err := requireLabelRouteAvailableWithDB(ctx, tx, label.Slug); err != nil {
		return err
	}
	sourceLocale := s.translation.ResolveInitialSourceLocale(ctx, tx, s.kratosClient, acceptLanguage)
	label.SourceLocale = sourceLocale
	document, err := s.contentBlocks.CreateDocument(ctx, tx, contentblock.CreateInput{
		Profile:      creativeContentProfile,
		SourceLocale: sourceLocale,
	})
	if err != nil {
		return normalizeLabelContentBlockError(err)
	}
	contentDocumentID := document.Document.ID.String()
	label.ContentDocumentID = &contentDocumentID
	if err := tx.Clauses(clause.Returning{Columns: []clause.Column{
		{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"},
	}}).Create(label).Error; err != nil {
		return err
	}
	if label.ParentLabelID != nil {
		if err := validateLabelParentTransition(ctx, tx, label.ID, *label.ParentLabelID); err != nil {
			return err
		}
	}
	if err := insertLabelOwner(tx, label.ID, ownerMemberID); err != nil {
		return err
	}
	owner, err := authorizationtarget.RequireLocked(ctx, tx, ownerMemberID)
	if err != nil {
		return err
	}
	_, err = initializeCreativeContentDocument(
		ctx,
		tx,
		s.contentBlocks,
		labelContentEntity,
		label.ID,
		sourceLocale,
		document,
		documentInput,
		func(context.Context, *gorm.DB) error { return nil },
	)
	if err != nil {
		return err
	}
	if err := s.createLabelSourceLocaleWithDB(
		ctx,
		tx,
		label.ID,
		name,
		sourceLocale,
	); err != nil {
		return err
	}
	if err := s.appendLabelCreatedAudit(ctx, tx, label.ID); err != nil {
		return err
	}
	apply, compensate, err := createLabelRelationshipMutations(label.ID, owner.IdentityID, label.ParentLabelID)
	if err != nil {
		return errs.Internal(err)
	}
	return write(apply, compensate)
}

func requireLabelRouteAvailableWithDB(ctx context.Context, tx *gorm.DB, slug *string) error {
	if slug == nil || *slug == "" {
		return nil
	}
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "label", "labels", *slug)
}

func insertLabelOwner(tx *gorm.DB, labelID, memberID string) error {
	return tx.Exec(`
		INSERT INTO label_owner (label_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, now())
	`, labelID, memberID).Error
}

func (s *LabelService) createLabelSourceLocaleWithDB(
	ctx context.Context,
	tx *gorm.DB,
	labelID string,
	name string,
	sourceLocale string,
) error {
	now := time.Now().UTC()
	if err := saveLabelSourceLocaleDocumentState(ctx, tx, labelID, sourceLocale, translationLocaleDocumentSaveInput{
		Title:               &name,
		OverwriteNullFields: true, Now: now,
	}); err != nil {
		return err
	}
	return touchLabelRootUpdatedAt(ctx, tx, labelID, now)
}

func (s *LabelService) PreviewDeleteLabel(
	ctx context.Context,
	req *connect.Request[managev1.PreviewDeleteLabelRequest],
) (*connect.Response[managev1.PreviewDeleteLabelResponse], error) {
	var label model.Label
	if err := s.db.WithContext(ctx).Select("id").First(&label, "id = ?", req.Msg.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("label", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}
	if err := requireLabelPermission(ctx, s.spiceDB, req.Msg.Id, policyv1.Label.Delete); err != nil {
		return nil, err
	}
	rows, err := loadLabelDeletionImpacts(ctx, s.db, req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(labelDeletionPreview(rows)), nil
}

// DeleteLabel deletes a label (admin or owner only)
func (s *LabelService) DeleteLabel(
	ctx context.Context,
	req *connect.Request[managev1.DeleteLabelRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if s.contentBlocks == nil {
		return nil, errs.InternalMsg("Label content Block store is not configured")
	}
	if strings.TrimSpace(req.Msg.PreviewRevision) == "" {
		return nil, errs.FailedPrecondition("delete preview is required")
	}

	var label model.Label
	if err := s.db.WithContext(ctx).
		Select("id").
		First(&label, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("label", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	snapshotPlan, err := policyv1.Label.Snapshot(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", "must identify a Label")
	}
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		// Freeze every relation represented by the preview revision until the
		// Label delete commits. Deletion is rare; one short table lock keeps the
		// exact-impact CAS honest without adding cross-domain state.
		if err := tx.Exec(`
			LOCK TABLE label, artist_label, program_event_label, release_label
			IN SHARE ROW EXCLUSIVE MODE
		`).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			First(&label, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		if err := requireLockedLabelPermission(ctx, tx, s.spiceDB, req.Msg.Id, policyv1.Label.Delete); err != nil {
			return err
		}
		impacts, err := loadLabelDeletionImpacts(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if labelDeletionRevision(impacts) != req.Msg.PreviewRevision {
			return errs.FailedPrecondition("label usage changed; review deletion impact again")
		}
		snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		deleteRelationships, restoreRelationships, err := labelDeletionRelationshipMutations(req.Msg.Id, snapshots)
		if err != nil {
			return errs.DependencyUnavailable("SpiceDB")
		}
		if err := validateResourceDeletionAuthorizationBatchSize("label", deleteRelationships, restoreRelationships); err != nil {
			return err
		}
		if err := tx.
			Where("entity_type = ? AND entity_id = ?", managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_LABEL.String(), req.Msg.Id).
			Delete(&model.ShareLink{}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.runtime.CancelAndReleaseOGWithDB(ctx, tx, req.Msg.Id); err != nil {
			return err
		}
		if err := s.runtime.ReleaseExactPublicAssetBindings(
			ctx, tx, "label", req.Msg.Id, []string{"logo:light", "logo:dark"},
		); err != nil {
			return err
		}
		documentID, err := loadLabelContentDocumentID(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := s.contentBlocks.DeleteDocument(
			ctx,
			tx,
			documentID,
			labelContentDocumentFence(req.Msg.Id, func(context.Context, *gorm.DB) error { return nil }),
		); err != nil {
			return normalizeLabelContentBlockError(err)
		}
		result := tx.Delete(&model.Label{}, "id = ?", req.Msg.Id)
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected == 0 {
			return errs.NotFound("label", req.Msg.Id)
		}
		if err := s.appendLabelDeletedAudit(ctx, tx, req.Msg.Id); err != nil {
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

// =============================================================================
// Publish/Unpublish (admin only)
// =============================================================================

// PublishLabel publishes a label (admin or owner only).
func (s *LabelService) PublishLabel(
	ctx context.Context,
	req *connect.Request[managev1.PublishLabelRequest],
) (*connect.Response[managev1.LabelLifecycleMutationResponse], error) {
	var label model.Label
	transitioned := false
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&label, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		if err := requireLockedLabelPermission(ctx, tx, s.spiceDB, req.Msg.Id, policyv1.Label.Publish); err != nil {
			return err
		}
		if label.Status == managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String() {
			return nil
		}
		if err := tx.Model(&label).Updates(structured.Fields{
			"status":       managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String(),
			"published_at": now,
			"updated_at":   now,
		}).Error; err != nil {
			return err
		}
		transitioned = true
		if err := s.appendLabelLifecycleAudit(ctx, tx, label.ID, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStatePublished); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("label", req.Msg.Id)
		}
		return nil, err
	}
	if transitioned {
		label.Status = managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String()
		label.PublishedAt = &now
		label.UpdatedAt = now
	}
	if transitioned {
		publishLabelContentUpdated(ctx, s.asyncPublisher, buildLabelStateTransitionContentUpdatedEvent(label.ID))
	}
	return connect.NewResponse(&managev1.LabelLifecycleMutationResponse{
		Id: label.ID, Changed: transitioned,
		Status:      managev1.LabelStatus(managev1.LabelStatus_value[label.Status]),
		PublishedAt: timestampProtoPtr(label.PublishedAt), UpdatedAt: timestamppb.New(label.UpdatedAt),
	}), nil
}

// UnpublishLabel unpublishes a label (admin or owner only)
func (s *LabelService) UnpublishLabel(
	ctx context.Context,
	req *connect.Request[managev1.UnpublishLabelRequest],
) (*connect.Response[managev1.LabelLifecycleMutationResponse], error) {
	var label model.Label
	transitioned := false
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&label, "id = ?", req.Msg.Id).Error; err != nil {
			return err
		}
		if err := requireLockedLabelPermission(ctx, tx, s.spiceDB, req.Msg.Id, policyv1.Label.Publish); err != nil {
			return err
		}
		if label.Status == managev1.LabelStatus_LABEL_STATUS_DRAFT.String() {
			return nil
		}
		if err := tx.Model(&label).Updates(structured.Fields{
			"status":     managev1.LabelStatus_LABEL_STATUS_DRAFT.String(),
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		transitioned = true
		if err := s.appendLabelLifecycleAudit(ctx, tx, label.ID, sharedtelemetry.AuditStatePublished, sharedtelemetry.AuditStateDraft); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("label", req.Msg.Id)
		}
		return nil, err
	}
	if transitioned {
		label.Status = managev1.LabelStatus_LABEL_STATUS_DRAFT.String()
		label.UpdatedAt = now
	}
	if transitioned {
		publishLabelContentUpdated(ctx, s.asyncPublisher, buildLabelStateTransitionContentUpdatedEvent(label.ID))
	}
	return connect.NewResponse(&managev1.LabelLifecycleMutationResponse{
		Id: label.ID, Changed: transitioned,
		Status:      managev1.LabelStatus(managev1.LabelStatus_value[label.Status]),
		PublishedAt: timestampProtoPtr(label.PublishedAt), UpdatedAt: timestamppb.New(label.UpdatedAt),
	}), nil
}

// =============================================================================
// Image Management (admin or editor)
// =============================================================================
