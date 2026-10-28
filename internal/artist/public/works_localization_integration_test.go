//go:build integration

package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	artistlabelpublictest "github.com/echovisionlab/geul-api/internal/testutil/artistlabelpublic"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestArtistPublicWorksUseRequestedWorkTranslationIntegration(t *testing.T) {
	fixture := artistlabelpublictest.New(t)
	koreanTitle := "공개 아티스트 레이블 작업 " + fixture.Suffix
	koreanSummary := "공개 아티스트 레이블 작업 요약"
	now := time.Now().UTC()
	require.NoError(t, fixture.DB.Exec(`
		INSERT INTO work_translation (entity_id, locale, title, summary, created_at, updated_at)
		VALUES (?::uuid, 'ko', ?, ?, ?, ?)
	`, fixture.Work.Id, koreanTitle, koreanSummary, now, now).Error)

	getWorks := func(acceptLanguage string) *openv1.ArtistWork {
		t.Helper()
		request := connect.NewRequest(&openv1.GetArtistWorksRequest{ArtistId: fixture.MainArtist.Id, Limit: 10})
		request.Header().Set("Accept-Language", acceptLanguage)
		response, err := fixture.ArtistService.GetWorks(context.Background(), request)
		require.NoError(t, err)
		require.Len(t, response.Msg.Works, 1)
		require.Equal(t, fixture.Work.Id, response.Msg.Works[0].Id)
		return response.Msg.Works[0]
	}

	korean := getWorks("ko")
	require.Equal(t, koreanTitle, korean.GetTitle())
	require.Equal(t, koreanSummary, korean.GetSummary())

	source := getWorks("en")
	require.Equal(t, fixture.Work.GetTitle(), source.GetTitle())
	require.Equal(t, fixture.WorkSummary, source.GetSummary())
}
