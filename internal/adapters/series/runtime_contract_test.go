package series_test

import (
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
)

var _ seriesdomain.Media = (*seriesadapter.Runtime)(nil)
var _ seriespostgres.Media = (*seriesadapter.Runtime)(nil)
var _ seriesdomain.Reads = (*seriesadapter.Runtime)(nil)
var _ seriesdomain.Members = (*seriesadapter.MemberSummaries)(nil)
var _ seriespostgres.PostAccess = seriesadapter.PostAccess{}
