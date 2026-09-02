package og

import (
	"context"
	"log/slog"
	"math"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// AdminAuthorizer owns account and resource authorization outside the OG lifecycle.
type AdminAuthorizer interface {
	RequireAuthenticated(context.Context) error
	RequireAdmin(context.Context) error
	AuthorizeEntity(context.Context, string, string, bool) error
}

// GlobalReconciler lets owning domains repair their own legacy OG source state
// before a global collection without exposing their tables to OG.
type GlobalReconciler interface {
	ReconcileBeforeGlobalGeneration(context.Context, *gorm.DB, string) error
}

// AdminService implements the OG-owned methods of the manage Admin service.
type AdminService struct {
	db         *gorm.DB
	cdnDomain  string
	planner    *Planner
	resolver   *Resolver
	collector  *Collector
	authorizer AdminAuthorizer
	reconciler GlobalReconciler
}

func NewAdminService(
	db *gorm.DB,
	cdnDomain string,
	planner *Planner,
	resolver *Resolver,
	collector *Collector,
	authorizer AdminAuthorizer,
	reconciler GlobalReconciler,
) *AdminService {
	if db == nil || planner == nil || resolver == nil || collector == nil || authorizer == nil || reconciler == nil {
		panic("OG admin service dependencies are required")
	}
	return &AdminService{
		db: db, cdnDomain: cdnDomain, planner: planner, resolver: resolver,
		collector: collector, authorizer: authorizer, reconciler: reconciler,
	}
}

// RegenerateOgImage durably creates a generation run for the selected target.
func (s *AdminService) RegenerateOgImage(
	ctx context.Context,
	req *connect.Request[managev1.RegenerateOgImageRequest],
) (*connect.Response[managev1.RegenerateOgImageResponse], error) {
	if err := s.authorizer.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	if err := s.authorizeOgEntityTarget(
		ctx,
		req.Msg.GetEntityType(),
		normalizeOgAuthorizationEntityID(req.Msg.GetEntityType(), req.Msg.GetEntityId()),
		true,
	); err != nil {
		return nil, err
	}
	var plan *Plan
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		requests, err := s.resolver.Resolve(ctx, tx, req.Msg)
		if err != nil {
			return err
		}
		plan, err = s.planner.RequestBulkReloadedWithDB(
			ctx, tx, "manual", "admin_regenerate", requests,
			func(reloadCtx context.Context, reloadTx *gorm.DB) ([]Request, error) {
				return s.resolver.Resolve(reloadCtx, reloadTx, req.Msg)
			},
		)
		return err
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.RegenerateOgImageResponse{
		RunId: plan.RunID, GenerationIds: plan.GenerationIDs,
	}), nil
}

// RegenerateAllOgImages creates one atomic run for every concrete OG target.
func (s *AdminService) RegenerateAllOgImages(
	ctx context.Context,
	req *connect.Request[managev1.RegenerateAllOgImagesRequest],
) (*connect.Response[managev1.RegenerateAllOgImagesResponse], error) {
	if err := s.authorizer.RequireAdmin(ctx); err != nil {
		return nil, err
	}
	_ = req
	var plan *Plan
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.reconciler.ReconcileBeforeGlobalGeneration(ctx, tx, s.cdnDomain); err != nil {
			return err
		}
		requests, err := s.collector.Collect(ctx, tx)
		if err != nil {
			return err
		}
		plan, err = s.planner.RequestBulkReloadedWithDB(
			ctx, tx, "manual", "admin_regenerate_all", requests,
			func(reloadCtx context.Context, reloadTx *gorm.DB) ([]Request, error) {
				return s.collector.Collect(reloadCtx, reloadTx)
			},
		)
		return err
	})
	if err != nil {
		return nil, errs.Internal(err)
	}
	slog.Info("queued one OG regeneration run for all targets", "runId", plan.RunID, "generationCount", len(plan.GenerationIDs))
	return connect.NewResponse(&managev1.RegenerateAllOgImagesResponse{
		RunId: plan.RunID, GenerationCount: boundedInt32(int64(len(plan.GenerationIDs))),
	}), nil
}

func boundedInt32(value int64) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}
