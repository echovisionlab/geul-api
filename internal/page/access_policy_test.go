package page

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPreparePageAccessPolicyPreservesRequestPresence(t *testing.T) {
	service := &PageService{}
	absent, err := service.preparePageUpdate(context.Background(), &managev1.UpdatePageRequest{})
	require.NoError(t, err)
	require.Nil(t, absent.accessPolicy)
	require.NotContains(t, absent.fields, "access_policy")
	reset, err := service.preparePageUpdate(context.Background(), &managev1.UpdatePageRequest{AccessPolicy: &commonv1.PageAccessPolicy{}})
	require.NoError(t, err)
	require.Equal(t, commonv1.PageAccessMode_PAGE_ACCESS_MODE_PUBLIC, reset.accessPolicy.Mode)
	require.Contains(t, reset.fields, "access_policy")
	_, err = service.preparePageUpdate(context.Background(), &managev1.UpdatePageRequest{AccessPolicy: &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode(999)}})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = service.preparePageUpdate(context.Background(), &managev1.UpdatePageRequest{AccessPolicy: &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_USER}}})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestManagePageAccessPolicyProjection(t *testing.T) {
	policy := &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, NewsletterSubscriber: true, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL}
	encoded, err := pageaccess.Encode(policy)
	require.NoError(t, err)
	service := &PageService{}
	page, err := service.toProtoPage(&model.Page{AccessPolicy: encoded, DocumentLayout: model.DefaultDocumentLayout()}, nil, nil)
	require.NoError(t, err)
	require.True(t, proto.Equal(policy, page.AccessPolicy))
	summary, err := service.toProtoPageSummary(&model.Page{AccessPolicy: encoded})
	require.NoError(t, err)
	require.True(t, proto.Equal(policy, summary.AccessPolicy))
}

func TestPageAccessPolicyUpdateEvent(t *testing.T) {
	event := buildManagePageContentUpdatedEvent(&managev1.UpdatePageRequest{Id: "page-1", AccessPolicy: &commonv1.PageAccessPolicy{}})
	require.NotNil(t, event)
	require.Len(t, event.ChangedFields, 1)
	require.Equal(t, "settings.access_policy", event.ChangedFields[0].Path)
	require.Equal(t, managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION, event.ChangedFields[0].Kind)
}
