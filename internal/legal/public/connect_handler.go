package public

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalTimestamp(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func localizationInfo(selection *publiccontent.Selection) *openv1.LocalizationInfo {
	if selection == nil {
		return nil
	}
	return publiccontent.ToProtoLocalizationInfo(*selection)
}

// assetMessage renders a ready public asset.
func assetMessage(asset *Asset) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{
		AssetId: asset.ID, Url: asset.URL, Extension: asset.Extension, MimeType: asset.MIMEType,
		FileSize: asset.FileSize, Sha256: asset.SHA256, Disposition: assetDispositionMessage(asset.Disposition),
		DownloadFilename: asset.DownloadFilename,
	}
}

func assetDispositionMessage(disposition AssetDisposition) commonv1.AssetDisposition {
	switch disposition {
	case DispositionInline:
		return commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE
	case DispositionAttachment:
		return commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT
	default:
		return commonv1.AssetDisposition_ASSET_DISPOSITION_UNSPECIFIED
	}
}

func getPolicyQuery(id, shareToken, sharePassword *string, header interface{ Get(string) string }) GetPolicyQuery {
	return GetPolicyQuery{
		ID: optionalStringValue(id), ShareToken: optionalStringValue(shareToken),
		SharePassword: optionalStringValue(sharePassword), AcceptLanguage: header.Get("Accept-Language"),
		Now: time.Now(),
	}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// TermsConnectHandler serves the public Terms RPCs.
type TermsConnectHandler struct {
	policies *Service
}

// NewTermsConnectHandler returns the public Terms adapter of policies.
func NewTermsConnectHandler(policies *Service) *TermsConnectHandler {
	return &TermsConnectHandler{policies: policies}
}

func termsMessage(policy *PublicPolicy) *openv1.Terms {
	if policy == nil {
		return nil
	}
	return &openv1.Terms{
		Id: policy.ID, Version: policy.Version, Title: policy.Title,
		Document: policy.Document, Revision: policy.Revision,
		EffectiveFrom: optionalTimestamp(policy.EffectiveFrom), EffectiveUntil: optionalTimestamp(policy.EffectiveUntil),
		LocalizationInfo: localizationInfo(policy.Localization), OgAsset: assetMessage(policy.OGAsset),
	}
}

// Get returns the currently active terms of service, or a specific version by ID.
func (h *TermsConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetTermsRequest]) (*connect.Response[openv1.GetTermsResponse], error) {
	result, err := h.policies.GetPolicy(ctx, legaldomain.Terms, getPolicyQuery(req.Msg.Id, req.Msg.ShareToken, req.Msg.SharePassword, req.Header()))
	return respond(&openv1.GetTermsResponse{Terms: termsMessage(result.Current), Scheduled: termsMessage(result.Scheduled)}, err)
}

// List returns archived terms of service.
func (h *TermsConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListTermsRequest]) (*connect.Response[openv1.ListTermsResponse], error) {
	page, err := h.policies.ListArchivedPolicies(ctx, legaldomain.Terms, ListArchivedPoliciesQuery{Limit: req.Msg.Limit, Offset: req.Msg.Offset})
	items := make([]*openv1.TermsSummary, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, &openv1.TermsSummary{
			Id: item.ID, Version: item.Version, Title: item.Title,
			EffectiveFrom: optionalTimestamp(item.EffectiveFrom), EffectiveUntil: optionalTimestamp(item.EffectiveUntil),
		})
	}
	return respond(&openv1.ListTermsResponse{Items: items, Total: page.Total}, err)
}

// PrivacyConnectHandler serves the public Privacy RPCs.
type PrivacyConnectHandler struct {
	policies *Service
}

// NewPrivacyConnectHandler returns the public Privacy adapter of policies.
func NewPrivacyConnectHandler(policies *Service) *PrivacyConnectHandler {
	return &PrivacyConnectHandler{policies: policies}
}

func privacyMessage(policy *PublicPolicy) *openv1.Privacy {
	if policy == nil {
		return nil
	}
	return &openv1.Privacy{
		Id: policy.ID, Version: policy.Version, Title: policy.Title,
		Document: policy.Document, Revision: policy.Revision,
		EffectiveFrom: optionalTimestamp(policy.EffectiveFrom), EffectiveUntil: optionalTimestamp(policy.EffectiveUntil),
		LocalizationInfo: localizationInfo(policy.Localization), OgAsset: assetMessage(policy.OGAsset),
	}
}

// Get returns the currently active privacy policy, or a specific version by ID.
func (h *PrivacyConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetPrivacyRequest]) (*connect.Response[openv1.GetPrivacyResponse], error) {
	result, err := h.policies.GetPolicy(ctx, legaldomain.Privacy, getPolicyQuery(req.Msg.Id, req.Msg.ShareToken, req.Msg.SharePassword, req.Header()))
	return respond(&openv1.GetPrivacyResponse{Privacy: privacyMessage(result.Current), Scheduled: privacyMessage(result.Scheduled)}, err)
}

// List returns archived privacy policies.
func (h *PrivacyConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListPrivacyRequest]) (*connect.Response[openv1.ListPrivacyResponse], error) {
	page, err := h.policies.ListArchivedPolicies(ctx, legaldomain.Privacy, ListArchivedPoliciesQuery{Limit: req.Msg.Limit, Offset: req.Msg.Offset})
	items := make([]*openv1.PrivacySummary, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, &openv1.PrivacySummary{
			Id: item.ID, Version: item.Version, Title: item.Title,
			EffectiveFrom: optionalTimestamp(item.EffectiveFrom), EffectiveUntil: optionalTimestamp(item.EffectiveUntil),
		})
	}
	return respond(&openv1.ListPrivacyResponse{Items: items, Total: page.Total}, err)
}
