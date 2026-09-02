package menu

import (
	"context"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// SiteSettingsReferences clears Site Settings slots that select a deleted Menu.
type SiteSettingsReferences interface {
	ClearMenuReferences(context.Context, *gorm.DB, string) error
}

// TargetReference identifies an owning-domain resource selected by a Menu
// item. Menu owns the link policy; adapters own concrete persistence lookup.
type TargetReference struct {
	LinkType managev1.MenuLinkType
	ID       string
	Slug     string
}

// TargetReferences validates and locks concrete target rows before a Menu tree
// becomes authoritative.
type TargetReferences interface {
	ValidateAndLock(context.Context, *gorm.DB, []TargetReference) error
}
