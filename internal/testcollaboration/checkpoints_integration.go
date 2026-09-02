//go:build integration

package testcollaboration

import (
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	"gorm.io/gorm"
)

func NewCheckpoints(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
) *collaboration.CheckpointFence {
	return collaborationadapter.NewCheckpointFence(db, spiceDB)
}
