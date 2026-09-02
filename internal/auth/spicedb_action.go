package auth

import (
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ResourceAction is one generated domain method such as Post.Edit or
// Page.Manage. It carries no engine key until the owning generated constructor
// validates and binds an exact resource ID.
type ResourceAction func(string) (policyv1.Can, error)
