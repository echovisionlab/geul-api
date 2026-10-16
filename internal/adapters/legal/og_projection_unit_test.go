package legal

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestProjectionRejectsNonCanonicalLegalRoute(t *testing.T) {
	t.Parallel()
	locale := "en"
	err := NewProjection().ReleasePending(t.Context(), &gorm.DB{}, og.Target{
		EntityType: "privacy", EntityID: uuid.NewString(), Locale: &locale, Kind: "locale",
	}, "")
	require.ErrorContains(t, err, "canonical route identity")
}
