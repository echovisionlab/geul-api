package filemedia

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

var ErrMediaURLSigningFailed = errors.New("media URL signing failed")

func BuildSignedMediaFileURL(
	mediaDomain, fileID string,
	extension string,
	secret string,
	ttl time.Duration,
	purpose mediaauth.Purpose,
) (string, error) {
	return buildSignedMediaFileURL(mediaDomain, fileID, extension, secret, ttl, purpose, "")
}

func BuildSignedMediaDownloadURL(
	mediaDomain, fileID string,
	extension string,
	secret string,
	ttl time.Duration,
	filename string,
) (string, error) {
	return buildSignedMediaFileURL(
		mediaDomain,
		fileID,
		extension,
		secret,
		ttl,
		mediaauth.PurposeDownload,
		filename,
	)
}

func buildSignedMediaFileURL(
	mediaDomain, fileID string,
	extension string,
	secret string,
	ttl time.Duration,
	purpose mediaauth.Purpose,
	filename string,
) (string, error) {
	if secret == "" {
		return "", ErrMediaURLSigningFailed
	}

	extension = strings.ToLower(strings.TrimSpace(extension))
	objectKey, err := mediaauth.MediaObjectKey(fileID, extension)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMediaURLSigningFailed, err)
	}

	token, err := generateMediaToken(purpose, mediaauth.ScopeExact, objectKey, ttl, secret, filename)
	if err != nil {
		return "", err
	}
	signedPath, err := mediaauth.SignedMediaPath(token, fileID, extension)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMediaURLSigningFailed, err)
	}

	return joinOriginPath(mediaDomain, signedPath), nil
}

func BuildPublicMediaHLSURL(mediaDomain, fileID, generationID, objectName string) (string, error) {
	// The persisted media generation contract fixes the public entry point to its
	// manifest. HLS segments resolve relative to that immutable generation URL.
	if objectName != "master.m3u8" {
		return "", ErrMediaURLSigningFailed
	}
	prefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMediaURLSigningFailed, err)
	}
	return joinOriginPath(mediaDomain, "/"+prefix+"/"+objectName), nil
}

func generateMediaToken(
	purpose mediaauth.Purpose,
	scopeType mediaauth.ScopeType,
	scopeValue string,
	ttl time.Duration,
	secret string,
	filename string,
) (string, error) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if maxTTL, ok := mediaTokenMaxTTL(purpose); ok && ttl > maxTTL {
		ttl = maxTTL
	}
	now := time.Now().UTC()
	token, err := mediaauth.GenerateToken(mediaauth.Claims{
		Version:      mediaauth.TokenVersion,
		Purpose:      purpose,
		ScopeType:    scopeType,
		ScopeValue:   scopeValue,
		IssuedAtUnix: now.Unix(),
		ExpiryUnix:   now.Add(ttl).Unix(),
		Methods:      []mediaauth.Method{mediaauth.MethodGet, mediaauth.MethodHead},
		Filename:     filename,
	}, secret)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMediaURLSigningFailed, err)
	}
	return token, nil
}

func mediaTokenMaxTTL(purpose mediaauth.Purpose) (time.Duration, bool) {
	switch purpose {
	case mediaauth.PurposeInline:
		return mediaauth.InlineTTL, true
	case mediaauth.PurposeDownload:
		return mediaauth.DownloadTTL, true
	default:
		return 0, false
	}
}

func mediaExtension(mimeType *string) string {
	if mimeType == nil || *mimeType == "" {
		return "bin"
	}
	return model.GetExtensionFromMime(*mimeType)
}

func joinOriginPath(origin, resourcePath string) string {
	if origin == "" {
		return resourcePath
	}

	trimmedDomain := strings.TrimRight(origin, "/")
	if strings.HasPrefix(trimmedDomain, "http://") || strings.HasPrefix(trimmedDomain, "https://") {
		return trimmedDomain + resourcePath
	}

	return "https://" + trimmedDomain + resourcePath
}
