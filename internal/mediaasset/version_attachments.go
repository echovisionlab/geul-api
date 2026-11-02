package mediaasset

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type AttachmentKind string

const (
	AttachmentFile  AttachmentKind = "file"
	AttachmentImage AttachmentKind = "image"
	AttachmentAudio AttachmentKind = "audio"
	AttachmentVideo AttachmentKind = "video"
)

func UnavailableVersionAttachmentKinds(ctx context.Context, store AttachmentStore, fileIDs map[uuid.UUID]struct{}) (map[uuid.UUID]AttachmentKind, error) {
	if store == nil {
		return nil, fmt.Errorf("version attachment transaction is required")
	}
	if len(fileIDs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(fileIDs))
	for fileID := range fileIDs {
		ids = append(ids, fileID.String())
	}
	rows, err := store.LockVersionFiles(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load Version attachment Files: %w", err)
	}

	available := make(map[uuid.UUID]struct{}, len(rows))
	unavailable := make(map[uuid.UUID]AttachmentKind)
	for _, row := range rows {
		fileID, err := uuid.Parse(row.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid Version attachment File ID %q: %w", row.ID, err)
		}
		available[fileID] = struct{}{}
		if row.DeleteRequestedAt != nil {
			unavailable[fileID] = missingAttachmentKindForMIME(row.MIMEType)
		}
	}
	for fileID := range fileIDs {
		if _, exists := available[fileID]; !exists {
			unavailable[fileID] = AttachmentFile
		}
	}
	return unavailable, nil
}

func missingAttachmentKindForMIME(mimeType string) AttachmentKind {
	normalized := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch {
	case strings.HasPrefix(normalized, "image/"):
		return AttachmentImage
	case strings.HasPrefix(normalized, "audio/"):
		return AttachmentAudio
	case strings.HasPrefix(normalized, "video/"):
		return AttachmentVideo
	default:
		return AttachmentFile
	}
}
