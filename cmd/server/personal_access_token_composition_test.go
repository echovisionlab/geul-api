package main

import (
	"bytes"
	"testing"

	memberpatadapter "github.com/echovisionlab/geul-api/internal/adapters/memberpat"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPersonalAccessTokenCompositionConstructsOneSharedService(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	repository, err := memberpatadapter.NewPersonalAccessTokenRepository(db)
	require.NoError(t, err)

	composition, err := newPersonalAccessTokenComposition(
		managev1connect.UnimplementedAccountServiceHandler{},
		repository,
		memberpatadapter.SystemClock{},
		bytes.NewReader(make([]byte, 96)),
	)

	require.NoError(t, err)
	require.NotNil(t, composition.tokens)
	require.NotNil(t, composition.accountHandler)
}

func TestPersonalAccessTokenCompositionFailsClosedBeforePartialRegistration(t *testing.T) {
	composition, err := newPersonalAccessTokenComposition(
		managev1connect.UnimplementedAccountServiceHandler{},
		nil,
		memberpatadapter.SystemClock{},
		bytes.NewReader(make([]byte, 96)),
	)

	require.Error(t, err)
	require.Nil(t, composition.tokens)
	require.Nil(t, composition.accountHandler)
}
