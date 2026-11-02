package public

import (
	"context"
	"testing"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/programevent"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestProgramEventTimestampProtoHandlesNilAndValues(t *testing.T) {
	require.Nil(t, timestampProto(nil))

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	require.True(t, timestamppb.New(now).AsTime().Equal(timestampProto(&now).AsTime()))
}

func TestProgramEventRequestValidationDoesNotRequireDatabaseRows(t *testing.T) {
	_, err := NewConnectHandler(&Service{}).Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = NewSeriesConnectHandler(&Service{}).Get(context.Background(), connect.NewRequest(&openv1.GetProgramEventSeriesRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

}

func TestProgramEventSeriesCopyIsLocaleNeutral(t *testing.T) {
	summary := "One summary"
	series := programevent.Series{ID: "series-1", Title: "One title", Summary: &summary, Slug: "one-title"}
	korean := seriesEntry(t.Context(), unusedAssets{}, series)
	german := seriesEntry(t.Context(), unusedAssets{}, series)

	require.Equal(t, korean.Title, german.Title)
	require.Equal(t, korean.Summary, german.Summary)
}
