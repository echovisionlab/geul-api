package programevent

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"github.com/google/uuid"
)

// contentBlockFileRenderResolver renders a Block File reference through the
// Store of the read that materializes it.
type contentBlockFileRenderResolver struct {
	store Store
}

func (r contentBlockFileRenderResolver) ResolveContentBlockFile(
	ctx context.Context,
	selector contentblock.FileRenderSelector,
) (contentblock.FileRenderTarget, error) {
	if r.store == nil || selector.BlockID == uuid.Nil || selector.ReferencePath == "" || selector.FileID == uuid.Nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("invalid Program Event Content Block File render selector")
	}
	mimeType, found, err := r.store.ContentFileMIMEType(ctx, selector.BlockID, selector.ReferencePath, selector.FileID)
	if err != nil {
		return contentblock.FileRenderTarget{}, fmt.Errorf("load exact Program Event Content Block File: %w", err)
	}
	if !found {
		return contentblock.FileRenderTarget{}, fmt.Errorf("exact Program Event Content Block File reference does not exist")
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return contentblock.FileRenderTarget{MIMEType: mimeType}, nil
	}
	ref, err := r.store.InlineImageAsset(ctx, selector.FileID.String())
	if err != nil {
		return contentblock.FileRenderTarget{}, err
	}
	return contentblock.FileRenderTarget{URL: optional.Value(ref).URL, MIMEType: optional.Value(ref).MimeType}, nil
}
