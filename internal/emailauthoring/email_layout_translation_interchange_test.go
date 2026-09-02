package emailauthoring

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProjectEmailLayoutInterchangeTargetPreservesAbsentAndExplicitEmpty(t *testing.T) {
	documentRevision := uuid.NewString()
	absent, err := projectEmailLayoutInterchangeTarget(documentRevision, nil)
	require.NoError(t, err)
	require.False(t, absent.Exists)
	require.Empty(t, absent.Revision)
	require.Empty(t, absent.Values)

	source, err := email.CanonicalizeLayoutSourceMarkers(
		`<main title="Source title"><p>Source body</p>{{content}}</main>`,
	)
	require.NoError(t, err)
	units, err := email.ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.NotEmpty(t, units)
	explicitEmpty := map[string]string{units[0].Handle: ""}
	overlay, _, err := email.ApplyLayoutLocaleValues(source, explicitEmpty)
	require.NoError(t, err)
	updatedAt := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	present, err := projectEmailLayoutInterchangeTarget(documentRevision, &email.LayoutTranslationEntry{
		LayoutTranslationDocument: email.LayoutTranslationDocument{
			ContentHTML: overlay, UpdatedAt: updatedAt,
		},
	})
	require.NoError(t, err)
	require.True(t, present.Exists)
	require.NotEmpty(t, present.Revision)
	require.Equal(t, explicitEmpty, present.Values)
}
