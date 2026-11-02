package series

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Deps are the collaborators of the Post Series use cases. Every field is
// required; NewService rejects a missing one.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // seriespostgres.NewUnits(db, ...)
	SpiceDB Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Audit   domainaudit.Appender
	Media   Media
	Reads   Reads
	Members Members
	Locales Locales
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Media": d.Media == nil, "Reads": d.Reads == nil, "Members": d.Members == nil, "Locales": d.Locales == nil,
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
	units   mutation.UnitOfWork[Store]
	spiceDB Authorizer
	members Members
	media   Media
	reads   Reads
	locales Locales
	// audit serves the translation ports, which run in the translation
	// application's transaction.
	audit     domainaudit.Appender
	mutations *mutation.StoreRunner[Store]
	now       func() time.Time
}

// NewService validates deps and returns the Post Series use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, members: deps.Members, media: deps.Media, reads: deps.Reads,
		locales: deps.Locales, audit: deps.Audit, mutations: mutations, now: time.Now,
	}, nil
}

// readLocked runs read in one transaction after access, so the rows it
// locks agree with each other.
func (s *Service) readLocked(ctx context.Context, access mutation.Access, read func(context.Context, Store) error) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: access,
		Change: func(ctx context.Context, store Store) (struct{}, error) { return struct{}{}, read(ctx, store) },
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

// Detail is a Series as the management API shows it: the root with its
// source copy and the ready assets it points at.
type Detail struct {
	Series             Series
	OgAsset            *Asset
	FeaturedImageAsset *Asset
}

// ListedSeries is a Series of a management list with its counts.
type ListedSeries struct {
	Detail
	PostCount    int32
	ManagerCount int32
}

// SeriesPage is one page of a management Series list.
type SeriesPage struct {
	Items   []ListedSeries
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// FeaturedImageChange is the outcome of a featured image change.
type FeaturedImageChange struct {
	Asset             *Asset
	OgGenerationRunID *string
}

type ListSeriesPostsQuery struct{ SeriesID string }
type ListMySeriesQuery struct{ Page Page }
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
func (s *Service) ListSeriesPosts(ctx context.Context, query ListSeriesPostsQuery) ([]SeriesPost, error) {
	var posts []SeriesPost
	if err := s.readLocked(ctx, lockedSeries(query.SeriesID, policyv1.PostSeries.View), func(ctx context.Context, store Store) error {
		var err error
		posts, err = store.Posts().SeriesPosts(ctx, query.SeriesID)
		return err
	}); err != nil {
		return nil, err
	}
	return posts, nil
}

// ListMySeries returns series where current user is a member
func (s *Service) ListMySeries(ctx context.Context, query ListMySeriesQuery) (SeriesPage, error) {
	seriesIDs, err := s.lookupSeriesResources(ctx, policyv1.PostSeries.LookupView())
	if err != nil {
		return SeriesPage{}, err
	}
	if len(seriesIDs) == 0 {
		return SeriesPage{Items: []ListedSeries{}, Limit: 20}, nil
	}

	limit, offset := query.Page.limitOr(20), query.Page.Offset
	var seriesList []Series
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if seriesList, total, err = store.SeriesPage(ctx, seriesIDs, limit, offset); err != nil {
			return err
		}
		return overlaySourceCopies(ctx, store, seriesList)
	}); err != nil {
		return SeriesPage{}, err
	}
	return s.seriesPage(ctx, seriesList, total, limit, offset)
}

// ListSeriesAdmin returns all series with stats (admin only)
func (s *Service) ListSeriesAdmin(ctx context.Context, query ListSeriesAdminQuery) (SeriesPage, error) {
	listCan, err := policyv1.PostSeries.List()
	if err != nil {
		return SeriesPage{}, err
	}
	if err := requireSeriesPlatformPermission(ctx, s.spiceDB, listCan); err != nil {
		return SeriesPage{}, err
	}

	limit, offset := query.Page.limitOr(20), query.Page.Offset
	var seriesList []Series
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if seriesList, total, err = store.AdminSeriesPage(ctx, query, limit, offset); err != nil {
			return err
		}
		return overlaySourceCopies(ctx, store, seriesList)
	}); err != nil {
		return SeriesPage{}, err
	}
	return s.seriesPage(ctx, seriesList, total, limit, offset)
}

// seriesPage projects the ready assets and counts of a listed page.
func (s *Service) seriesPage(ctx context.Context, seriesList []Series, total int64, limit, offset int32) (SeriesPage, error) {
	readyOgAssets, err := s.loadReadySeriesOgAssets(ctx, seriesList)
	if err != nil {
		return SeriesPage{}, err
	}
	listDetails, err := s.loadSeriesListDetails(ctx, seriesList)
	if err != nil {
		return SeriesPage{}, err
	}
	items := make([]ListedSeries, len(seriesList))
	for i := range seriesList {
		details := listDetails[seriesList[i].ID]
		items[i] = ListedSeries{
			Detail: Detail{
				Series:             seriesList[i],
				OgAsset:            manageOgAssetFromReadyMap(readyOgAssets, seriesList[i].OgAssetID),
				FeaturedImageAsset: details.FeaturedImageAsset,
			},
			PostCount:    details.PostCount,
			ManagerCount: details.ManagerCount,
		}
	}
	return SeriesPage{
		Items: items, Total: int32(total), Limit: limit, Offset: offset,
		HasMore: offset+limit < int32(total),
	}, nil
}

// ListSeriesSimple returns all series for dropdowns
func (s *Service) ListSeriesSimple(ctx context.Context, _ ListSeriesSimpleQuery) ([]Series, error) {
	seriesIDs, err := s.lookupSeriesResources(ctx, policyv1.PostSeries.LookupManage())
	if err != nil {
		return nil, err
	}
	if len(seriesIDs) == 0 {
		return []Series{}, nil
	}
	var seriesList []Series
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if seriesList, err = store.SeriesByCreation(ctx, seriesIDs); err != nil {
			return err
		}
		return overlaySourceCopies(ctx, store, seriesList)
	}); err != nil {
		return nil, err
	}
	sort.SliceStable(seriesList, func(i, j int) bool { return seriesList[i].Title < seriesList[j].Title })
	return seriesList, nil
}

// CreateSeries creates a draft Series. Publishing is an explicit later
// transition.
func (s *Service) CreateSeries(ctx context.Context, command CreateSeriesCommand) (Detail, error) {
	title := strings.TrimSpace(command.Title)
	if title == "" {
		return Detail{}, domainerr.Required("title")
	}
	slug := seriesSlugFromTitle(title)
	if command.Slug != nil {
		slug = *command.Slug
	}
	slug, err := validateSeriesSlug(slug)
	if err != nil {
		return Detail{}, err
	}
	if err := s.units.Read(ctx, func(store Store) error {
		if err := ensureSlugAvailable(ctx, store, slug, ""); err != nil {
			return err
		}
		return store.EnsureRouteAvailable(ctx, slug, false)
	}); err != nil {
		return Detail{}, err
	}

	now := dbtime.Now()
	sourceLocale := s.resolveInitialSourceLocale(ctx, command.AcceptLanguage)
	var audits seriesAudits
	series, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Series, Store]{
		Access: mutation.FreshCan(policyv1.PostSeries.Create()),
		Change: func(ctx context.Context, store Store) (Series, error) {
			return createSeries(ctx, store, command, title, slug, sourceLocale, now, &audits)
		},
		Audit: func(Series) mutation.Audit { return audits.audit() },
		Relationships: func(series Series) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
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
		if errors.Is(err, ErrSlugTaken) {
			return Detail{}, domainerr.Duplicate("series", "slug", slug).WithCause(err)
		}
		return Detail{}, err
	}
	return s.detail(ctx, series)
}

func createSeries(
	ctx context.Context,
	store Store,
	command CreateSeriesCommand,
	title, slug, sourceLocale string,
	now time.Time,
	audits *seriesAudits,
) (Series, error) {
	if err := ensureSlugAvailable(ctx, store, slug, ""); err != nil {
		return Series{}, err
	}
	if err := store.EnsureRouteAvailable(ctx, slug, true); err != nil {
		return Series{}, err
	}
	contentDocumentID, err := createSeriesContentDocument(ctx, store, now.UTC())
	if err != nil {
		return Series{}, err
	}
	series, err := store.InsertSeries(ctx, Series{
		Slug: slug, Status: StatusDraft,
		SourceLocale: sourceLocale, ContentDocumentID: contentDocumentID.String(), CreatedAt: now, UpdatedAt: &now,
	})
	if err != nil {
		return Series{}, err
	}
	if err := saveSourceCopy(ctx, store, series.ID, sourceLocale, &title, command.Description, now.UTC()); err != nil {
		return Series{}, err
	}
	if _, err := store.RequestOG(ctx, series.ID, sourceLocale, false, "series_created"); err != nil {
		return Series{}, err
	}
	series.SourceLocale = sourceLocale
	series.Title = title
	series.Description = command.Description
	audits.created(series.ID)
	return series, nil
}

// CheckSeriesSlugAvailable checks if a slug is available. With ExcludeID
// the caller must be able to edit that Series.
func (s *Service) CheckSeriesSlugAvailable(ctx context.Context, query CheckSeriesSlugAvailableQuery) (bool, error) {
	slug, err := validateSeriesSlug(query.Slug)
	if err != nil {
		return false, err
	}
	excludeID := ""
	if query.ExcludeID != nil {
		excludeID = *query.ExcludeID
	}
	if excludeID != "" {
		if err := s.requireSeriesPermissionOrNotFound(ctx, excludeID, policyv1.PostSeries.Edit); err != nil {
			return false, err
		}
	} else {
		createCan, err := policyv1.PostSeries.Create()
		if err != nil {
			return false, err
		}
		if err := requireSeriesPlatformPermission(ctx, s.spiceDB, createCan); err != nil {
			return false, err
		}
	}
	var available bool
	err = s.units.Read(ctx, func(store Store) error {
		taken, err := store.SlugTaken(ctx, slug, excludeID)
		if err != nil || taken {
			return err
		}
		available, err = store.RouteAvailable(ctx, slug)
		return err
	})
	return available, err
}

// seriesDeletion is a locked Series about to be deleted and the policy
// relationships its deletion removes and would restore.
type seriesDeletion struct {
	series               Series
	deleteRelationships  []policyv1.RelationshipMutation
	restoreRelationships []policyv1.RelationshipMutation
}

func (s *Service) DeleteSeries(ctx context.Context, command DeleteSeriesCommand) error {
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[seriesDeletion, Store]{
		Access: lockedSeries(command.ID, policyv1.PostSeries.Delete),
		Change: func(ctx context.Context, store Store) (seriesDeletion, error) {
			return s.deleteSeries(ctx, store, command.ID, &audits)
		},
		Audit: func(seriesDeletion) mutation.Audit { return audits.audit() },
		Relationships: func(deletion seriesDeletion) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return deletion.deleteRelationships, deletion.restoreRelationships, nil
		},
	})
	return err
}

func (s *Service) deleteSeries(ctx context.Context, store Store, seriesID string, audits *seriesAudits) (seriesDeletion, error) {
	var deletion seriesDeletion
	var err error
	if deletion.series, err = loadSeries(ctx, store, seriesID); err != nil {
		return seriesDeletion{}, err
	}
	series := deletion.series
	if err := s.seriesDeletionRelationships(ctx, &deletion); err != nil {
		return seriesDeletion{}, err
	}
	posts := store.Posts()
	postIDs, err := posts.SeriesPostIDs(ctx, series.ID)
	if err != nil {
		return seriesDeletion{}, err
	}
	if err := lockSeriesOrderPosts(ctx, posts, postIDs); err != nil {
		return seriesDeletion{}, err
	}
	if err := posts.ClearAll(ctx, series.ID, dbtime.Now()); err != nil {
		return seriesDeletion{}, err
	}
	if err := store.ReleaseFeaturedImage(ctx, series.ID); err != nil {
		return seriesDeletion{}, err
	}
	if err := store.CancelAndReleaseOG(ctx, series.ID); err != nil {
		return seriesDeletion{}, err
	}
	// Delete cascades the manager attribution rows. Post relation columns are
	// cleared together above so the paired series/order invariant is preserved.
	if err := store.DeleteSeries(ctx, series.ID); err != nil {
		return seriesDeletion{}, err
	}
	contentDocumentID, err := parseSeriesContentDocumentUUID(series.ContentDocumentID, "content_document_id")
	if err != nil {
		return seriesDeletion{}, err
	}
	if err := deleteSeriesContentDocument(ctx, store, contentDocumentID); err != nil {
		return seriesDeletion{}, err
	}
	if err := store.RemoveMenuTargets(ctx, series.ID, series.Slug); err != nil {
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

func (s *Service) SetSeriesFeaturedImage(ctx context.Context, command SetSeriesFeaturedImageCommand) (FeaturedImageChange, error) {
	var audits seriesAudits
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[FeaturedImageChange, Store]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Edit),
		Change: func(ctx context.Context, store Store) (FeaturedImageChange, error) {
			series, err := loadSeries(ctx, store, command.SeriesID)
			if err != nil {
				return FeaturedImageChange{}, err
			}
			if err := store.RequireAttachableFile(ctx, command.FileID); err != nil {
				if errors.Is(err, ErrFileNotFound) {
					return FeaturedImageChange{}, domainerr.ResourceNotFound("file", command.FileID)
				}
				return FeaturedImageChange{}, err
			}
			if equalOptionalString(series.FeaturedImageFileID, &command.FileID) {
				return FeaturedImageChange{}, nil
			}
			return setFeaturedImage(ctx, store, series.ID, command.FileID, &audits)
		},
		Audit: func(FeaturedImageChange) mutation.Audit { return audits.audit() },
	})
}

func setFeaturedImage(ctx context.Context, store Store, seriesID, fileID string, audits *seriesAudits) (FeaturedImageChange, error) {
	if err := store.SetFeaturedImage(ctx, seriesID, &fileID, time.Now().UTC()); err != nil {
		return FeaturedImageChange{}, err
	}
	var change FeaturedImageChange
	var err error
	if change.Asset, err = store.BindFeaturedImage(ctx, seriesID, fileID); err != nil {
		return FeaturedImageChange{}, err
	}
	if change.OgGenerationRunID, err = store.RequestOG(ctx, seriesID, "", true, "series_featured_image_updated"); err != nil {
		return FeaturedImageChange{}, err
	}
	audits.featuredImage(seriesID, sharedtelemetry.AuditCollectionOperationAdded, fileID)
	return change, nil
}

// DeleteSeriesFeaturedImage removes the featured image and returns the OG
// generation run its removal started, if any.
func (s *Service) DeleteSeriesFeaturedImage(ctx context.Context, command DeleteSeriesFeaturedImageCommand) (*string, error) {
	var audits seriesAudits
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[*string, Store]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Edit),
		Change: func(ctx context.Context, store Store) (*string, error) {
			series, err := loadSeries(ctx, store, command.SeriesID)
			if err != nil || series.FeaturedImageFileID == nil {
				return nil, err
			}
			oldFileID := *series.FeaturedImageFileID
			if err := store.SetFeaturedImage(ctx, series.ID, nil, time.Now().UTC()); err != nil {
				return nil, err
			}
			if err := store.ReleaseFeaturedImage(ctx, series.ID); err != nil {
				return nil, err
			}
			runID, err := store.RequestOG(ctx, series.ID, "", true, "series_featured_image_removed")
			if err != nil {
				return nil, err
			}
			audits.featuredImage(series.ID, sharedtelemetry.AuditCollectionOperationRemoved, oldFileID)
			return runID, nil
		},
		Audit: func(*string) mutation.Audit { return audits.audit() },
	})
}

// loadSeries reads a Series root, answering a missing one as not found.
func loadSeries(ctx context.Context, store Store, seriesID string) (Series, error) {
	series, err := store.Series(ctx, seriesID)
	if errors.Is(err, ErrSeriesNotFound) {
		return Series{}, domainerr.ResourceNotFound("series", seriesID)
	}
	return series, err
}
