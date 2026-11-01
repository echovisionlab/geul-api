package page

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPreparePageAccessPolicyPreservesRequestPresence(t *testing.T) {
	service := &Service{}
	absent, err := service.preparePageUpdate(context.Background(), UpdatePageCommand{})
	require.NoError(t, err)
	require.Nil(t, absent.accessPolicy)
	require.Nil(t, absent.change().AccessPolicy)
	reset, err := service.preparePageUpdate(context.Background(), UpdatePageCommand{AccessPolicy: &pageaccess.Policy{}})
	require.NoError(t, err)
	require.Equal(t, pageaccess.ModePublic, reset.accessPolicy.Mode)
	require.NotNil(t, reset.change().AccessPolicy)
	_, err = service.preparePageUpdate(context.Background(), UpdatePageCommand{AccessPolicy: &pageaccess.Policy{Mode: 999}})
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	_, err = service.preparePageUpdate(context.Background(), UpdatePageCommand{AccessPolicy: &pageaccess.Policy{Mode: pageaccess.ModeConditions, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_USER}}})
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
}

func TestManagePageAccessPolicyProjection(t *testing.T) {
	policy := &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, NewsletterSubscriber: true, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL}
	encoded, err := pageaccess.Encode(pageaccess.FromMessage(policy))
	require.NoError(t, err)
	view := View{Page: Page{AccessPolicy: encoded, DocumentLayout: DefaultDocumentLayout()}}
	page, err := PageMessage(Detail{View: view})
	require.NoError(t, err)
	require.True(t, proto.Equal(policy, page.AccessPolicy))
	list, err := PageListMessage(PageList{Pages: []View{view}})
	require.NoError(t, err)
	require.True(t, proto.Equal(policy, list.Pages[0].AccessPolicy))
	updated, err := UpdateResponseMessage(UpdateResult{Page: view.Page})
	require.NoError(t, err)
	require.True(t, proto.Equal(policy, updated.AccessPolicy))
}

func TestPageAccessPolicyUpdateNamesTheSetting(t *testing.T) {
	require.Equal(t, []string{"accessPolicy"}, updatedSettingFields(UpdatePageCommand{ID: "page-1", AccessPolicy: &pageaccess.Policy{}}))
	slug, shown := "", true
	require.Equal(t, []string{"slug", "showTitle"}, updatedSettingFields(UpdatePageCommand{ID: "page-1", Slug: &slug, ShowTitle: &shown}))
}
