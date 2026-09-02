package work_test

import (
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
)

var _ workdomain.Runtime = (*workadapter.Runtime)(nil)
var _ workpublic.MediaResolver = (*workadapter.Runtime)(nil)
