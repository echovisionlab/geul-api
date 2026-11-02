package og

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// AdminConnectHandler serves the OG-owned methods of the manage Admin service.
type AdminConnectHandler struct {
	admin *Admin
}

func NewAdminConnectHandler(admin *Admin) *AdminConnectHandler {
	return &AdminConnectHandler{admin: admin}
}

func (h *AdminConnectHandler) RegenerateOgImage(
	ctx context.Context,
	req *connect.Request[managev1.RegenerateOgImageRequest],
) (*connect.Response[managev1.RegenerateOgImageResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	plan, err := h.admin.RegenerateOgImage(ctx, RegenerateCommand{
		EntityType: entityTypeName(req.Msg.GetEntityType()),
		EntityID:   req.Msg.GetEntityId(),
		Selection:  SelectionFromProto(req.Msg.GetSelection()),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.RegenerateOgImageResponse{
		RunId: plan.RunID, GenerationIds: plan.GenerationIDs,
	}), nil
}

func (h *AdminConnectHandler) RegenerateAllOgImages(
	ctx context.Context,
	_ *connect.Request[managev1.RegenerateAllOgImagesRequest],
) (*connect.Response[managev1.RegenerateAllOgImagesResponse], error) {
	plan, err := h.admin.RegenerateAllOgImages(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.RegenerateAllOgImagesResponse{
		RunId: plan.RunID, GenerationCount: boundedInt32(int64(len(plan.GenerationIDs))),
	}), nil
}

func (h *AdminConnectHandler) GetOgGeneration(
	ctx context.Context,
	req *connect.Request[managev1.GetOgGenerationRequest],
) (*connect.Response[managev1.GetOgGenerationResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	view, err := h.admin.GetOgGeneration(ctx, req.Msg.GetGenerationId())
	if err != nil {
		return nil, errs.Wrap(err)
	}
	generation, err := generationViewToProto(view)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetOgGenerationResponse{Generation: generation}), nil
}

func (h *AdminConnectHandler) GetLatestOgGeneration(
	ctx context.Context,
	req *connect.Request[managev1.GetLatestOgGenerationRequest],
) (*connect.Response[managev1.GetLatestOgGenerationResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	view, err := h.admin.GetLatestOgGeneration(ctx, targetQueryFromProto(req.Msg.GetTarget()))
	if err != nil {
		return nil, errs.Wrap(err)
	}
	generation, err := generationViewToProto(view)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.GetLatestOgGenerationResponse{Generation: generation}), nil
}

func (h *AdminConnectHandler) GetOgGenerationRun(
	ctx context.Context,
	req *connect.Request[managev1.GetOgGenerationRunRequest],
) (*connect.Response[managev1.GetOgGenerationRunResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	view, err := h.admin.GetOgGenerationRun(ctx, req.Msg.GetRunId())
	if err != nil {
		return nil, errs.Wrap(err)
	}
	run := &managev1.OgGenerationRun{
		RunId: view.Run.ID, Status: runStatusToProto(view.Run.Status), GenerationCount: int32(len(view.Generations)),
		CreatedAt: timestamppb.New(view.Run.CreatedAt), UpdatedAt: timestamppb.New(view.Run.UpdatedAt),
	}
	if view.Run.CompletedAt != nil {
		run.CompletedAt = timestamppb.New(*view.Run.CompletedAt)
	}
	for _, generation := range view.Generations {
		switch generation.Status {
		case StatusQueued:
			run.QueuedCount++
		case StatusProcessing:
			run.ProcessingCount++
		case StatusReady:
			run.ReadyCount++
		case StatusFailed:
			run.FailedCount++
		case StatusSuperseded:
			run.SupersededCount++
		case StatusCancelled:
			run.CancelledCount++
		}
		if generation.Status != StatusFailed {
			continue
		}
		target, err := TargetToProto(view.FailedTargets[generation.ID])
		if err != nil {
			return nil, errs.Internal(err)
		}
		run.Failures = append(run.Failures, &managev1.OgGenerationFailure{
			GenerationId: generation.ID, Target: target,
			ErrorCode: stringValue(generation.LastErrorCode),
		})
	}
	return connect.NewResponse(&managev1.GetOgGenerationRunResponse{Run: run}), nil
}

func generationViewToProto(view GenerationView) (*managev1.OgGeneration, error) {
	target, err := TargetToProto(view.Target)
	if err != nil {
		return nil, errs.Internal(err)
	}
	generation := &managev1.OgGeneration{
		GenerationId: view.Generation.ID, RunId: view.Generation.RunID, Target: target,
		Status:                  StatusToProto(view.Generation.Status),
		ErrorCode:               optionalString(view.Generation.LastErrorCode),
		ReplacementGenerationId: optionalString(view.Generation.SupersededByID),
		CreatedAt:               timestamppb.New(view.Generation.CreatedAt), UpdatedAt: timestamppb.New(view.Generation.UpdatedAt),
	}
	if view.Generation.CompletedAt != nil {
		generation.CompletedAt = timestamppb.New(*view.Generation.CompletedAt)
	}
	generation.Asset = AssetRefToProto(view.Asset)
	return generation, nil
}

func targetQueryFromProto(target *managev1.OgGenerationTarget) *TargetQuery {
	if target == nil {
		return nil
	}
	query := &TargetQuery{EntityType: entityTypeName(target.GetEntityType()), EntityID: target.GetEntityId()}
	switch {
	case target.GetEntity() != nil:
		query.Scope = ScopeEntity
	case target.GetLocale() != nil:
		query.Scope = ScopeLocale
		query.Locale = target.GetLocale().GetLocale()
	}
	return query
}

// InternalConnectHandler serves the renderer's internal OG service.
type InternalConnectHandler struct {
	lifecycle *Lifecycle
}

func NewInternalConnectHandler(lifecycle *Lifecycle) *InternalConnectHandler {
	return &InternalConnectHandler{lifecycle: lifecycle}
}

func (h *InternalConnectHandler) ClaimOgGeneration(
	ctx context.Context,
	req *connect.Request[intrav1.ClaimOgGenerationRequest],
) (*connect.Response[intrav1.ClaimOgGenerationResponse], error) {
	claim, err := h.lifecycle.Claim(ctx, req.Msg.GetGenerationId())
	if err != nil {
		return nil, lifecycleError(err)
	}
	response := &intrav1.ClaimOgGenerationResponse{
		Result:           claimResultToProto(claim.Result),
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
	response.Target, err = TargetToProto(claim.Target)
	if err != nil {
		return nil, errs.Internal(err)
	}
	response.Title = optionalString(&claim.EntitySnapshot.Title)
	response.FeaturedImage = claim.EntitySnapshot.FeaturedImage.toProto()
	response.Output = claim.EntitySnapshot.Output.toProto()
	response.RenderConfig, err = decodeRenderConfigSnapshot(claim.RenderConfigSnapshot, claim.ConfigRevision)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(response), nil
}

func (h *InternalConnectHandler) CompleteOgGeneration(
	ctx context.Context,
	req *connect.Request[intrav1.CompleteOgGenerationRequest],
) (*connect.Response[intrav1.CompleteOgGenerationResponse], error) {
	var written *WrittenAsset
	if result := req.Msg.GetWritten(); result != nil {
		written = &WrittenAsset{AssetID: result.GetAssetId(), FileSize: result.GetFileSize(), SHA256: result.GetSha256()}
	}
	status, asset, err := h.lifecycle.Complete(ctx, req.Msg.GetGenerationId(), req.Msg.GetLeaseToken(), written)
	if err != nil {
		return nil, lifecycleError(err)
	}
	return connect.NewResponse(&intrav1.CompleteOgGenerationResponse{
		Status: StatusToProto(status),
		Asset:  AssetRefToProto(asset),
	}), nil
}

func (h *InternalConnectHandler) FailOgGeneration(
	ctx context.Context,
	req *connect.Request[intrav1.FailOgGenerationRequest],
) (*connect.Response[intrav1.FailOgGenerationResponse], error) {
	if err := h.lifecycle.Fail(
		ctx,
		req.Msg.GetGenerationId(),
		req.Msg.GetLeaseToken(),
		req.Msg.GetErrorCode(),
	); err != nil {
		return nil, lifecycleError(err)
	}
	status, err := h.lifecycle.GenerationStatus(ctx, req.Msg.GetGenerationId())
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.FailOgGenerationResponse{Status: StatusToProto(status)}), nil
}

// lifecycleError answers a row the transaction found missing as an unnamed
// missing generation.
func lifecycleError(err error) error {
	if _, ok := errs.Classified(err); ok {
		// Wrap keeps the outer operation context behind the classified answer.
		return errs.Wrap(err)
	}
	if missingRow(err) {
		return errs.NotFound("og_generation", "")
	}
	return errs.Internal(err)
}

func claimResultToProto(result ClaimResult) intrav1.OgGenerationClaimResult {
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

func decodeRenderConfigSnapshot(payload []byte, revision string) (*intrav1.OgRenderConfigSnapshot, error) {
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

// AssetRefToProto renders a ready OG asset reference; nil stays nil.
func AssetRefToProto(asset *AssetRef) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{
		AssetId:     asset.AssetID,
		Url:         asset.URL,
		Extension:   asset.Extension,
		MimeType:    asset.MimeType,
		FileSize:    asset.FileSize,
		Sha256:      append([]byte(nil), asset.SHA256...),
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}

func TargetToProto(target GenerationTarget) (*managev1.OgGenerationTarget, error) {
	entityType := EntityTypeForName(target.EntityType)
	if entityType == managev1.OgEntityType_OG_ENTITY_TYPE_UNSPECIFIED {
		return nil, fmt.Errorf("unsupported OG generation target entity type %q", target.EntityType)
	}
	result := &managev1.OgGenerationTarget{EntityType: entityType, EntityId: target.EntityID}
	switch target.TargetKind {
	case KindEntity:
		result.Scope = &managev1.OgGenerationTarget_Entity{Entity: &managev1.OgEntityTarget{}}
	case KindLocale:
		if target.Locale == nil || strings.TrimSpace(*target.Locale) == "" {
			return nil, errors.New("locale OG target is missing locale")
		}
		result.Scope = &managev1.OgGenerationTarget_Locale{
			Locale: &managev1.OgLocaleTarget{Locale: strings.TrimSpace(*target.Locale)},
		}
	default:
		return nil, fmt.Errorf("unsupported OG target kind %q", target.TargetKind)
	}
	return result, nil
}

func StatusToProto(status GenerationStatus) managev1.OgGenerationStatus {
	switch status {
	case StatusQueued:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_QUEUED
	case StatusProcessing:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_PROCESSING
	case StatusReady:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_READY
	case StatusFailed:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_FAILED
	case StatusSuperseded:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_SUPERSEDED
	case StatusCancelled:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_CANCELLED
	default:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_UNSPECIFIED
	}
}

func runStatusToProto(status RunStatus) managev1.OgGenerationRunStatus {
	switch status {
	case RunQueued:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_QUEUED
	case RunRunning:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_PROCESSING
	case RunReady:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_READY
	case RunPartialFailed:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_PARTIALLY_FAILED
	case RunFailed:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_FAILED
	case RunCancelled:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_CANCELLED
	default:
		return managev1.OgGenerationRunStatus_OG_GENERATION_RUN_STATUS_UNSPECIFIED
	}
}

// entityTypes maps the API entity types to their policy names.
var entityTypes = map[managev1.OgEntityType]string{
	managev1.OgEntityType_OG_ENTITY_TYPE_POST:    "post",
	managev1.OgEntityType_OG_ENTITY_TYPE_PAGE:    "page",
	managev1.OgEntityType_OG_ENTITY_TYPE_WORK:    "work",
	managev1.OgEntityType_OG_ENTITY_TYPE_LABEL:   "label",
	managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST:  "artist",
	managev1.OgEntityType_OG_ENTITY_TYPE_SITE:    entitySite,
	managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE: entityRelease,
	managev1.OgEntityType_OG_ENTITY_TYPE_SERIES:  "series",
	managev1.OgEntityType_OG_ENTITY_TYPE_FORM:    "form",
	managev1.OgEntityType_OG_ENTITY_TYPE_PRIVACY: "privacy",
	managev1.OgEntityType_OG_ENTITY_TYPE_TERMS:   "terms",
}

// PolicyForEntityType returns the policy of an API entity type.
func PolicyForEntityType(entityType managev1.OgEntityType) (Policy, bool) {
	name, ok := entityTypes[entityType]
	if !ok {
		return Policy{}, false
	}
	return PolicyForEntityName(name)
}

// EntityTypeForName returns the API entity type of a policy name.
func EntityTypeForName(name string) managev1.OgEntityType {
	for entityType, entityName := range entityTypes {
		if entityName == name {
			return entityType
		}
	}
	return managev1.OgEntityType_OG_ENTITY_TYPE_UNSPECIFIED
}

// entityTypeName is the policy name a use case receives for an API entity
// type; an unknown type keeps the name the client sent, which no policy has.
func entityTypeName(entityType managev1.OgEntityType) string {
	if policy, ok := PolicyForEntityType(entityType); ok {
		return policy.Name
	}
	return entityType.String()
}

// SelectionFromProto reads a client's target selection.
func SelectionFromProto(selection *managev1.OgTargetSelection) Selection {
	switch {
	case selection == nil || selection.GetTarget() == nil:
		return Selection{Kind: SelectionMissing}
	case selection.GetPrimary() != nil:
		return Selection{Kind: SelectionPrimary}
	case selection.GetAllLocales() != nil:
		return Selection{Kind: SelectionAllLocales}
	default:
		return Selection{Kind: SelectionLocale, Locale: selection.GetLocale()}
	}
}

// SelectionToProto renders a selection for a request source.
func SelectionToProto(selection Selection) *managev1.OgTargetSelection {
	switch selection.Kind {
	case SelectionPrimary:
		return &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{Primary: &managev1.OgPrimaryTarget{}}}
	case SelectionAllLocales:
		return &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_AllLocales{AllLocales: &managev1.OgAllLocaleTargets{}}}
	case SelectionLocale:
		return &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Locale{Locale: selection.Locale}}
	default:
		return nil
	}
}

// EntityTypeName returns the policy name of an API entity type, or an
// invalid entity type answer naming the type the caller sent.
func EntityTypeName(entityType managev1.OgEntityType) (string, error) {
	policy, ok := PolicyForEntityType(entityType)
	if !ok {
		return "", errs.InvalidEntityType(entityType.String())
	}
	return policy.Name, nil
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
