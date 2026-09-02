package page_test

import (
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
)

var _ pagedomain.Runtime = (*pageadapter.Runtime)(nil)
