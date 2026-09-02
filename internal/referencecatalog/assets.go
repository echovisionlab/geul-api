package referencecatalog

import (
	"context"

	"gorm.io/gorm"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type AssetOwner struct {
	Type string
	ID   string
}

type AssetBinding struct {
	SourceFileID string
	Owner        AssetOwner
	Key          string
	Kind         string
}

type AssetRelease struct {
	Owner         AssetOwner
	BindingPrefix string
}

type AssetSource struct {
	FileID        string
	Kind          string
	FallbackKinds []string
}

// Assets provides the file-owned operations required by reference catalogs.
type Assets interface {
	LockForAttachment(context.Context, *gorm.DB, []string) error
	BindReady(context.Context, *gorm.DB, AssetBinding) (*commonv1.AssetRef, error)
	Release(context.Context, *gorm.DB, AssetRelease) error
	ReadyRef(context.Context, *gorm.DB, AssetSource) (*commonv1.AssetRef, error)
}
