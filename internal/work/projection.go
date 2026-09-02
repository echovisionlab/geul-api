package work

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// workSortConfig defines allowed sort fields for works
func (s *WorkService) CheckWorkSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckWorkSlugAvailableRequest],
) (*connect.Response[managev1.CheckWorkSlugAvailableResponse], error) {
	if req.Msg.ExcludeWorkId != nil {
		if err := RequireExists(ctx, s.db, *req.Msg.ExcludeWorkId); err != nil {
			return nil, err
		}
		var work model.Work
		if err := s.db.WithContext(ctx).Select("id", "status").Take(&work, "id = ?", *req.Msg.ExcludeWorkId).Error; err != nil {
			return nil, errs.Internal(err)
		}
		if err := requireWorkPermission(ctx, s.spiceDB, work, policyv1.Work.Manage, workAuthorizationMutation); err != nil {
			return nil, err
		}
	} else if err := requireWorkCreate(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	if err := validateSlugWithoutSlash(req.Msg.Slug); err != nil {
		return nil, err
	}

	var count int64

	query := s.db.WithContext(ctx).Model(&model.Work{}).Where("slug = ?", req.Msg.Slug)

	if req.Msg.ExcludeWorkId != nil {
		query = query.Where("id != ?", *req.Msg.ExcludeWorkId)
	}

	if err := query.Count(&count).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if count == 0 {
		available, err := routeregistry.IsResourceRouteAvailable(ctx, s.db, "works", req.Msg.Slug)
		if err != nil {
			return nil, err
		}
		if !available {
			count = 1
		}
	}

	return connect.NewResponse(&managev1.CheckWorkSlugAvailableResponse{
		Available: count == 0,
	}), nil
}

func (s *WorkService) getWorkFeaturedImageAsset(ctx context.Context, workID string) *commonv1.AssetRef {
	var result struct {
		FileID *string `gorm:"column:file_id"`
	}

	err := s.db.WithContext(ctx).
		Table("work").
		Select("work.featured_image_file_id AS file_id").
		Where("work.id = ?", workID).
		Scan(&result).Error

	if err != nil || result.FileID == nil || *result.FileID == "" {
		return nil
	}

	asset, err := s.runtime.ReadyPublicAssetRefForSourceFile(ctx, s.db, *result.FileID, "image")
	if err != nil {
		slog.Warn("Failed to resolve work featured image asset", "workId", workID, "fileId", *result.FileID, "error", err)
		return nil
	}
	return asset
}

// getWorkCreditsWithError fetches credits for a work with error handling
func (s *WorkService) getWorkCreditsWithError(ctx context.Context, workID string) ([]*managev1.WorkCredit, error) {
	var credits []model.WorkCredit
	if err := s.db.WithContext(ctx).
		Where("work_id = ?", workID).
		Order("sort_order").
		Find(&credits).Error; err != nil {
		return nil, err
	}

	protoCredits := make([]*managev1.WorkCredit, len(credits))
	for i, c := range credits {
		protoCredits[i] = s.toProtoCredit(ctx, &c)
	}
	return protoCredits, nil
}

func (s *WorkService) getWorkClients(ctx context.Context, workID string) []*managev1.WorkClient {
	type clientRow struct {
		ID          string
		Name        string
		Website     *string
		LightFileID *string `gorm:"column:light_file_id"`
		DarkFileID  *string `gorm:"column:dark_file_id"`
	}

	var rows []clientRow
	err := s.db.WithContext(ctx).
		Table("work_client wc").
		Select("c.id, c.name, c.website, c.logo_light_file_id AS light_file_id, c.logo_dark_file_id AS dark_file_id").
		Joins("JOIN client c ON c.id = wc.client_id").
		Where("wc.work_id = ?", workID).
		Order("wc.sort_order ASC").
		Scan(&rows).Error

	if err != nil || len(rows) == 0 {
		return nil
	}

	clients := make([]*managev1.WorkClient, 0, len(rows))
	for _, row := range rows {
		client := &managev1.WorkClient{
			Id:   row.ID,
			Name: row.Name,
		}
		if row.Website != nil {
			client.Website = row.Website
		}
		if row.LightFileID != nil && *row.LightFileID != "" {
			asset, assetErr := s.runtime.ReadyPublicAssetRefForSourceFile(ctx, s.db, *row.LightFileID, "logo")
			if assetErr != nil {
				slog.Warn("Failed to resolve client light logo asset", "clientId", row.ID, "fileId", *row.LightFileID, "error", assetErr)
			} else {
				client.LogoLightAsset = asset
			}
		}
		if row.DarkFileID != nil && *row.DarkFileID != "" {
			asset, assetErr := s.runtime.ReadyPublicAssetRefForSourceFile(ctx, s.db, *row.DarkFileID, "logo")
			if assetErr != nil {
				slog.Warn("Failed to resolve client dark logo asset", "clientId", row.ID, "fileId", *row.DarkFileID, "error", assetErr)
			} else {
				client.LogoDarkAsset = asset
			}
		}
		clients = append(clients, client)
	}

	return clients
}

func (s *WorkService) loadReadyWorkOgAssets(
	ctx context.Context,
	works []model.Work,
) (map[string]*commonv1.AssetRef, error) {
	candidates := make([]*string, 0, len(works))
	for i := range works {
		candidates = append(candidates, works[i].OgAssetID)
	}
	return loadReadyManageOgAssetRefs(ctx, s.runtime, s.db, candidates...)
}

func (s *WorkService) workResponseWithReadyOg(
	ctx context.Context,
	work *model.Work,
	imageAsset *commonv1.AssetRef,
) (*connect.Response[managev1.Work], error) {
	if err := s.hydrateWorkContentProjection(ctx, work); err != nil {
		return nil, err
	}
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, work.OgAssetID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(s.toProtoWork(work, imageAsset, ogAsset)), nil
}

func (s *WorkService) hydrateWorkContentProjection(ctx context.Context, work *model.Work) error {
	if work == nil {
		return errs.InternalMsg("Work is required")
	}
	if s.contentBlocks == nil {
		return errs.InternalMsg("Work content Block store is not configured")
	}
	if s.mediaHydrator == nil {
		return errs.InternalMsg("Work Block media hydrator is not configured")
	}
	var source *workSourceLocaleMetadata
	var snapshot contentblock.Snapshot
	var mediaItems []*contentv1.ContentBlockMediaItem
	var principal *auth.UserInfo
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var loadErr error
		if _, loadErr = requireLockedWorkPermission(
			ctx, tx, s.spiceDB, work.ID, policyv1.Work.View, workAuthorizationRead,
		); loadErr != nil {
			return loadErr
		}
		principal = auth.GetUser(ctx)
		documentID, loadErr := loadWorkContentDocumentIDForRead(ctx, tx, work.ID)
		if loadErr != nil {
			return loadErr
		}
		source, loadErr = LoadRequiredSourceLocaleMetadata(ctx, tx, work.ID)
		if loadErr != nil {
			return loadErr
		}
		snapshot, loadErr = s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, source.Locale)
		if loadErr != nil {
			return loadErr
		}
		mediaItems, loadErr = LoadContentBlockMediaReferences(ctx, tx, documentID)
		if loadErr != nil {
			return loadErr
		}
		mediaItems, loadErr = s.mediaHydrator.HydrateAuthorizedWorkBlockMediaWithDB(
			ctx, tx, work.ID, documentID, principal, mediaItems,
		)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return normalizeWorkContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return normalizeWorkContentBlockError(err)
	}
	work.ContentDocument = document
	work.ContentRevision = snapshot.Document.Revision.String()
	work.BlockMedia = mediaItems
	return nil
}

func (s *WorkService) hydrateWorkContentProjections(ctx context.Context, works []model.Work) error {
	for index := range works {
		if err := s.hydrateWorkContentProjection(ctx, &works[index]); err != nil {
			return err
		}
	}
	return nil
}

func LoadLocalizedWorkContentProjectionForPublic(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	workID string,
	documentID uuid.UUID,
	locale string,
) (*contentv1.LocalizedRichTextDocument, string, []*contentv1.ContentBlockMediaItem, error) {
	if tx == nil || store == nil {
		return nil, "", nil, errs.InternalMsg("Work content projection dependencies are required")
	}
	source, err := LoadRequiredSourceLocaleMetadata(ctx, tx, workID)
	if err != nil {
		return nil, "", nil, err
	}
	if strings.TrimSpace(locale) == "" {
		locale = source.Locale
	}
	if documentID == uuid.Nil {
		return nil, "", nil, errs.FailedPrecondition("Work content document is not initialized")
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.Locale)
	if err != nil {
		return nil, "", nil, normalizeWorkContentBlockError(err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return nil, "", nil, normalizeWorkContentBlockError(err)
	}
	media, err := LoadContentBlockMediaReferences(ctx, tx, documentID)
	if err != nil {
		return nil, "", nil, err
	}
	return document,
		snapshot.Document.Revision.String(),
		media,
		nil
}

// toProtoWork converts a model.Work to protobuf Work
func (s *WorkService) toProtoWork(
	w *model.Work,
	imageAsset *commonv1.AssetRef,
	ogAsset *commonv1.AssetRef,
) *managev1.Work {
	work := &managev1.Work{
		Id:           w.ID,
		Title:        w.Title,
		Type:         managev1.WorkType(managev1.WorkType_value[w.Type]),
		Year:         w.Year,
		Month:        w.Month,
		IsPresent:    w.IsPresent,
		Featured:     w.Featured,
		Status:       managev1.WorkStatus(managev1.WorkStatus_value[w.Status]),
		CreatedAt:    timestamppb.New(w.CreatedAt),
		UpdatedAt:    timestamppb.New(w.UpdatedAt),
		OgAsset:      ogAsset,
		Document:     w.ContentDocument,
		Revision:     w.ContentRevision,
		SourceLocale: w.SourceLocale,
		BlockMedia:   w.BlockMedia,
	}

	if w.Slug != nil {
		work.Slug = w.Slug
	}
	if w.Summary != nil {
		work.Summary = w.Summary
	}
	if imageAsset != nil {
		work.FeaturedImageAsset = imageAsset
	}
	if w.PublishedAt != nil {
		work.PublishedAt = timestamppb.New(*w.PublishedAt)
	}
	// Convert metadata
	if w.Metadata != nil {
		if metadata, err := structpb.NewStruct(w.Metadata); err == nil {
			work.Metadata = metadata
		}
	}

	if w.MapPlaceID != nil {
		work.MapPlaceId = w.MapPlaceID
	}
	if w.UntilYear != nil {
		work.UntilYear = w.UntilYear
	}
	if w.UntilMonth != nil {
		work.UntilMonth = w.UntilMonth
	}

	return work
}

func (s *WorkService) normalizeMapPlaceID(ctx context.Context, raw string) (*string, error) {
	mapPlaceID := strings.TrimSpace(raw)
	if mapPlaceID == "" {
		return nil, nil
	}

	var count int64
	if err := s.db.WithContext(ctx).
		Table("map_place").
		Where("id = ?", mapPlaceID).
		Count(&count).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if count == 0 {
		return nil, errs.InvalidArgument("map_place_id", "map place does not exist")
	}

	return &mapPlaceID, nil
}

// ============================================================================
// Version History
// ============================================================================

// ListWorkVersions returns a paginated list of version snapshots for a work.
