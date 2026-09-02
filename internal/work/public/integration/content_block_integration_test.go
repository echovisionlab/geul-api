//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPublicWorkContentBlockStore(t *testing.T) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(
		allowPublicWorkFileReuse{},
	)
	require.NoError(t, err)
	return store
}

type allowPublicWorkFileReuse struct{}

func (allowPublicWorkFileReuse) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func emptyPublicWorkDocument(locale string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
		SourceLocale:            locale,
		Base:                    &contentv1.RichTextBlockGraph{},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: locale,
		}},
	}
}
