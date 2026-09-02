package og

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// InternalService provides internal-only access for OG image operations.
type InternalService struct {
	db        *gorm.DB
	cdnDomain string
	lifecycle *Lifecycle
}

func NewInternalService(db *gorm.DB, cdnDomain string, projections ...Projection) *InternalService {
	if db == nil {
		panic("InternalService: db is required")
	}
	if strings.TrimSpace(cdnDomain) == "" {
		panic("InternalService: cdnDomain is required")
	}
	canonicalCDNDomain := strings.TrimRight(strings.TrimSpace(cdnDomain), "/")
	return &InternalService{
		db:        db,
		cdnDomain: canonicalCDNDomain,
		lifecycle: NewLifecycle(db, canonicalCDNDomain, projections...),
	}
}

func (s *InternalService) ClaimOgGeneration(
	ctx context.Context,
	req *connect.Request[intrav1.ClaimOgGenerationRequest],
) (*connect.Response[intrav1.ClaimOgGenerationResponse], error) {
	claim, err := s.lifecycle.Claim(ctx, req.Msg.GetGenerationId())
	if err != nil {
		return nil, mapOgLifecycleError(err)
	}
	response := &intrav1.ClaimOgGenerationResponse{
		Result:           ogClaimResultToProto(claim.Result),
		GenerationStatus: StatusToProto(claim.Generation.Status),
	}
	if claim.LeaseExpiresAt != nil {
		response.LeaseExpiresAt = timestamppb.New(*claim.LeaseExpiresAt)
	}
	if claim.Result != Claimed {
		return connect.NewResponse(response), nil
	}
	response.LeaseToken = &claim.LeaseToken
	response.RunId = &claim.Generation.RunID
	response.Target, err = TargetToProto(&claim.Target)
	if err != nil {
		return nil, errs.Internal(err)
	}
	response.Title = optionalString(&claim.EntitySnapshot.Title)
	response.FeaturedImage = claim.EntitySnapshot.FeaturedImage.toProto()
	response.Output = claim.EntitySnapshot.Output.toProto()
	response.RenderConfig, err = decodeOgRenderConfigSnapshot(claim.RenderConfigSnapshot, claim.ConfigRevision)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(response), nil
}

func (s *InternalService) CompleteOgGeneration(
	ctx context.Context,
	req *connect.Request[intrav1.CompleteOgGenerationRequest],
) (*connect.Response[intrav1.CompleteOgGenerationResponse], error) {
	status, asset, err := s.lifecycle.Complete(
		ctx,
		req.Msg.GetGenerationId(),
		req.Msg.GetLeaseToken(),
		req.Msg.GetWritten(),
	)
	if err != nil {
		return nil, mapOgLifecycleError(err)
	}
	var assetRef *commonv1.AssetRef
	if asset != nil {
		assetRef, err = mediaasset.NewLifecycle(s.db, s.cdnDomain).AssetRef(*asset)
		if err != nil {
			return nil, errs.Internal(err)
		}
	}
	return connect.NewResponse(&intrav1.CompleteOgGenerationResponse{
		Status: StatusToProto(status),
		Asset:  assetRef,
	}), nil
}

func (s *InternalService) FailOgGeneration(
	ctx context.Context,
	req *connect.Request[intrav1.FailOgGenerationRequest],
) (*connect.Response[intrav1.FailOgGenerationResponse], error) {
	if err := s.lifecycle.Fail(
		ctx,
		req.Msg.GetGenerationId(),
		req.Msg.GetLeaseToken(),
		req.Msg.GetErrorCode(),
	); err != nil {
		return nil, mapOgLifecycleError(err)
	}
	var generation model.OgGeneration
	if err := s.db.WithContext(ctx).First(&generation, "id = ?", req.Msg.GetGenerationId()).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.FailOgGenerationResponse{
		Status: StatusToProto(generation.Status),
	}), nil
}

func mapOgLifecycleError(err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.NotFound("og_generation", "")
	}
	return errs.Internal(err)
}

func ogClaimResultToProto(result ClaimResult) intrav1.OgGenerationClaimResult {
	switch result {
	case Claimed:
		return intrav1.OgGenerationClaimResult_OG_GENERATION_CLAIM_RESULT_CLAIMED
	default:
		return intrav1.OgGenerationClaimResult_OG_GENERATION_CLAIM_RESULT_SKIP
	}
}

func (s *ogAssetRefSnapshot) toProto() *commonv1.AssetRef {
	if s == nil {
		return nil
	}
	return &commonv1.AssetRef{AssetId: s.AssetID, Url: s.URL, Extension: s.Extension, MimeType: s.MimeType}
}

func (s ogOutputSnapshot) toProto() *commonv1.AssetWriteTarget {
	return &commonv1.AssetWriteTarget{
		AssetId:     s.AssetID,
		ObjectKey:   s.ObjectKey,
		Extension:   s.Extension,
		MimeType:    s.MimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}

func decodeOgRenderConfigSnapshot(payload []byte, revision string) (*intrav1.OgRenderConfigSnapshot, error) {
	var snapshot ogRenderConfigSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	response := &intrav1.OgRenderConfigSnapshot{
		SiteTitle:    snapshot.SiteTitle,
		PrimaryColor: snapshot.PrimaryColor,
		LogoAsset:    snapshot.LogoAsset.toProto(),
		Revision:     revision,
	}
	if len(snapshot.OGImageConfig) > 0 {
		var value structured.Fields
		if err := json.Unmarshal(snapshot.OGImageConfig, &value); err != nil {
			return nil, err
		}
		config, err := structpb.NewStruct(value)
		if err != nil {
			return nil, err
		}
		response.OgImageConfig = config
	}
	return response, nil
}
