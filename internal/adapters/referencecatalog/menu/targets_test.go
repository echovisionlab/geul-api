package referencecatalogmenuadapter

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

func TestTargetsIgnoresEmptyChangesBeforeDatabaseAccess(t *testing.T) {
	targets := NewTargets(nil)
	require.NoError(t, targets.UpdateSlug(t.Context(), nil, referencecatalog.MenuTargetSlugChange{}))
	require.NoError(t, targets.Remove(t.Context(), nil, referencecatalog.MenuTarget{}))
}
