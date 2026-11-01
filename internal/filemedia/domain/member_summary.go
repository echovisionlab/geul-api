package domain

import "github.com/echovisionlab/geul-api/internal/mediaasset"

type MemberSummary struct {
	ID, Nickname string
	Avatar       *mediaasset.Reference
	Deleted      bool
}
