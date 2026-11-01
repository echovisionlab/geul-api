package account

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNewServiceRejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	_, err := NewService(Deps{})
	require.EqualError(t, err, "account dependencies are required: Audit, DB, Identity, MemberDeletion, MemberEmails, Newsletter, Publisher, SpiceDB")

	_, err = NewService(Deps{
		DB: &gorm.DB{}, SpiceDB: &auth.SpiceDBClient{}, Identity: unusedIdentity{}, Publisher: unusedPublisher{},
		Newsletter: unusedNewsletter{}, MemberDeletion: unusedMemberDeletion{}, MemberEmails: unusedMemberEmails{},
		Audit: &audittest.Recorder{},
	})
	require.NoError(t, err)
}
