package authentication

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNewAuthBootstrapServiceRejectsMissingRequiredDependencies(t *testing.T) {
	db := &gorm.DB{}
	spicedb := &auth.SpiceDBClient{}

	require.Panics(t, func() {
		NewAuthBootstrapService(nil, spicedb, nil, nil)
	})
	require.Panics(t, func() {
		NewAuthBootstrapService(db, nil, nil, nil)
	})
}
