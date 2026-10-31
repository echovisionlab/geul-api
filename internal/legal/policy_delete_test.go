package legal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type policyDeletionOG struct {
	OG
	current   *CurrentRoute
	released  bool
	requested string
	err       error
}

func (og *policyDeletionOG) CurrentForRoute(context.Context, *gorm.DB, string) (*CurrentRoute, error) {
	return og.current, og.err
}
func (*policyDeletionOG) RouteID(string) string { return "public-route" }
func (og *policyDeletionOG) CancelAndRelease(_ context.Context, _ *gorm.DB, kind, id string) error {
	og.released = id == "public-route"
	return nil
}
func (og *policyDeletionOG) RequestSaved(_ context.Context, _ *gorm.DB, kind, id, locale string, all bool, reason string) error {
	og.requested = id
	return nil
}

func TestPolicyDeletionRefreshesOnlyTheAffectedPublicRoute(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		before := &CurrentRoute{ID: "deleted"}
		og := &policyDeletionOG{current: &CurrentRoute{ID: "fallback"}}
		require.NoError(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", before))
		require.Equal(t, "fallback", og.requested)
		require.False(t, og.released)
		og = &policyDeletionOG{}
		require.NoError(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", before))
		require.True(t, og.released)
		og = &policyDeletionOG{err: errors.New("lookup failed")}
		require.Error(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", before))
		require.False(t, og.released)
		require.NoError(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", &CurrentRoute{ID: "other"}))
	}
}
