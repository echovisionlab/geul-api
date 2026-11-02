package work

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/memberref"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/structured"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConnectHandler serves the Work management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedWorkServiceHandler
	works *Service
}

// NewConnectHandler returns the Connect adapter of works.
func NewConnectHandler(works *Service) *ConnectHandler {
	return &ConnectHandler{works: works}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// render translates a use case's result into its message.
func render[R, T any](result R, err error, message func(R) *T) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message(result)), nil
}

func deleted(err error) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func page(request interface {
	GetLimit() int32
	GetOffset() int32
}) Page {
	return Page{Limit: request.GetLimit(), Offset: request.GetOffset()}
}

func workTypeName(workType managev1.WorkType) string {
	if workType == managev1.WorkType_WORK_TYPE_UNSPECIFIED {
		return ""
	}
	return workType.String()
}

// WorkType is the API enum of a stored Work type; a value the API does not
// know is UNSPECIFIED.
func WorkType(stored string) managev1.WorkType {
	return managev1.WorkType(managev1.WorkType_value[stored])
}

// workStatusToProto maps a stored Work status to the API enum. A value the
// API does not know is UNSPECIFIED.
func workStatusToProto(status Status) managev1.WorkStatus {
	switch status {
	case StatusDraft:
		return managev1.WorkStatus_WORK_STATUS_DRAFT
	case StatusPublished:
		return managev1.WorkStatus_WORK_STATUS_PUBLISHED
	case StatusArchived:
		return managev1.WorkStatus_WORK_STATUS_ARCHIVED
	default:
		return managev1.WorkStatus_WORK_STATUS_UNSPECIFIED
	}
}

// WorkStatusName is the API name of a stored Work status.
func WorkStatusName(status Status) string { return workStatusToProto(status).String() }

// fieldsOf is the stored form of a present Struct; an absent one is nil.
func fieldsOf(value *structpb.Struct) structured.Fields {
	if value == nil {
		return nil
	}
	return value.AsMap()
}

func clientIDsOf(value *managev1.WorkClientsUpdate) *[]string {
	if value == nil {
		return nil
	}
	ids := value.GetClientIds()
	return &ids
}

func timestampProtoPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func paginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

// MetadataStruct is the API form of a Work's metadata, or none when it
// cannot be converted.
func MetadataStruct(ctx context.Context, workID string, metadata structured.Fields) *structpb.Struct {
	if metadata == nil {
		return nil
	}
	converted, err := structpb.NewStruct(metadata)
	if err != nil {
		slog.ErrorContext(ctx, "Work metadata conversion failed; serving work without metadata", "domain", "work", "operation", "convert_work_metadata", failurecause.Attr(err), "work_id", workID)
		return nil
	}
	return converted
}

// workMessage renders a managed Work.
func workMessage(ctx context.Context, w View) *managev1.Work {
	work := &managev1.Work{
		Id:           w.ID,
		Title:        w.Title,
		Type:         WorkType(w.Type),
		Year:         w.Year,
		Month:        w.Month,
		IsPresent:    w.IsPresent,
		Featured:     w.Featured,
		Status:       workStatusToProto(w.Status),
		CreatedAt:    timestamppb.New(w.CreatedAt),
		UpdatedAt:    timestamppb.New(w.UpdatedAt),
		OgAsset:      mediaasset.ReferenceProto(w.OgAsset),
		Document:     w.Document,
		Revision:     w.Revision,
		SourceLocale: w.SourceLocale,
		BlockMedia:   fileref.BlockMediaItemsProto(w.BlockMedia),
	}
	if w.Slug != nil {
		work.Slug = w.Slug
	}
	if w.Summary != nil {
		work.Summary = w.Summary
	}
	if w.FeaturedImage != nil {
		work.FeaturedImageAsset = mediaasset.ReferenceProto(w.FeaturedImage)
	}
	if w.PublishedAt != nil {
		work.PublishedAt = timestamppb.New(*w.PublishedAt)
	}
	work.Metadata = MetadataStruct(ctx, w.ID, w.Metadata)
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

func clientMessages(clients []Client) []*managev1.WorkClient {
	if clients == nil {
		return nil
	}
	messages := make([]*managev1.WorkClient, 0, len(clients))
	for _, client := range clients {
		message := &managev1.WorkClient{Id: client.ID, Name: client.Name, LogoLightAsset: mediaasset.ReferenceProto(client.LightLogo), LogoDarkAsset: mediaasset.ReferenceProto(client.DarkLogo)}
		if client.Website != nil {
			message.Website = client.Website
		}
		messages = append(messages, message)
	}
	return messages
}

func lifecycleMessage(change LifecycleChange) *managev1.WorkLifecycleMutationResponse {
	return &managev1.WorkLifecycleMutationResponse{
		Id: change.ID, Changed: change.Changed,
		Status:      workStatusToProto(change.Status),
		PublishedAt: timestampProtoPtr(change.PublishedAt), UpdatedAt: timestamppb.New(change.UpdatedAt),
	}
}

func creditGroupMessage(group CreditGroup) *managev1.WorkCreditGroup {
	return &managev1.WorkCreditGroup{Id: group.ID, WorkId: group.WorkID, Name: group.Name}
}

func creditMessage(view CreditView) *managev1.WorkCredit {
	c := view.Credit
	credit := &managev1.WorkCredit{Id: c.ID}
	if c.GroupID != nil {
		credit.GroupId = c.GroupID
	}
	if c.Name != nil {
		credit.Name = c.Name
	}
	if c.CreditRole != nil {
		credit.CreditRole = c.CreditRole
	}
	if artist := view.Artist; artist != nil {
		credit.Artist = &managev1.CreditArtist{Id: artist.ID, Name: artist.Name, Slug: artist.Slug, ImageAsset: mediaasset.ReferenceProto(artist.ImageAsset)}
	}
	if c.MemberID != nil {
		credit.Member = memberref.ReferenceMessage(view.Member)
	}
	return credit
}

// orderMessages renders the credit order; a credit names its group, empty
// when ungrouped.
func orderMessages(order []OrderItem) []*managev1.WorkCreditOrderItem {
	result := make([]*managev1.WorkCreditOrderItem, 0, len(order))
	for _, entry := range order {
		item := &managev1.WorkCreditOrderItem{Kind: managev1.WorkCreditItemKind(entry.Kind), Id: entry.ID}
		if entry.Kind == CreditItemCredit {
			groupID := ""
			if entry.GroupID != nil {
				groupID = *entry.GroupID
			}
			item.GroupId = &groupID
		}
		result = append(result, item)
	}
	return result
}

func orderItemOf(item *managev1.WorkCreditOrderItem) *OrderItem {
	if item == nil {
		return nil
	}
	return &OrderItem{Kind: CreditItemKind(item.Kind), ID: item.Id, GroupID: item.GroupId}
}

func (h *ConnectHandler) ListWorksAdmin(ctx context.Context, req *connect.Request[managev1.ListWorksAdminRequest]) (*connect.Response[managev1.ListWorksAdminResponse], error) {
	listed, err := h.works.ListWorksAdmin(ctx, ListWorksAdminQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts})
	return render(listed, err, func(listed AdminWorkPage) *managev1.ListWorksAdminResponse {
		works := make([]*managev1.WorkWithStats, len(listed.Works))
		for i, work := range listed.Works {
			works[i] = &managev1.WorkWithStats{Work: workMessage(ctx, work.View), CreditCount: work.CreditCount, ClientCount: work.ClientCount}
		}
		return &managev1.ListWorksAdminResponse{Works: works, Pagination: paginationMessage(listed.Pagination)}
	})
}

func (h *ConnectHandler) CreateWork(ctx context.Context, req *connect.Request[managev1.CreateWorkRequest]) (*connect.Response[managev1.Work], error) {
	msg := req.Msg
	created, err := h.works.CreateWork(ctx, CreateWorkCommand{
		Title: msg.Title, Slug: msg.Slug, Type: workTypeName(msg.Type), Summary: msg.Summary,
		Metadata: fieldsOf(msg.Metadata), Featured: msg.Featured, Year: msg.Year, Month: msg.Month,
		UntilYear: msg.UntilYear, UntilMonth: msg.UntilMonth, IsPresent: msg.IsPresent,
		SourceLocale: msg.SourceLocale, Document: msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(created, err, func(view View) *managev1.Work { return workMessage(ctx, view) })
}

func (h *ConnectHandler) DeleteWork(ctx context.Context, req *connect.Request[managev1.DeleteWorkRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.works.DeleteWork(ctx, DeleteWorkCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishWork(ctx context.Context, req *connect.Request[managev1.PublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error) {
	change, err := h.works.PublishWork(ctx, PublishWorkCommand{ID: req.Msg.Id})
	return render(change, err, lifecycleMessage)
}

func (h *ConnectHandler) UnpublishWork(ctx context.Context, req *connect.Request[managev1.UnpublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error) {
	change, err := h.works.UnpublishWork(ctx, UnpublishWorkCommand{ID: req.Msg.Id})
	return render(change, err, lifecycleMessage)
}

func (h *ConnectHandler) GetWork(ctx context.Context, req *connect.Request[managev1.GetWorkRequest]) (*connect.Response[managev1.Work], error) {
	work, err := h.works.GetWork(ctx, GetWorkQuery{ID: req.Msg.Id})
	return render(work, err, func(view View) *managev1.Work {
		message := workMessage(ctx, view)
		message.Clients = clientMessages(view.Clients)
		return message
	})
}

func (h *ConnectHandler) UpdateWork(ctx context.Context, req *connect.Request[managev1.UpdateWorkRequest]) (*connect.Response[managev1.UpdateWorkResponse], error) {
	msg := req.Msg
	command := UpdateWorkCommand{
		ID: msg.Id, Slug: msg.Slug, Metadata: fieldsOf(msg.Metadata), ObservedMetadata: fieldsOf(msg.ObservedMetadata),
		Featured: msg.Featured, Clients: clientIDsOf(msg.Clients), ObservedClients: clientIDsOf(msg.ObservedClients),
		Year: msg.Year, Month: msg.Month, UntilYear: msg.UntilYear, UntilMonth: msg.UntilMonth,
		IsPresent: msg.IsPresent, MapPlaceID: msg.MapPlaceId,
	}
	if msg.Type != nil {
		workType := msg.Type.String()
		command.Type = &workType
	}
	update, err := h.works.UpdateWork(ctx, command)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(updateMessage(update))
}

func updateMessage(update Update) (*managev1.UpdateWorkResponse, error) {
	work := update.Work
	metadata, err := structpb.NewStruct(work.Metadata)
	if err != nil {
		return nil, err
	}
	return &managev1.UpdateWorkResponse{
		Id:         work.ID,
		Changed:    update.Changed,
		Slug:       work.Slug,
		Type:       WorkType(work.Type),
		Metadata:   metadata,
		Featured:   work.Featured,
		Clients:    clientMessages(update.Clients),
		Year:       work.Year,
		Month:      work.Month,
		UntilYear:  work.UntilYear,
		UntilMonth: work.UntilMonth,
		IsPresent:  work.IsPresent,
		MapPlaceId: work.MapPlaceID,
		UpdatedAt:  timestamppb.New(work.UpdatedAt),
	}, nil
}

func (h *ConnectHandler) SetWorkFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetWorkFeaturedImageRequest]) (*connect.Response[managev1.SetWorkFeaturedImageResponse], error) {
	change, err := h.works.SetWorkFeaturedImage(ctx, SetWorkFeaturedImageCommand{WorkID: req.Msg.WorkId, FileID: req.Msg.FileId})
	return render(change, err, func(change FeaturedImageChange) *managev1.SetWorkFeaturedImageResponse {
		return &managev1.SetWorkFeaturedImageResponse{ImageAsset: mediaasset.ReferenceProto(change.ImageAsset), OgGenerationRunId: change.OgGenerationRunID}
	})
}

func (h *ConnectHandler) DeleteWorkFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeleteWorkFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	runID, err := h.works.DeleteWorkFeaturedImage(ctx, DeleteWorkFeaturedImageCommand{WorkID: req.Msg.WorkId})
	return render(runID, err, func(runID *string) *managev1.OgAssetDeleteResponse {
		return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: runID}
	})
}

// CreditsMessage renders a Work's attribution.
func CreditsMessage(credits Credits) *managev1.GetWorkCreditsResponse {
	groups := make([]*managev1.WorkCreditGroup, len(credits.Groups))
	for i := range credits.Groups {
		groups[i] = creditGroupMessage(credits.Groups[i])
	}
	messages := make([]*managev1.WorkCredit, len(credits.Credits))
	for i := range credits.Credits {
		messages[i] = creditMessage(credits.Credits[i])
	}
	return &managev1.GetWorkCreditsResponse{Groups: groups, Credits: messages, Order: orderMessages(credits.Order)}
}

// CreditGroupMessage and CreditMessage render one credit group and credit.
func CreditGroupMessage(group CreditGroup) *managev1.WorkCreditGroup {
	return creditGroupMessage(group)
}
func CreditMessage(view CreditView) *managev1.WorkCredit { return creditMessage(view) }

func (h *ConnectHandler) GetWorkCredits(ctx context.Context, req *connect.Request[managev1.GetWorkCreditsRequest]) (*connect.Response[managev1.GetWorkCreditsResponse], error) {
	credits, err := h.works.GetWorkCredits(ctx, GetWorkCreditsQuery{WorkID: req.Msg.WorkId})
	return render(credits, err, CreditsMessage)
}

func (h *ConnectHandler) AddWorkCredit(ctx context.Context, req *connect.Request[managev1.AddWorkCreditRequest]) (*connect.Response[managev1.WorkCredit], error) {
	msg := req.Msg
	credit, err := h.works.AddWorkCredit(ctx, AddWorkCreditCommand{
		WorkID: msg.WorkId, GroupID: msg.GroupId, ArtistID: msg.ArtistId, MemberID: msg.MemberId, Name: msg.Name, CreditRole: msg.CreditRole,
	})
	return render(credit, err, creditMessage)
}

func (h *ConnectHandler) UpdateWorkCredit(ctx context.Context, req *connect.Request[managev1.UpdateWorkCreditRequest]) (*connect.Response[managev1.WorkCredit], error) {
	credit, err := h.works.UpdateWorkCredit(ctx, UpdateWorkCreditCommand{CreditID: req.Msg.CreditId, GroupID: req.Msg.GroupId, CreditRole: req.Msg.CreditRole})
	return render(credit, err, creditMessage)
}

func (h *ConnectHandler) DeleteWorkCredit(ctx context.Context, req *connect.Request[managev1.DeleteWorkCreditRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.works.DeleteWorkCredit(ctx, DeleteWorkCreditCommand{CreditID: req.Msg.CreditId}))
}

func (h *ConnectHandler) MoveWorkCreditItem(ctx context.Context, req *connect.Request[managev1.MoveWorkCreditItemRequest]) (*connect.Response[managev1.MoveWorkCreditItemResponse], error) {
	msg := req.Msg
	move, err := h.works.MoveWorkCreditItem(ctx, MoveWorkCreditItemCommand{
		WorkID: msg.WorkId, Kind: CreditItemKind(msg.Kind), ItemID: msg.ItemId, TargetGroupID: msg.TargetGroupId,
		After: orderItemOf(msg.After), Before: orderItemOf(msg.Before),
	})
	return render(move, err, func(move CreditMove) *managev1.MoveWorkCreditItemResponse {
		return &managev1.MoveWorkCreditItemResponse{Items: orderMessages(move.Items), Changed: move.Changed}
	})
}

func (h *ConnectHandler) CreateWorkCreditGroup(ctx context.Context, req *connect.Request[managev1.CreateWorkCreditGroupRequest]) (*connect.Response[managev1.WorkCreditGroup], error) {
	group, err := h.works.CreateWorkCreditGroup(ctx, CreateWorkCreditGroupCommand{WorkID: req.Msg.WorkId, Name: req.Msg.Name})
	return render(group, err, creditGroupMessage)
}

func (h *ConnectHandler) UpdateWorkCreditGroup(ctx context.Context, req *connect.Request[managev1.UpdateWorkCreditGroupRequest]) (*connect.Response[managev1.WorkCreditGroup], error) {
	group, err := h.works.UpdateWorkCreditGroup(ctx, UpdateWorkCreditGroupCommand{GroupID: req.Msg.GroupId, Name: req.Msg.Name})
	return render(group, err, creditGroupMessage)
}

func (h *ConnectHandler) DeleteWorkCreditGroup(ctx context.Context, req *connect.Request[managev1.DeleteWorkCreditGroupRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.works.DeleteWorkCreditGroup(ctx, DeleteWorkCreditGroupCommand{GroupID: req.Msg.GroupId}))
}

func creditedWorkMessage(credited CreditedWork) *managev1.MyCreditedWork {
	workType := WorkType(credited.Type)
	if workType == managev1.WorkType_WORK_TYPE_UNSPECIFIED {
		workType = managev1.WorkType_WORK_TYPE_MUSIC_PROJECT
	}
	workStatus := workStatusToProto(credited.Status)
	if workStatus == managev1.WorkStatus_WORK_STATUS_UNSPECIFIED {
		workStatus = managev1.WorkStatus_WORK_STATUS_DRAFT
	}
	creditType := managev1.MyCreditedWorkCreditType_MY_CREDITED_WORK_CREDIT_TYPE_ARTIST
	if credited.CreditType == CreditTypeMember {
		creditType = managev1.MyCreditedWorkCreditType_MY_CREDITED_WORK_CREDIT_TYPE_MEMBER
	}
	return &managev1.MyCreditedWork{
		WorkId:               credited.WorkID,
		SourceLocale:         credited.SourceLocale,
		Title:                credited.Title,
		Slug:                 credited.Slug,
		Type:                 workType,
		Status:               workStatus,
		CreditId:             credited.CreditID,
		CreditRole:           credited.CreditRole,
		CreatedAt:            timestamppb.New(credited.CreatedAt),
		CreditType:           creditType,
		CreditedAs:           credited.CreditedAs,
		CreditedAsImageAsset: mediaasset.ReferenceProto(credited.Image),
	}
}

func (h *ConnectHandler) ListMyCreditedWorks(ctx context.Context, req *connect.Request[managev1.ListMyCreditedWorksRequest]) (*connect.Response[managev1.ListMyCreditedWorksResponse], error) {
	listed, err := h.works.ListMyCreditedWorks(ctx, ListMyCreditedWorksQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts})
	return render(listed, err, func(listed CreditedWorkPage) *managev1.ListMyCreditedWorksResponse {
		works := make([]*managev1.MyCreditedWork, 0, len(listed.Works))
		for _, credited := range listed.Works {
			works = append(works, creditedWorkMessage(credited))
		}
		return &managev1.ListMyCreditedWorksResponse{Works: works, Pagination: paginationMessage(listed.Pagination)}
	})
}

// VersionsMessage renders a page of version checkpoints.
func VersionsMessage(versions VersionPage) *managev1.ListWorkVersionsResponse {
	messages := make([]*managev1.WorkVersion, len(versions.Versions))
	for i, version := range versions.Versions {
		contributors := make([]*managev1.VersionContributor, 0, len(version.Contributors))
		for _, contributor := range version.Contributors {
			contributors = append(contributors, &managev1.VersionContributor{MemberId: contributor.MemberID, Nickname: contributor.Nickname})
		}
		messages[i] = &managev1.WorkVersion{
			Id:            version.ID,
			Version:       version.Version,
			SourceLocale:  version.SourceLocale,
			Title:         version.Title,
			Summary:       version.Summary,
			CanonicalHash: version.CanonicalHash,
			CreatedAt:     timestamppb.New(version.CreatedAt),
			Contributors:  contributors,
		}
	}
	return &managev1.ListWorkVersionsResponse{Versions: messages, Pagination: paginationMessage(versions.Pagination)}
}

// WorkMessage renders a managed Work without its clients.
func WorkMessage(ctx context.Context, view View) *managev1.Work { return workMessage(ctx, view) }

func (h *ConnectHandler) ListWorkVersions(ctx context.Context, req *connect.Request[managev1.ListWorkVersionsRequest]) (*connect.Response[managev1.ListWorkVersionsResponse], error) {
	versions, err := h.works.ListWorkVersions(ctx, ListWorkVersionsQuery{WorkID: req.Msg.WorkId, Page: page(req.Msg.GetPagination())})
	return render(versions, err, VersionsMessage)
}

func (h *ConnectHandler) RestoreWorkVersion(ctx context.Context, req *connect.Request[managev1.RestoreWorkVersionRequest]) (*connect.Response[managev1.Work], error) {
	restored, err := h.works.RestoreWorkVersion(ctx, RestoreWorkVersionCommand{WorkID: req.Msg.WorkId, VersionID: req.Msg.VersionId})
	return render(restored, err, func(view View) *managev1.Work { return workMessage(ctx, view) })
}

func (h *ConnectHandler) CheckWorkSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckWorkSlugAvailableRequest]) (*connect.Response[managev1.CheckWorkSlugAvailableResponse], error) {
	available, err := h.works.CheckWorkSlugAvailable(ctx, CheckWorkSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeWorkID: req.Msg.ExcludeWorkId})
	return render(available, err, func(available bool) *managev1.CheckWorkSlugAvailableResponse {
		return &managev1.CheckWorkSlugAvailableResponse{Available: available}
	})
}

// InternalConnectHandler serves the Work collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalWorkServiceHandler
	works *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of works.
func NewInternalConnectHandler(works *Service) *InternalConnectHandler {
	return &InternalConnectHandler{works: works}
}

func roomMetadataMessage(metadata RoomMetadata) *intrav1.WorkLocaleMetadata {
	return &intrav1.WorkLocaleMetadata{Locale: metadata.Locale, Title: metadata.Title, Summary: metadata.Summary}
}

func (h *InternalConnectHandler) LoadWorkBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadWorkBlockDocumentRequest]) (*connect.Response[intrav1.LoadWorkBlockDocumentResponse], error) {
	room, err := h.works.LoadWorkBlockDocument(ctx, LoadWorkBlockDocumentQuery{
		WorkID: req.Msg.WorkId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	})
	return render(room, err, func(room RoomDocument) *intrav1.LoadWorkBlockDocumentResponse {
		response := &intrav1.LoadWorkBlockDocumentResponse{
			Document:            room.Document,
			DocumentRevision:    room.DocumentRevision,
			SourceMetadata:      roomMetadataMessage(room.SourceMetadata),
			BlockMedia:          fileref.BlockMediaItemsProto(room.BlockMedia),
			Locale:              room.Locale,
			LocaleExists:        room.LocaleExists,
			TargetRevision:      room.TargetRevision,
			PresentLocaleValues: room.PresentLocaleValues,
		}
		if room.LocaleMetadata != nil {
			response.LocaleMetadata = roomMetadataMessage(*room.LocaleMetadata)
		}
		return response
	})
}

func (h *InternalConnectHandler) ApplyWorkBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyWorkBlockBatchRequest]) (*connect.Response[intrav1.ApplyWorkBlockBatchResponse], error) {
	msg := req.Msg
	saved, err := h.works.ApplyWorkBlockBatch(ctx, ApplyWorkBlockBatchCommand{
		WorkID: msg.WorkId, Locale: msg.Locale, Batch: msg.Batch,
		AffectedLocaleValues: msg.AffectedLocaleValues, ExpectedTargetRevision: msg.ExpectedTargetRevision,
	})
	return render(saved, err, func(saved RoomSave) *intrav1.ApplyWorkBlockBatchResponse {
		return &intrav1.ApplyWorkBlockBatchResponse{
			DocumentRevision: saved.Content.DocumentRevision.String(), Changed: saved.Content.Changed,
			SourceChanged:  saved.Content.TranslationSourceChanged,
			ChangedLocales: saved.Content.ChangedLocales,
			Locale:         saved.Locale, TargetRevision: saved.TargetRevision,
		}
	})
}

func (h *InternalConnectHandler) UpdateWorkLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateWorkLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateWorkLocaleMetadataResponse], error) {
	msg := req.Msg
	command := UpdateWorkLocaleMetadataCommand{
		WorkID: msg.WorkId, Locale: msg.Locale, Title: msg.Title,
		ExpectedRevision: msg.ExpectedRevision, ExpectedTargetRevision: msg.ExpectedTargetRevision,
		ContributorMemberIDs: msg.ContributorMemberIds,
	}
	switch summary := msg.SummaryUpdate.(type) {
	case *intrav1.UpdateWorkLocaleMetadataRequest_Summary:
		command.SetSummary, command.Summary = true, &summary.Summary
	case *intrav1.UpdateWorkLocaleMetadataRequest_ClearSummary:
		command.SetSummary = true
	}
	saved, err := h.works.UpdateWorkLocaleMetadata(ctx, command)
	return render(saved, err, func(saved RoomSave) *intrav1.UpdateWorkLocaleMetadataResponse {
		return &intrav1.UpdateWorkLocaleMetadataResponse{
			DocumentRevision: saved.Content.DocumentRevision.String(), Changed: saved.Content.Changed,
			SourceChanged: saved.Locale == saved.SourceLocale && saved.Content.TranslationSourceChanged, ChangedLocales: []string{saved.Locale},
			Locale: saved.Locale, TargetRevision: saved.TargetRevision,
		}
	})
}

func (h *InternalConnectHandler) CreateWorkVersionCheckpoint(ctx context.Context, req *connect.Request[intrav1.CreateWorkVersionCheckpointRequest]) (*connect.Response[intrav1.CreateWorkVersionCheckpointResponse], error) {
	msg := req.Msg
	checkpoint, err := h.works.CreateWorkVersionCheckpoint(ctx, CreateWorkVersionCheckpointCommand{
		WorkID: msg.WorkId, Locale: msg.Locale, ExpectedRevision: msg.ExpectedRevision, ContributorMemberIDs: msg.ContributorMemberIds,
	})
	return render(checkpoint, err, func(checkpoint VersionCheckpoint) *intrav1.CreateWorkVersionCheckpointResponse {
		response := &intrav1.CreateWorkVersionCheckpointResponse{
			Created:  checkpoint.Created != nil,
			Revision: checkpoint.Revision,
			Locale:   checkpoint.Locale,
		}
		if checkpoint.Created != nil {
			response.VersionId = &checkpoint.Created.ID
		}
		return response
	})
}
