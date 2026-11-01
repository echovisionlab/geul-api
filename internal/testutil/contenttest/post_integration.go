//go:build integration

package contenttest

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	"github.com/stretchr/testify/require"

	"gorm.io/gorm"
)

type postIntegrationFileReuseAuthorizer struct{}

func (postIntegrationFileReuseAuthorizer) AuthorizeFileReuse(
	context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock,
	contentblock.FileReference, contentblock.File,
) error {
	return nil
}

func NewPostContentBlockStore(t *testing.T) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(postIntegrationFileReuseAuthorizer{})
	require.NoError(t, err)
	return store
}

func EmptyPostDocument(locale string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            locale,
		Base:                    &contentv1.RichTextBlockGraph{},
		LocaleOverlays:          []*contentv1.RichTextLocaleOverlay{{Locale: locale}},
	}
}
