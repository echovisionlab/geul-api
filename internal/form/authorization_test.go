package form

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingFormPermissionChecker struct {
	calls []policyv1.Can
	err   error
}

func (checker *recordingFormPermissionChecker) Check(
	_ context.Context,
	can policyv1.Can,
) error {
	checker.calls = append(checker.calls, can)
	return checker.err
}

func TestRequireFormActionPerformsExactlyOneCatalogCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action formObjectAction
		can    func(string) (policyv1.Can, error)
	}{
		{name: "view", action: formActionView, can: policyv1.Form.View},
		{name: "edit", action: formActionEdit, can: policyv1.Form.Edit},
		{name: "delete", action: formActionDelete, can: policyv1.Form.Delete},
		{name: "manage", action: formActionManage, can: policyv1.Form.Manage},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			checker := &recordingFormPermissionChecker{}
			require.NoError(t, requireFormAction(t.Context(), checker, "form-id", test.action))
			expected, err := test.can("form-id")
			require.NoError(t, err)
			require.Equal(t, []policyv1.Can{expected}, checker.calls)
		})
	}
}

func TestRequireFormActionRejectsUnknownActionWithoutAuthorizationCall(t *testing.T) {
	t.Parallel()

	checker := &recordingFormPermissionChecker{}
	err := requireFormAction(t.Context(), checker, "form-id", formObjectAction(255))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	assert.Empty(t, checker.calls)
}

func TestRequireFormActionPropagatesExactCheckFailure(t *testing.T) {
	t.Parallel()

	want := connect.NewError(connect.CodePermissionDenied, assert.AnError)
	checker := &recordingFormPermissionChecker{err: want}
	err := requireFormAction(t.Context(), checker, "form-id", formActionEdit)
	require.ErrorIs(t, err, want)
	expected, canErr := policyv1.Form.Edit("form-id")
	require.NoError(t, canErr)
	require.Equal(t, []policyv1.Can{expected}, checker.calls)
}

var _ formPermissionChecker = (*recordingFormPermissionChecker)(nil)
