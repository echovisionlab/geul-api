package artist

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ArtistService implements the ArtistService Connect handler
func (s *ArtistService) SetArtistImage(
	ctx context.Context,
	req *connect.Request[managev1.SetArtistImageRequest],
) (_ *connect.Response[managev1.SetArtistImageResponse], terminalErr error) {
	defer func() {
		if terminalErr != nil {
			level := slog.LevelWarn
			switch connect.CodeOf(terminalErr) {
			case connect.CodeUnknown, connect.CodeInternal, connect.CodeDataLoss, connect.CodeUnavailable, connect.CodeUnimplemented, connect.CodeDeadlineExceeded:
				level = slog.LevelError
			}
			slog.Log(ctx, level, "Operation failed", "domain", "artist", "operation", "SetArtistImage", failurecause.Attr(terminalErr), "code", connect.CodeOf(terminalErr).String())
		}
	}()

	fileIDs, err := validateOrderedArtistFileIDs([]string{req.Msg.FileId})
	if err != nil {
		return nil, err
	}
	images, ogRunID, err := s.replaceArtistImages(ctx, req.Msg.ArtistId, fileIDs, nil)
	if err != nil {
		return nil, err
	}
	var imageAsset *commonv1.AssetRef
	if len(images) > 0 {
		imageAsset = images[0].Asset
	}
	return connect.NewResponse(&managev1.SetArtistImageResponse{
		ImageAsset: imageAsset, OgGenerationRunId: ogRunID,
	}), nil
}

// DeleteArtistImage removes the artist's profile image (admin or manager)
func (s *ArtistService) DeleteArtistImage(
	ctx context.Context,
	req *connect.Request[managev1.DeleteArtistImageRequest],
) (_ *connect.Response[managev1.OgAssetDeleteResponse], terminalErr error) {
	defer func() {
		if terminalErr != nil {
			level := slog.LevelWarn
			switch connect.CodeOf(terminalErr) {
			case connect.CodeUnknown, connect.CodeInternal, connect.CodeDataLoss, connect.CodeUnavailable, connect.CodeUnimplemented, connect.CodeDeadlineExceeded:
				level = slog.LevelError
			}
			slog.Log(ctx, level, "Operation failed", "domain", "artist", "operation", "DeleteArtistImage", failurecause.Attr(terminalErr), "code", connect.CodeOf(terminalErr).String())
		}
	}()

	_, ogRunID, err := s.replaceArtistImages(ctx, req.Msg.ArtistId, nil, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.OgAssetDeleteResponse{
		Success: true, OgGenerationRunId: ogRunID,
	}), nil
}

// ==================== Owner and Manager Assignment ====================
