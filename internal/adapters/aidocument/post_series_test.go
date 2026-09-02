package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
	"github.com/stretchr/testify/require"
)

func TestNewPostSeriesRegistrationBindsExactDomainAndRequiresDependencies(t *testing.T) {
	port := &seriesdomain.AIDocumentService{}
	registration, err := NewPostSeriesRegistration(port)
	require.NoError(t, err)
	require.Equal(t, core.DomainPostSeries, registration.Domain)
	require.Same(t, port, registration.Port)

	_, err = NewPostSeriesRegistration(nil)
	require.Error(t, err)
}
