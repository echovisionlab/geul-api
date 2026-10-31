package series

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Deps are the collaborators of the Post Series use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	DB       *gorm.DB
	SpiceDB  *auth.SpiceDBClient
	Identity auth.IdentityManager
	Media    MediaRuntime
	OG       OGRefresh
	Reads    SeriesReadProjection
	// MenuTargets keeps Menu links to a Series in step with its slug.
	MenuTargets MenuTargets
	Posts       PostAccess
	Membership  PostMembership
	Members     MemberSummaries
	// Checkpoints authorize the contributors of a collaboration save.
	Checkpoints PostSeriesCollaborationCheckpointFence
	Audit       domainaudit.Appender
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Identity": d.Identity == nil, "Media": d.Media == nil,
		"OG": d.OG == nil, "Reads": d.Reads == nil, "MenuTargets": d.MenuTargets == nil, "Posts": d.Posts == nil,
		"Membership": d.Membership == nil, "Members": d.Members == nil, "Checkpoints": d.Checkpoints == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("series dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Post Series use cases: management, collaboration and
// the AI document boundary. Adapters translate their requests into its
// commands and queries.
type Service struct {
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	permissions    seriesPermissionChecker
	kratosClient   auth.IdentityManager
	menuTargets    MenuTargets
	postAccess     PostAccess
	postMembership PostMembership
	members        MemberSummaries
	media          MediaRuntime
	ogRefresh      OGRefresh
	reads          SeriesReadProjection
	checkpoints    PostSeriesCollaborationCheckpointFence
	// auditWriter serves the translation ports, which run in the
	// translation application's transaction.
	auditWriter domainaudit.Appender
	mutations   *mutation.Runner
	access      aiDocumentAccess
	now         func() time.Time
}

// NewService validates deps and returns the Post Series use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, permissions: deps.SpiceDB, kratosClient: deps.Identity,
		menuTargets: deps.MenuTargets, postAccess: deps.Posts, postMembership: deps.Membership, members: deps.Members,
		media: deps.Media, ogRefresh: deps.OG, reads: deps.Reads, checkpoints: deps.Checkpoints, auditWriter: deps.Audit,
		mutations: mutations, access: seriesAIDocumentAccess{}, now: time.Now,
	}, nil
}

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, *gorm.DB) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) { return struct{}{}, read(ctx, tx) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

// Page selects a page of a list. A zero Limit reads the list's default.
type Page struct {
	Limit  int32
	Offset int32
}

func (p Page) limitOr(fallback int32) int32 {
	if p.Limit > 0 {
		return p.Limit
	}
	return fallback
}

type ListSeriesPostsQuery struct{ SeriesID string }
type ListMySeriesQuery struct{ Page Page }
type ListSeriesAdminQuery struct {
	Page    Page
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}
type ListSeriesSimpleQuery struct{}
type CheckSeriesSlugAvailableQuery struct {
	Slug      string
	ExcludeID *string
}

// CreateSeriesCommand creates a draft Series. AcceptLanguage picks its
// source locale when the creator has no preference.
type CreateSeriesCommand struct {
	Title          string
	Slug           *string
	Description    *string
	AcceptLanguage string
}
type DeleteSeriesCommand struct{ ID string }
type SetSeriesFeaturedImageCommand struct{ SeriesID, FileID string }
type DeleteSeriesFeaturedImageCommand struct{ SeriesID string }

// ListSeriesPosts returns the full ordered Post set to an exact Manager/Admin.
// Public readers use the open Post list, which exposes only public lifecycle states.
func (s *Service) ListSeriesPosts(ctx context.Context, query ListSeriesPostsQuery) (*managev1.ListSeriesPostsResponse, error) {
	var posts []SeriesPost
	if err := s.readLocked(ctx, lockedSeries(query.SeriesID, policyv1.PostSeries.View), func(ctx context.Context, tx *gorm.DB) error {
		var err error
		posts, err = s.postMembership.ListSeriesPosts(ctx, tx, query.SeriesID)
		return err
	}); err != nil {
		return nil, err
	}

	// Convert to proto
	protoPosts := make([]*managev1.SeriesPost, len(posts))
	for i := range posts {
		protoPosts[i] = toProtoSeriesPost(&posts[i])
	}

	return &managev1.ListSeriesPostsResponse{
		Posts: protoPosts,
	}, nil
}

// ListMySeries returns series where current user is a member
func (s *Service) ListMySeries(ctx context.Context, query ListMySeriesQuery) (*managev1.ListMySeriesResponse, error) {
	seriesIDs, err := s.lookupSeriesResources(ctx, policyv1.PostSeries.LookupView())
	if err != nil {
		return nil, err
	}

	if len(seriesIDs) == 0 {
		return &managev1.ListMySeriesResponse{
			Series: []*managev1.SeriesWithStats{},
			Pagination: &commonv1.PaginationResponse{
				Total:   0,
				Limit:   20,
				Offset:  0,
				HasMore: false,
			},
		}, nil
	}

	var seriesList []model.Series
	var total int64

	stored := s.db.WithContext(ctx).Model(&model.Series{}).
		Where("id IN ?", seriesIDs)

	// Count total
	if err := stored.Count(&total).Error; err != nil {
		return nil, err
	}

	limit, offset := query.Page.limitOr(20), query.Page.Offset

	stored = stored.Order("created_at DESC, id ASC")

	if err := stored.Limit(int(limit)).Offset(int(offset)).Find(&seriesList).Error; err != nil {
		return nil, err
	}
	if err := s.overlaySeriesSourceLocaleDocuments(ctx, seriesList); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadySeriesOgAssets(ctx, seriesList)
	if err != nil {
		return nil, err
	}
	listDetails, err := s.loadSeriesListDetails(ctx, seriesList)
	if err != nil {
		return nil, err
	}

	seriesWithStats := make([]*managev1.SeriesWithStats, len(seriesList))
	for i := range seriesList {
		protoSeries := s.toProtoSeries(&seriesList[i], manageOgAssetFromReadyMap(readyOgAssets, seriesList[i].OgAssetID))
		details := listDetails[seriesList[i].ID]
		protoSeries.FeaturedImageAsset = details.FeaturedImageAsset
		seriesWithStats[i] = &managev1.SeriesWithStats{
			Series:       protoSeries,
			PostCount:    details.PostCount,
			ManagerCount: details.ManagerCount,
		}
	}

	return &managev1.ListMySeriesResponse{
		Series: seriesWithStats,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}, nil
}

// ListSeriesAdmin returns all series with stats (admin only)
func (s *Service) ListSeriesAdmin(ctx context.Context, query ListSeriesAdminQuery) (*managev1.ListSeriesAdminResponse, error) {
	listCan, err := policyv1.PostSeries.List()
	if err != nil {
		return nil, err
	}
	if err := requireSeriesPlatformPermission(ctx, s.permissions, listCan); err != nil {
		return nil, err
	}

	var seriesList []model.Series
	var total int64

	stored := s.db.WithContext(ctx).Model(&model.Series{})

	// Apply filters using FilterConfig
	stored, err = SeriesFilterConfig.ApplyFilters(stored, query.Filters)
	if err != nil {
		return nil, err
	}

	// Count total
	if err := stored.Count(&total).Error; err != nil {
		return nil, err
	}

	limit, offset := query.Page.limitOr(20), query.Page.Offset

	// Apply sorting
	stored, err = seriesSortConfig.ApplySort(stored, query.Sorts)
	if err != nil {
		return nil, err
	}
	stored = stored.Order("id ASC")

	if err := stored.Limit(int(limit)).Offset(int(offset)).Find(&seriesList).Error; err != nil {
		return nil, err
	}
	if err := s.overlaySeriesSourceLocaleDocuments(ctx, seriesList); err != nil {
		return nil, err
	}
	readyOgAssets, err := s.loadReadySeriesOgAssets(ctx, seriesList)
	if err != nil {
		return nil, err
	}
	listDetails, err := s.loadSeriesListDetails(ctx, seriesList)
	if err != nil {
		return nil, err
	}

	seriesWithStats := make([]*managev1.SeriesWithStats, len(seriesList))
	for i := range seriesList {
		protoSeries := s.toProtoSeries(&seriesList[i], manageOgAssetFromReadyMap(readyOgAssets, seriesList[i].OgAssetID))
		details := listDetails[seriesList[i].ID]
		protoSeries.FeaturedImageAsset = details.FeaturedImageAsset
		seriesWithStats[i] = &managev1.SeriesWithStats{
			Series:       protoSeries,
			PostCount:    details.PostCount,
			ManagerCount: details.ManagerCount,
		}
	}

	return &managev1.ListSeriesAdminResponse{
		Series: seriesWithStats,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}, nil
}

// ListSeriesSimple returns all series for dropdowns
func (s *Service) ListSeriesSimple(ctx context.Context, query ListSeriesSimpleQuery) (*managev1.ListSeriesSimpleResponse, error) {
	seriesIDs, err := s.lookupSeriesResources(ctx, policyv1.PostSeries.LookupManage())
	if err != nil {
		return nil, err
	}
	if len(seriesIDs) == 0 {
		return &managev1.ListSeriesSimpleResponse{Series: []*managev1.SeriesSimple{}}, nil
	}

	var seriesList []model.Series

	stored := s.db.WithContext(ctx).Model(&model.Series{}).
		Where("id IN ?", seriesIDs).
		Order("created_at ASC")

	if err := stored.Find(&seriesList).Error; err != nil {
		return nil, err
	}
	if err := s.overlaySeriesSourceLocaleDocuments(ctx, seriesList); err != nil {
		return nil, err
	}
	sort.SliceStable(seriesList, func(i, j int) bool { return seriesList[i].Title < seriesList[j].Title })

	// Convert to simple
	simpleSeries := make([]*managev1.SeriesSimple, len(seriesList))
	for i := range seriesList {
		simpleSeries[i] = toProtoSeriesSimple(&seriesList[i])
	}

	return &managev1.ListSeriesSimpleResponse{
		Series: simpleSeries,
	}, nil
}

// CreateSeries creates a draft Series. Publishing is an explicit later
// transition.
func (s *Service) CreateSeries(ctx context.Context, command CreateSeriesCommand) (*managev1.Series, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return nil, domainerr.Required("title")
	}
	slug := seriesSlugFromTitle(title)
	if command.Slug != nil {
		slug = *command.Slug
	}
	slug, err := validateSeriesSlug(slug)
	if err != nil {
		return nil, err
	}
	if err := ensureSlugAvailable(ctx, s.db, &model.Series{}, "series", slug, ""); err != nil {
		return nil, err
	}
	if err := routeregistry.EnsureResourceRouteAvailable(ctx, s.db, "series", "series", slug); err != nil {
		return nil, err
	}

	now := dbtime.Now()
	sourceLocale := resolveInitialSourceLocale(ctx, s.db, s.kratosClient, command.AcceptLanguage)
	var audits seriesAudits
	series, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.Series]{
		Access: mutation.FreshCan(policyv1.PostSeries.Create()),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.Series, error) {
			return s.createSeries(ctx, tx, command, title, slug, sourceLocale, now, &audits)
		},
		Audit: func(*model.Series) mutation.Audit { return audits.audit() },
		Relationships: func(series *model.Series) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			touch, err := policyv1.PostSeries.TouchPolicy(series.ID)
			if err != nil {
				return nil, nil, err
			}
			remove, err := policyv1.PostSeries.DeletePolicy(series.ID)
			if err != nil {
				return nil, nil, err
			}
			return []policyv1.RelationshipMutation{touch}, []policyv1.RelationshipMutation{remove}, nil
		},
	})
	if err != nil {
		if dberrors.IsUniqueViolation(err) {
			return nil, domainerr.Duplicate("series", "slug", slug).WithCause(err)
		}
		return nil, err
	}
	ogAsset, err := s.media.ReadyAsset(ctx, series.OgAssetID)
	if err != nil {
		return nil, err
	}
	protoSeries := s.toProtoSeries(series, ogAsset)
	s.setSeriesFeaturedImageAsset(ctx, protoSeries)
	return protoSeries, nil
}

func (s *Service) createSeries(
	ctx context.Context,
	tx *gorm.DB,
	command CreateSeriesCommand,
	title, slug, sourceLocale string,
	now time.Time,
	audits *seriesAudits,
) (*model.Series, error) {
	if err := ensureSlugAvailable(ctx, tx, &model.Series{}, "series", slug, ""); err != nil {
		return nil, err
	}
	if err := routeregistry.EnsureResourceRouteAvailableInTx(ctx, tx, "series", "series", slug); err != nil {
		return nil, err
	}
	contentDocumentID, err := createSeriesContentDocument(ctx, tx, now.UTC())
	if err != nil {
		return nil, err
	}
	series := &model.Series{
		Slug: slug, Status: managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
		SourceLocale: sourceLocale, ContentDocumentID: contentDocumentID.String(), CreatedAt: now, UpdatedAt: &now,
	}
	if err := tx.Omit("ID").Clauses(clause.Returning{}).Create(series).Error; err != nil {
		return nil, err
	}
	if err := SaveSourceLocaleDocument(ctx, tx, series.ID, sourceLocale, &title, command.Description, now.UTC()); err != nil {
		return nil, err
	}
	if _, err := s.ogRefresh.RequestCurrent(ctx, tx, series.ID, sourceLocale, false, "series_created"); err != nil {
		return nil, err
	}
	series.SourceLocale = sourceLocale
	series.Title = title
	series.Description = command.Description
	audits.created(series.ID)
	return series, nil
}

// CheckSeriesSlugAvailable checks if a slug is available. With ExcludeID
// the caller must be able to edit that Series.
func (s *Service) CheckSeriesSlugAvailable(ctx context.Context, query CheckSeriesSlugAvailableQuery) (*managev1.CheckSeriesSlugAvailableResponse, error) {
	slug, err := validateSeriesSlug(query.Slug)
	if err != nil {
		return nil, err
	}
	excludeID := ""
	if query.ExcludeID != nil {
		excludeID = *query.ExcludeID
	}
	if excludeID != "" {
		if err := s.requireSeriesPermissionOrNotFound(ctx, excludeID, policyv1.PostSeries.Edit); err != nil {
			return nil, err
		}
	} else {
		createCan, err := policyv1.PostSeries.Create()
		if err != nil {
			return nil, err
		}
		if err := requireSeriesPlatformPermission(ctx, s.permissions, createCan); err != nil {
			return nil, err
		}
	}
	available, err := isSlugAvailable(ctx, s.db, &model.Series{}, slug, excludeID)
	if err != nil {
		return nil, err
	}
	if available {
		available, err = routeregistry.IsResourceRouteAvailable(ctx, s.db, "series", slug)
		if err != nil {
			return nil, err
		}
	}
	return &managev1.CheckSeriesSlugAvailableResponse{Available: available}, nil
}

// seriesDeletion is a locked Series about to be deleted and the policy
// relationships its deletion removes and would restore.
type seriesDeletion struct {
	series               model.Series
	deleteRelationships  []policyv1.RelationshipMutation
	restoreRelationships []policyv1.RelationshipMutation
}

func (s *Service) DeleteSeries(ctx context.Context, command DeleteSeriesCommand) error {
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[seriesDeletion]{
		Access: lockedSeries(command.ID, policyv1.PostSeries.Delete),
		Change: func(ctx context.Context, tx *gorm.DB) (seriesDeletion, error) {
			return s.deleteSeries(ctx, tx, command.ID, &audits)
		},
		Audit: func(seriesDeletion) mutation.Audit { return audits.audit() },
		Relationships: func(deletion seriesDeletion) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return deletion.deleteRelationships, deletion.restoreRelationships, nil
		},
	})
	return err
}

func (s *Service) deleteSeries(ctx context.Context, tx *gorm.DB, seriesID string, audits *seriesAudits) (seriesDeletion, error) {
	var deletion seriesDeletion
	if err := tx.First(&deletion.series, "id = ?", seriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return seriesDeletion{}, domainerr.ResourceNotFound("series", seriesID)
		}
		return seriesDeletion{}, err
	}
	series := deletion.series
	if err := s.seriesDeletionRelationships(ctx, &deletion); err != nil {
		return seriesDeletion{}, err
	}
	postIDs, err := s.postMembership.SeriesPostIDs(ctx, tx, series.ID)
	if err != nil {
		return seriesDeletion{}, err
	}
	if err := lockSeriesOrderPosts(ctx, tx, s.postMembership, postIDs); err != nil {
		return seriesDeletion{}, err
	}
	if err := s.postMembership.ClearAllSeries(ctx, tx, series.ID, dbtime.Now()); err != nil {
		return seriesDeletion{}, err
	}
	if err := s.media.ReleaseFeaturedImage(ctx, tx, series.ID); err != nil {
		return seriesDeletion{}, err
	}
	if err := s.media.CancelAndReleaseOG(ctx, tx, series.ID); err != nil {
		return seriesDeletion{}, err
	}
	// Delete cascades the manager attribution rows. Post relation columns are
	// cleared together above so the paired series/order invariant is preserved.
	if err := tx.Delete(&series).Error; err != nil {
		return seriesDeletion{}, err
	}
	contentDocumentID, err := parseSeriesContentDocumentUUID(series.ContentDocumentID, "content_document_id")
	if err != nil {
		return seriesDeletion{}, err
	}
	if err := deleteSeriesContentDocument(ctx, tx, contentDocumentID); err != nil {
		return seriesDeletion{}, err
	}
	if err := s.menuTargets.Remove(ctx, tx, "series", series.ID, series.Slug); err != nil {
		return seriesDeletion{}, err
	}
	audits.deleted(series.ID)
	return deletion, nil
}

// seriesDeletionRelationships reads the Series policy relationships its
// deletion removes and their exact restore.
func (s *Service) seriesDeletionRelationships(ctx context.Context, deletion *seriesDeletion) error {
	snapshotPlan, err := policyv1.PostSeries.Snapshot(deletion.series.ID)
	if err != nil {
		return err
	}
	snapshots, _, err := s.spiceDB.SnapshotResourceRelationshipDescriptors(ctx, snapshotPlan)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if deletion.deleteRelationships, deletion.restoreRelationships, err = seriesAuthorizationSnapshotMutations(deletion.series.ID, snapshots); err != nil {
		return err
	}
	if err := validateResourceDeletionAuthorizationBatchSize("series", deletion.deleteRelationships, deletion.restoreRelationships); err != nil {
		return err
	}
	if len(deletion.deleteRelationships) == 0 {
		return errors.New("series authorization relationships are missing")
	}
	return nil
}

// seriesFeaturedImage is the outcome of a featured image change.
type seriesFeaturedImage struct {
	asset             *commonv1.AssetRef
	ogGenerationRunID *string
}

func (s *Service) SetSeriesFeaturedImage(ctx context.Context, command SetSeriesFeaturedImageCommand) (*managev1.SetSeriesFeaturedImageResponse, error) {
	var audits seriesAudits
	image, err := mutation.Run(ctx, s.mutations, mutation.Spec[seriesFeaturedImage]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Edit),
		Change: func(ctx context.Context, tx *gorm.DB) (seriesFeaturedImage, error) {
			series, err := loadLockedSeries(tx, command.SeriesID)
			if err != nil {
				return seriesFeaturedImage{}, err
			}
			if err := s.media.RequireAttachableFile(ctx, tx, command.FileID); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return seriesFeaturedImage{}, domainerr.ResourceNotFound("file", command.FileID)
				}
				return seriesFeaturedImage{}, err
			}
			if sameOptionalString(series.FeaturedImageFileID, &command.FileID) {
				return seriesFeaturedImage{}, nil
			}
			if err := tx.Model(&series).Updates(structured.Fields{
				"featured_image_file_id": command.FileID, "updated_at": time.Now().UTC(),
			}).Error; err != nil {
				return seriesFeaturedImage{}, err
			}
			var image seriesFeaturedImage
			if image.asset, err = s.media.BindFeaturedImage(ctx, tx, command.FileID, series.ID); err != nil {
				return seriesFeaturedImage{}, err
			}
			if image.ogGenerationRunID, err = s.ogRefresh.RequestCurrent(ctx, tx, series.ID, "", true, "series_featured_image_updated"); err != nil {
				return seriesFeaturedImage{}, err
			}
			audits.featuredImage(series.ID, sharedtelemetry.AuditCollectionOperationAdded, command.FileID)
			return image, nil
		},
		Audit: func(seriesFeaturedImage) mutation.Audit { return audits.audit() },
	})
	if err != nil {
		return nil, err
	}
	return &managev1.SetSeriesFeaturedImageResponse{ImageAsset: image.asset, OgGenerationRunId: image.ogGenerationRunID}, nil
}

func (s *Service) DeleteSeriesFeaturedImage(ctx context.Context, command DeleteSeriesFeaturedImageCommand) (*managev1.OgAssetDeleteResponse, error) {
	var audits seriesAudits
	image, err := mutation.Run(ctx, s.mutations, mutation.Spec[seriesFeaturedImage]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Edit),
		Change: func(ctx context.Context, tx *gorm.DB) (seriesFeaturedImage, error) {
			series, err := loadLockedSeries(tx, command.SeriesID)
			if err != nil || series.FeaturedImageFileID == nil {
				return seriesFeaturedImage{}, err
			}
			oldFileID := *series.FeaturedImageFileID
			if err := tx.Model(&series).Updates(structured.Fields{
				"featured_image_file_id": nil, "updated_at": time.Now().UTC(),
			}).Error; err != nil {
				return seriesFeaturedImage{}, err
			}
			if err := s.media.ReleaseFeaturedImage(ctx, tx, series.ID); err != nil {
				return seriesFeaturedImage{}, err
			}
			var image seriesFeaturedImage
			if image.ogGenerationRunID, err = s.ogRefresh.RequestCurrent(ctx, tx, series.ID, "", true, "series_featured_image_removed"); err != nil {
				return seriesFeaturedImage{}, err
			}
			audits.featuredImage(series.ID, sharedtelemetry.AuditCollectionOperationRemoved, oldFileID)
			return image, nil
		},
		Audit: func(seriesFeaturedImage) mutation.Audit { return audits.audit() },
	})
	if err != nil {
		return nil, err
	}
	return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: image.ogGenerationRunID}, nil
}

func loadLockedSeries(tx *gorm.DB, seriesID string) (model.Series, error) {
	var series model.Series
	if err := tx.First(&series, "id = ?", seriesID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Series{}, domainerr.ResourceNotFound("series", seriesID)
		}
		return model.Series{}, err
	}
	return series, nil
}
