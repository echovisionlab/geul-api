package audience

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type memberReferenceResult struct {
	eligible []string
	err      error
}

func (r memberReferenceResult) EligibleIDs(
	context.Context,
	*gorm.DB,
	[]string,
) ([]string, error) {
	return r.eligible, r.err
}

func TestReplaceSegmentRelationsRejectsIneligibleExcludedMember(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)

	err = replaceSegmentRelations(
		db,
		memberReferenceResult{},
		uuid.NewString(),
		model.AudienceSegmentConfig{ExcludeMemberIDs: []string{uuid.NewString()}},
	)
	require.ErrorContains(
		t,
		err,
		"every excluded member must be an onboarded Member with an exact Identity link",
	)
}
