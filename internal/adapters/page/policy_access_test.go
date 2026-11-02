package page_test

import (
	"github.com/echovisionlab/geul-api/internal/adapters/filemediapolicy"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
)

var _ filemediapolicy.LockedPolicyAccess = (*pageadapter.PolicyAccess)(nil)
