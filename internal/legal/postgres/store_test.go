package legalpostgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
)

// The Legal statuses are the strings the history tables hold, which the
// model constants name for code that has not moved.
func TestLegalStatusesAreTheStoredValues(t *testing.T) {
	for stored, status := range map[model.LegalPolicyStatus]legal.Status{
		model.TermsStatusDraft: legal.TermsDraft, model.TermsStatusScheduled: legal.TermsScheduled,
		model.TermsStatusActive: legal.TermsActive, model.TermsStatusArchived: legal.TermsArchived,
		model.PrivacyStatusDraft: legal.PrivacyDraft, model.PrivacyStatusScheduled: legal.PrivacyScheduled,
		model.PrivacyStatusActive: legal.PrivacyActive, model.PrivacyStatusArchived: legal.PrivacyArchived,
	} {
		require.EqualValues(t, stored, status)
	}
}

func TestNewUnitsNamesEveryMissingPort(t *testing.T) {
	_, err := NewUnits(nil, Ports{})
	require.EqualError(t, err, "legal store database is required")
}

func TestCollaborationRoomsNameTheirKind(t *testing.T) {
	require.Equal(t, "COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY", collaborationResourceType(legal.Terms).String())
	require.Equal(t, "COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY", collaborationResourceType(legal.Privacy).String())
}
