package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/stretchr/testify/require"
)

func TestCreditedWorkPreservesWorkSourceLocaleForMemberAndArtistCredits(t *testing.T) {
	memberID := "member-1"
	for _, owner := range []*string{nil, &memberID} {
		response, err := new(Service).projectCreditedWork(t.Context(), CreditedRow{
			WorkID: "work-1", SourceLocale: "pt-BR", MemberID: owner,
		}, memberID, &memberref.Summary{Nickname: "Member"})
		require.NoError(t, err)
		require.Equal(t, "pt-BR", response.SourceLocale)
	}
}
