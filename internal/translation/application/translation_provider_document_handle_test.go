package application

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTranslationJobRPCProjectionDoesNotExposeProviderDocumentHandle(t *testing.T) {
	documentID := "provider-document-id"
	documentKey := "provider-document-secret-key"
	submittedAt := time.Unix(1_700_001_000, 0).UTC()
	job := Job{
		EntityType:                  "menu",
		ProviderDocumentID:          &documentID,
		ProviderDocumentKey:         &documentKey,
		ProviderDocumentSubmittedAt: &submittedAt,
	}

	encoded, err := json.Marshal(JobMessage(job))
	require.NoError(t, err)
	require.NotContains(t, string(encoded), documentID)
	require.NotContains(t, string(encoded), documentKey)
}
