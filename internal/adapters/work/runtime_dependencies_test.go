package work_test

import (
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	workpublicpostgres "github.com/echovisionlab/geul-api/internal/work/public/postgres"
)

var _ workpostgres.Runtime = (*workadapter.Runtime)(nil)
var _ workpublic.MediaResolver = (*workadapter.Runtime)(nil)
var _ workpublicpostgres.ReadyAssets = (*workadapter.Runtime)(nil)
