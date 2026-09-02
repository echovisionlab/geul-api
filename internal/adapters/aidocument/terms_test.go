package aidocumentadapter

import (
	"testing"

	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/legal"
)

func TestTermsRegistrationUsesLegalPolicyPort(t *testing.T) {
	registration, err := NewTermsRegistration(&legal.AIDocumentService{})
	require.NoError(t, err)
	require.Equal(t, core.DomainTerms, registration.Domain)
	port, ok := registration.Port.(*legalAIDocumentPort)
	require.True(t, ok)
	require.Equal(t, "terms", port.entityType)
	require.Equal(t, core.DomainTerms, port.domain)

	_, err = NewTermsRegistration(nil)
	require.Error(t, err)
}
