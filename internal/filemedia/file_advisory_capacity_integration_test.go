//go:build integration

package filemedia

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPrepareClientMediaUsesReservedAdvisoryConnectionIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	service := withTestStorage(&Service{db: db})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = service.PrepareClientMediaUpload(ctx, PrepareClientMediaUploadCommand{UploadID: uuid.NewString(), FileID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err), "session lookup must use the reserved connection instead of waiting for a second pool connection: %v", err)
}
