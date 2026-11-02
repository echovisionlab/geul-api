package page_test

import (
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
)

var _ pagepostgres.Runtime = (*pageadapter.Runtime)(nil)
