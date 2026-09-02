package public

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// AssetReader resolves public assets without exposing file lifecycle mutation.
type AssetReader interface {
	ReadyRef(context.Context, *gorm.DB, referencecatalog.AssetSource) *commonv1.AssetRef
}
