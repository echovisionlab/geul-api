package mediaauth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ErrURLSigningFailed wraps every reason a media delivery URL cannot be built.
var ErrURLSigningFailed = errors.New("media URL signing failed")

// SignFileURL signs an exact media file URL and returns the expiry its token
// enforces, which callers advertise instead of recomputing now+ttl. The TTL
// falls back to one minute and is capped at the purpose's lifetime; filename
// is the download filename claim, empty for inline delivery.
func SignFileURL(
	mediaDomain, fileID string,
	extension string,
	secret string,
	ttl time.Duration,
	purpose Purpose,
	filename string,
) (string, time.Time, error) {
	if secret == "" {
		return "", time.Time{}, ErrURLSigningFailed
	}
	extension = strings.ToLower(strings.TrimSpace(extension))
	objectKey, err := MediaObjectKey(fileID, extension)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %w", ErrURLSigningFailed, err)
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	if limit, ok := maxTTL(purpose); ok && ttl > limit {
		ttl = limit
	}
	now := time.Now().UTC()
	expiryUnix := now.Add(ttl).Unix()
	token, err := GenerateToken(Claims{
		Version:      TokenVersion,
		Purpose:      purpose,
		ScopeType:    ScopeExact,
		ScopeValue:   objectKey,
		IssuedAtUnix: now.Unix(),
		ExpiryUnix:   expiryUnix,
		Methods:      []Method{MethodGet, MethodHead},
		Filename:     filename,
	}, secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %w", ErrURLSigningFailed, err)
	}
	// The token was issued for this exact object key, so the path is valid.
	signedPath, _ := SignedMediaPath(token, fileID, extension)
	return JoinOrigin(mediaDomain, signedPath), time.Unix(expiryUnix, 0).UTC(), nil
}

// PublicHLSURL returns the unsigned entry point of a ready media generation.
func PublicHLSURL(mediaDomain, fileID, generationID, objectName string) (string, error) {
	// The persisted media generation contract fixes the public entry point to its
	// manifest. HLS segments resolve relative to that immutable generation URL.
	if objectName != "master.m3u8" {
		return "", ErrURLSigningFailed
	}
	prefix, err := MediaHLSObjectPrefix(fileID, generationID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrURLSigningFailed, err)
	}
	return JoinOrigin(mediaDomain, "/"+prefix+"/"+objectName), nil
}

// ExpiringFileRef signs one file for public delivery. A download carries the
// canonical download filename both in its token and in the reference.
func ExpiringFileRef(
	mediaDomain string,
	secret string,
	fileID string,
	extension string,
	mimeType string,
	fileName *string,
	purpose Purpose,
	ttl time.Duration,
) (*commonv1.ExpiringMediaRef, error) {
	ref, err := ExpiringReference(mediaDomain, secret, fileID, extension, mimeType, fileName, purpose, ttl)
	if err != nil {
		return nil, err
	}
	return &commonv1.ExpiringMediaRef{FileId: ref.FileID, Url: ref.URL, Purpose: DeliveryPurpose(ref.Purpose), ExpiresAt: timestamppb.New(ref.ExpiresAt), Extension: ref.Extension, MimeType: ref.MimeType, FileName: ref.FileName}, nil
}

// Reference is a signed media URL and the exact expiry enforced by its token.
type Reference struct {
	FileID, URL, Extension, MimeType string
	FileName                         *string
	Purpose                          Purpose
	ExpiresAt                        time.Time
}

func (r *Reference) Clone() *Reference {
	if r == nil {
		return nil
	}
	result := *r
	if r.FileName != nil {
		v := *r.FileName
		result.FileName = &v
	}
	return &result
}
func ExpiringReference(mediaDomain, secret, fileID, extension, mimeType string, fileName *string, purpose Purpose, ttl time.Duration) (*Reference, error) {
	var downloadFilename string
	if purpose == PurposeDownload {
		downloadFilename = CanonicalDownloadFilename(fileName, fileID, extension)
	}
	url, expiresAt, err := SignFileURL(mediaDomain, fileID, extension, secret, ttl, purpose, downloadFilename)
	if err != nil {
		return nil, err
	}

	ref := &Reference{FileID: fileID, URL: url, Purpose: purpose, ExpiresAt: expiresAt, Extension: extension, MimeType: mimeType, FileName: fileName}
	if purpose == PurposeDownload {
		ref.FileName = &downloadFilename
	}
	return ref, nil
}

// DeliveryPurpose maps a token purpose to its wire value.
func DeliveryPurpose(purpose Purpose) commonv1.MediaDeliveryPurpose {
	switch purpose {
	case PurposeInline:
		return commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE
	case PurposeDownload:
		return commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD
	default:
		return commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_UNSPECIFIED
	}
}

// ValidDownloadFilename reports whether value may appear as a token's
// download filename claim.
func ValidDownloadFilename(value string) bool {
	return validFilename(value)
}

// CanonicalDownloadFilename returns a token-safe filename for both new and
// historical file rows. Historical invalid values are never echoed into signed
// token claims; they receive a deterministic file-ID fallback instead.
func CanonicalDownloadFilename(fileName *string, fileID string, extension string) string {
	extension = strings.ToLower(strings.TrimSpace(extension))
	if fileName != nil {
		normalized := norm.NFC.String(strings.TrimSpace(*fileName))
		if normalized != "" && validFilename(normalized) {
			if extension == "" || strings.EqualFold(FilenameExtension(normalized), extension) {
				return normalized
			}
			return normalized + "." + extension
		}
	}
	if extension != "" {
		return "download-" + strings.TrimSpace(fileID) + "." + extension
	}
	return "download-" + strings.TrimSpace(fileID)
}

// FilenameExtension returns the text after the last dot of value, or "" when
// value has none or ends with a dot.
func FilenameExtension(value string) string {
	index := strings.LastIndexByte(value, '.')
	if index < 0 || index == len(value)-1 {
		return ""
	}
	return value[index+1:]
}

// JoinOrigin prefixes a delivery path with its origin, defaulting to HTTPS
// when origin has no scheme; an empty origin leaves the path relative.
func JoinOrigin(origin, resourcePath string) string {
	if origin == "" {
		return resourcePath
	}
	trimmed := strings.TrimRight(origin, "/")
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed + resourcePath
	}
	return "https://" + trimmed + resourcePath
}
