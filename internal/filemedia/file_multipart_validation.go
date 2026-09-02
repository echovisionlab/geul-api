package filemedia

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func multipartSessionChunkSize(session model.UploadSession) int64 {
	if session.ChunkSize > 0 {
		return int64(session.ChunkSize)
	}
	return int64(chunkSize)
}

func expectedMultipartPartSize(fileSize int64, chunkSize int64, partNumber int32) int64 {
	if fileSize <= 0 || chunkSize <= 0 || partNumber <= 0 {
		return 0
	}
	start := int64(partNumber-1) * chunkSize
	if start >= fileSize {
		return 0
	}
	end := min(start+chunkSize, fileSize)
	return end - start
}

func validateMultipartPartContentLength(session model.UploadSession, partNumber int32, contentLength int64) error {
	if contentLength <= 0 {
		return errs.InvalidArgument("content_length", "upload part content length is required")
	}

	expectedSize := expectedMultipartPartSize(session.FileSize, multipartSessionChunkSize(session), partNumber)
	if expectedSize <= 0 {
		return errs.InvalidArgument("part_number", "upload part is outside the expected multipart range")
	}
	if contentLength != expectedSize {
		return errs.InvalidArgument(
			"content_length",
			fmt.Sprintf("upload part size %d does not match expected %d", contentLength, expectedSize),
		)
	}

	return nil
}

func validateMultipartCompletionParts(session model.UploadSession, parts []model.UploadPart) error {
	if session.TotalParts <= 0 || int32(len(parts)) != session.TotalParts {
		return errs.FailedPrecondition("uploaded part count does not match expected multipart count")
	}

	chunkSize := multipartSessionChunkSize(session)
	for i, part := range parts {
		expectedPartNumber := int32(i + 1)
		if part.PartNumber != expectedPartNumber {
			return errs.FailedPrecondition("uploaded part numbers do not form the required multipart sequence")
		}
		if strings.TrimSpace(part.ETag) == "" {
			return errs.FailedPrecondition("uploaded part is missing its object-store identity")
		}
		expectedSize := expectedMultipartPartSize(session.FileSize, chunkSize, part.PartNumber)
		if expectedSize <= 0 || part.Size != expectedSize {
			return errs.FailedPrecondition(
				fmt.Sprintf("uploaded part %d size does not match expected multipart size", part.PartNumber),
			)
		}
	}

	return nil
}

func readMultipartSniffPrefix(reader io.Reader, contentLength int64) ([]byte, io.Reader, error) {
	if contentLength <= 0 {
		return nil, reader, nil
	}

	prefixSize := min(int64(multipartSniffBytes), contentLength)
	if prefixSize <= 0 {
		return nil, reader, nil
	}

	prefix := make([]byte, prefixSize)
	n, err := io.ReadFull(reader, prefix)
	switch err {
	case nil:
		prefix = prefix[:n]
	case io.EOF, io.ErrUnexpectedEOF:
		prefix = prefix[:n]
	default:
		return nil, nil, err
	}

	return prefix, io.MultiReader(bytes.NewReader(prefix), reader), nil
}

func readMultipartUploadBody(reader io.Reader, contentLength int64) ([]byte, error) {
	if contentLength < 0 {
		return nil, errs.InvalidArgument("content_length", "upload part content length is required")
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	if int64(len(body)) != contentLength {
		return nil, io.ErrUnexpectedEOF
	}

	return body, nil
}

func toCompletedParts(parts []model.UploadPart) []types.CompletedPart {
	completed := make([]types.CompletedPart, len(parts))
	for i, part := range parts {
		completed[i] = types.CompletedPart{
			PartNumber: aws.Int32(part.PartNumber),
			ETag:       aws.String(part.ETag),
		}
	}
	return completed
}

func uploadPartInfos(parts []model.UploadPart) []*managev1.UploadPartInfo {
	partInfos := make([]*managev1.UploadPartInfo, 0, len(parts))
	for _, part := range parts {
		partInfos = append(partInfos, &managev1.UploadPartInfo{
			PartNumber: part.PartNumber,
			Etag:       part.ETag,
		})
	}
	return partInfos
}

func uploadPartModelNumbers(parts []model.UploadPart) []int32 {
	partNumbers := make([]int32, 0, len(parts))
	for _, part := range parts {
		partNumbers = append(partNumbers, part.PartNumber)
	}
	return partNumbers
}

func normalizeMimeType(mimeType string) string {
	if mimeType == "" {
		return ""
	}
	base := strings.Split(mimeType, ";")[0]
	return strings.ToLower(strings.TrimSpace(base))
}

func buildAllowedMimeSet(allowed []string) map[string]struct{} {
	set := make(map[string]struct{}, len(allowed))
	for _, mime := range allowed {
		normalized := normalizeMimeType(mime)
		if normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	return set
}

func formatAllowedExtensions(allowed []string) string {
	seen := make(map[string]struct{}, len(allowed))
	extensions := make([]string, 0, len(allowed))

	for _, mimeType := range allowed {
		normalized := normalizeMimeType(mimeType)
		if normalized == "" {
			continue
		}

		ext := strings.ToUpper(model.GetExtensionFromMime(normalized))
		if ext == "BIN" {
			parts := strings.Split(normalized, "/")
			if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
				ext = strings.ToUpper(parts[1])
			}
		}
		if ext == "" {
			continue
		}
		if _, ok := seen[ext]; ok {
			continue
		}
		seen[ext] = struct{}{}
		extensions = append(extensions, ext)
	}

	return strings.Join(extensions, ", ")
}

func unsupportedMimeMessage(actual string, allowed []string) string {
	actualLabel := normalizeMimeType(actual)
	prefix := "Unsupported file type."
	if actualLabel != "" {
		prefix = fmt.Sprintf("Unsupported file type (%s).", actualLabel)
	}

	allowedFormats := formatAllowedExtensions(allowed)
	if allowedFormats == "" {
		return prefix
	}
	return fmt.Sprintf("%s Supported formats: %s.", prefix, allowedFormats)
}

func undeterminedMimeMessage(allowed []string) string {
	allowedFormats := formatAllowedExtensions(allowed)
	if allowedFormats == "" {
		return "Could not determine the file type."
	}
	return fmt.Sprintf("Could not determine the file type. Supported formats: %s.", allowedFormats)
}

func mimeMismatchMessage(declared, detected string, allowed []string) string {
	parts := make([]string, 0, 2)
	if normalizedDeclared := normalizeMimeType(declared); normalizedDeclared != "" {
		parts = append(parts, "declared "+normalizedDeclared)
	}
	if normalizedDetected := normalizeMimeType(detected); normalizedDetected != "" {
		parts = append(parts, "detected "+normalizedDetected)
	}

	prefix := "Uploaded file type does not match this field."
	if len(parts) > 0 {
		prefix = fmt.Sprintf("%s (%s).", strings.TrimSuffix(prefix, "."), strings.Join(parts, ", "))
	}

	allowedFormats := formatAllowedExtensions(allowed)
	if allowedFormats == "" {
		return prefix
	}
	return fmt.Sprintf("%s Supported formats: %s.", prefix, allowedFormats)
}

var canonicalMimeAliases = map[string]string{
	"video/x-quicktime":            "video/quicktime",
	"video/avi":                    "video/x-msvideo",
	"video/x-avi":                  "video/x-msvideo",
	"video/msvideo":                "video/x-msvideo",
	"application/x-matroska":       "video/x-matroska",
	"video/matroska":               "video/x-matroska",
	"video/mkv":                    "video/x-matroska",
	"video/x-mkv":                  "video/x-matroska",
	"audio/x-wav":                  "audio/wav",
	"audio/wave":                   "audio/wav",
	"audio/x-aac":                  "audio/aac",
	"audio/m4a":                    "audio/mp4",
	"audio/x-m4a":                  "audio/mp4",
	"audio/mp4a-latm":              "audio/mp4",
	"application/ogg":              "audio/ogg",
	"application/x-zip-compressed": "application/zip",
	"image/jpg":                    "image/jpeg",
	"image/x-png":                  "image/png",
	"image/vnd.microsoft.icon":     "image/x-icon",
	"audio/mp3":                    "audio/mpeg",
	"audio/x-flac":                 "audio/flac",
}

func canonicalizeMimeType(mimeType string, allowedSet map[string]struct{}) string {
	normalized := normalizeMimeType(mimeType)
	if normalized == "" {
		return ""
	}
	if normalized == "audio/x-aiff" && mimeTypeAllowed("audio/aiff", allowedSet) {
		return "audio/aiff"
	}
	if mimeTypeAllowed(normalized, allowedSet) {
		return normalized
	}
	alias, ok := canonicalMimeAliases[normalized]
	if ok && mimeTypeAllowed(alias, allowedSet) {
		return alias
	}
	return normalized
}

func mimeTypeAllowed(mimeType string, allowedSet map[string]struct{}) bool {
	_, ok := allowedSet[mimeType]
	return ok
}

func isMimeAllowed(mimeType string, allowedSet map[string]struct{}) (bool, string) {
	canonical := canonicalizeMimeType(mimeType, allowedSet)
	if canonical == "" {
		return false, canonical
	}
	_, ok := allowedSet[canonical]
	return ok, canonical
}

func validateMultipartCompletionMime(
	requestedMime string,
	detectedMime string,
	allowed []string,
) (string, error) {
	allowedSet := buildAllowedMimeSet(allowed)
	requestAllowed, canonicalReq := isMimeAllowed(requestedMime, allowedSet)
	detectedAllowed, canonicalDetected := isMimeAllowed(detectedMime, allowedSet)
	if !requestAllowed || !detectedAllowed {
		return "", errs.InvalidArgument("mime_type", mimeMismatchMessage(requestedMime, detectedMime, allowed))
	}

	if canonicalReq == canonicalDetected {
		return canonicalDetected, nil
	}

	return "", errs.InvalidArgument("mime_type", mimeMismatchMessage(requestedMime, canonicalDetected, allowed))
}

func detectCanonicalMime(body []byte, allowedSet map[string]struct{}) string {
	detectedRaw := http.DetectContentType(body)
	detectedBase := normalizeMimeType(detectedRaw)

	if len(body) >= 4 && bytes.Equal(body[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return detectMatroskaMime(body, allowedSet)
	}

	if detectsAllowedSVG(body, detectedBase, allowedSet) {
		return "image/svg+xml"
	}

	if aiffMime := detectAIFFMime(body, allowedSet); aiffMime != "" {
		return aiffMime
	}
	if flacMime := detectFLACMime(body, allowedSet); flacMime != "" {
		return flacMime
	}
	if aacMime := detectAACMime(body, allowedSet); aacMime != "" {
		return aacMime
	}

	if glbMime := detectGLBMime(body, allowedSet); glbMime != "" {
		return glbMime
	}

	if detectedBase == "application/octet-stream" ||
		detectedBase == "video/mp4" ||
		detectedBase == "audio/mp4" ||
		detectedBase == "application/mp4" {
		if isoMime := detectISOBaseMediaMime(body, allowedSet); isoMime != "" {
			return isoMime
		}
	}

	if detectedBase == "application/octet-stream" {
		if mpegMime := detectMPEGAudioMime(body, allowedSet); mpegMime != "" {
			return mpegMime
		}
		if riffMime := detectRIFFMime(body, allowedSet); riffMime != "" {
			return riffMime
		}
	}

	return canonicalizeMimeType(detectedRaw, allowedSet)
}

func detectsAllowedSVG(body []byte, detectedBase string, allowedSet map[string]struct{}) bool {
	if _, ok := allowedSet["image/svg+xml"]; !ok {
		return false
	}
	if detectedBase != "text/xml" && detectedBase != "application/xml" && detectedBase != "text/plain" {
		return false
	}
	sample := body
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	return bytes.Contains(bytes.ToLower(sample), []byte("<svg"))
}
