package page

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// The stored types of the File derivatives a Block's media presentation
// needs (managev1.FileDerivativeType names).
const (
	derivativeHLS         = "FILE_DERIVATIVE_TYPE_HLS"
	derivativeThumbnail   = "FILE_DERIVATIVE_TYPE_THUMBNAIL"
	derivativeSpectrogram = "FILE_DERIVATIVE_TYPE_SPECTROGRAM"
	derivativeWaveform    = "FILE_DERIVATIVE_TYPE_WAVEFORM"
)

// requireAttachmentsReadyForPublication rejects publication while a Block
// attachment's File is missing, pending deletion or lacks the presentation
// its media type needs.
func requireAttachmentsReadyForPublication(
	ctx context.Context,
	store Store,
	attachments []contentblock.PublicationAttachment,
) error {
	fileIDs, err := publicationFileIDs(attachments)
	if err != nil || len(fileIDs) == 0 {
		return err
	}
	files, err := store.PublicationFiles(ctx, fileIDs)
	if err != nil {
		return err
	}
	if len(files) != len(fileIDs) {
		return domainerr.Precondition("Page content Block media File is unavailable")
	}
	presentations, err := store.PublicationPresentations(ctx, fileIDs)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := requirePresentation(presentations, file); err != nil {
			return err
		}
	}
	return nil
}

// publicationFileIDs returns the sorted distinct Files the attachments
// reference; a missing attachment rejects publication.
func publicationFileIDs(attachments []contentblock.PublicationAttachment) ([]string, error) {
	fileIDs := make([]string, 0, len(attachments))
	seen := make(map[string]struct{}, len(attachments))
	for _, attachment := range attachments {
		if attachment.FileID == uuid.Nil || attachment.MissingMediaKind != "" {
			return nil, domainerr.Precondition("Page content Block has a missing media attachment")
		}
		fileID := attachment.FileID.String()
		if _, exists := seen[fileID]; !exists {
			seen[fileID] = struct{}{}
			fileIDs = append(fileIDs, fileID)
		}
	}
	sort.Strings(fileIDs)
	return fileIDs, nil
}

// requirePresentation rejects a File whose presentation for its media type
// is not ready.
func requirePresentation(presentations Presentations, file PublicationFile) error {
	if file.DeleteRequested {
		return domainerr.Precondition("Page content Block media File is pending deletion")
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.SplitN(file.MIMEType, ";", 2)[0]))
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		if !presentations.SourceAssets[PresentationKey{FileID: file.ID, Kind: "image"}] {
			return domainerr.Precondition("Page content Block image presentation is not ready")
		}
	case mimeType == "model/gltf-binary":
		if !presentations.SourceAssets[PresentationKey{FileID: file.ID, Kind: "mesh"}] {
			return domainerr.Precondition("Page content Block mesh presentation is not ready")
		}
	case strings.HasPrefix(mimeType, "video/"):
		if file.DurationSeconds == nil {
			return domainerr.Precondition("Page content Block video presentation is not ready")
		}
		return requireDerivatives(presentations, file.ID, derivativeHLS, derivativeThumbnail)
	case strings.HasPrefix(mimeType, "audio/"):
		if file.DurationSeconds == nil {
			return domainerr.Precondition("Page content Block audio presentation is not ready")
		}
		return requireDerivatives(presentations, file.ID, derivativeHLS, derivativeSpectrogram, derivativeWaveform)
	}
	return nil
}

func requireDerivatives(presentations Presentations, fileID string, derivativeTypes ...string) error {
	for _, derivativeType := range derivativeTypes {
		if !presentations.Derivatives[PresentationKey{FileID: fileID, Kind: derivativeType}] {
			return domainerr.Precondition("Page content Block media presentation is not ready")
		}
	}
	return nil
}
