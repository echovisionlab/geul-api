package public

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNewFileServiceRequiresDatabase(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(t, "public file service dependencies are required", func() {
		NewFileService(nil, &auth.SpiceDBClient{}, "https://cdn.example.com", "https://media.example.com", "unit-public-media-secret", 30*time.Minute)
	})
}

func TestNewFileServiceRequiresSpiceDBClient(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(t, "public file service dependencies are required", func() {
		NewFileService(&gorm.DB{}, nil, "https://cdn.example.com", "https://media.example.com", "unit-public-media-secret", 30*time.Minute)
	})
}
