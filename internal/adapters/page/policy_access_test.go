package page_test

import (
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	"github.com/echovisionlab/geul-api/internal/filemedia"
)

var _ filemedia.PagePolicyAccess = (*pageadapter.PolicyAccess)(nil)
