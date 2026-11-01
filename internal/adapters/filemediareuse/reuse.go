package filemediareuse

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"gorm.io/gorm"
)

type Authorizer struct {
	checker authz.AuthorizationDecisionChecker
}

func New(checker authz.AuthorizationDecisionChecker) *Authorizer {
	return &Authorizer{checker: checker}
}
func (a *Authorizer) AuthorizeFileReuse(ctx context.Context, tx *gorm.DB, document contentblock.Document, _ contentblock.FullBlock, _ contentblock.FileReference, file contentblock.File) error {
	return filemedia.AuthorizeContentBlockFileReuse(ctx, filemediapostgres.Bind(tx, filemediapostgres.Ports{}, nil), a.checker, document.ID.String(), file.ID.String())
}
