package work

import (
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
)

func TestCreditedWorkPreservesWorkSourceLocaleForMemberAndArtistCredits(t *testing.T) {
	memberID := "member-1"
	for _, owner := range []*string{nil, &memberID} {
		response, err := new(Service).projectCreditedWork(t.Context(), creditedWorkRow{
			WorkID: "work-1", SourceLocale: "pt-BR", MemberID: owner,
		}, memberID, &commonv1.MemberSummary{Nickname: "Member"})
		require.NoError(t, err)
		require.Equal(t, "pt-BR", response.SourceLocale)
	}
}
