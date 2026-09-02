package series_test

import (
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
)

var _ seriesdomain.MediaRuntime = (*seriesadapter.Runtime)(nil)
var _ seriesdomain.OGRefresh = (*seriesadapter.Runtime)(nil)
var _ seriesdomain.SeriesReadProjection = (*seriesadapter.Runtime)(nil)
