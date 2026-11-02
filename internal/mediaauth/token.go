package mediaauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	TokenVersion = 1
	ClockSkew    = 30 * time.Second

	InlineTTL   = 15 * time.Minute
	DownloadTTL = 15 * time.Minute

	maxTokenLength         = 4096
	maxHLSObjectNameLength = 255
	maxAssetFilenameLength = 100
	maxFilenameLength      = 255
)

type Purpose string

const (
	PurposeInline   Purpose = "inline"
	PurposeDownload Purpose = "download"
)

type ScopeType string

const (
	ScopeExact  ScopeType = "exact"
	ScopePrefix ScopeType = "prefix"
)

type Method string

const (
	MethodGet  Method = "GET"
	MethodHead Method = "HEAD"
)

type PathKind string

const (
	PathAsset       PathKind = "asset"
	PathMediaObject PathKind = "media_object"
	PathMediaHLS    PathKind = "media_hls"
)

type DeliveryPath struct {
	Kind         PathKind
	Token        string
	AssetID      string
	Filename     string
	FileID       string
	GenerationID string
	Extension    string
	ObjectName   string
	ObjectKey    string
}

var (
	ErrInvalidToken     = errors.New("invalid media token")
	ErrTokenExpired     = errors.New("expired media token")
	ErrTokenNotYetValid = errors.New("media token is not yet valid")
	ErrInvalidPath      = errors.New("invalid media path")

	uuidPattern          = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	extensionPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9]{0,15}$`)
	assetFilenamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	hlsObjectPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	tokenPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
)

type Claims struct {
	Version      int       `json:"v"`
	Purpose      Purpose   `json:"p"`
	ScopeType    ScopeType `json:"st"`
	ScopeValue   string    `json:"sv"`
	IssuedAtUnix int64     `json:"iat"`
	ExpiryUnix   int64     `json:"exp"`
	Methods      []Method  `json:"m"`
	Filename     string    `json:"fn,omitempty"`
}

func NewClaims(purpose Purpose, scopeType ScopeType, scopeValue string, issuedAt time.Time, filename string) (Claims, error) {
	ttl, ok := maxTTL(purpose)
	if !ok {
		return Claims{}, ErrInvalidToken
	}

	claims := Claims{
		Version:      TokenVersion,
		Purpose:      purpose,
		ScopeType:    scopeType,
		ScopeValue:   scopeValue,
		IssuedAtUnix: issuedAt.UTC().Unix(),
		ExpiryUnix:   issuedAt.UTC().Add(ttl).Unix(),
		Methods:      []Method{MethodGet, MethodHead},
		Filename:     filename,
	}
	if err := validateClaims(&claims); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

func GenerateToken(claims Claims, secret string) (string, error) {
	if secret == "" {
		return "", ErrInvalidToken
	}
	if err := validateClaims(&claims); err != nil {
		return "", err
	}

	// Claims contains only JSON primitives, so marshaling cannot fail.
	payload, _ := json.Marshal(claims)
	payloadEncoded := base64.RawURLEncoding.EncodeToString(payload)
	return payloadEncoded + "." + computeSignature(payloadEncoded, secret), nil
}

func ValidateToken(token, secret string) (*Claims, error) {
	return validateTokenAt(token, secret, time.Now())
}

func validateTokenAt(token, secret string, now time.Time) (*Claims, error) {
	if secret == "" || !validTokenWire(token) {
		return nil, ErrInvalidToken
	}
	payloadEncoded, signature, _ := strings.Cut(token, ".")

	expectedSignature := computeSignature(payloadEncoded, secret)
	if !hmac.Equal([]byte(signature), []byte(expectedSignature)) {
		return nil, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadEncoded)
	if err != nil {
		return nil, ErrInvalidToken
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims Claims
	if err := decoder.Decode(&claims); err != nil {
		return nil, ErrInvalidToken
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidToken
	}
	if err := validateClaims(&claims); err != nil {
		return nil, ErrInvalidToken
	}
	canonicalPayload, _ := json.Marshal(claims)
	if !bytes.Equal(payload, canonicalPayload) {
		return nil, ErrInvalidToken
	}

	now = now.UTC()
	issuedAt := time.Unix(claims.IssuedAtUnix, 0).UTC()
	expiresAt := time.Unix(claims.ExpiryUnix, 0).UTC()
	if now.Before(issuedAt.Add(-ClockSkew)) {
		return nil, ErrTokenNotYetValid
	}
	if now.After(expiresAt.Add(ClockSkew)) {
		return nil, ErrTokenExpired
	}
	return &claims, nil
}

func (c *Claims) allowsPath(requestedPath string) bool {
	path, err := canonicalObjectPath(requestedPath)
	if err != nil {
		return false
	}
	switch c.ScopeType {
	case ScopeExact:
		return path == c.ScopeValue
	default:
		return false
	}
}

func (c *Claims) allowsMethod(method string) bool {
	for _, allowed := range c.Methods {
		if string(allowed) == method {
			return true
		}
	}
	return false
}

func (c *Claims) AllowsRequest(method, requestedPath string) bool {
	return c != nil && c.allowsMethod(method) && c.allowsPath(requestedPath)
}

func AssetObjectKey(assetID, extension string) (string, error) {
	if err := validateUUID(assetID); err != nil {
		return "", err
	}
	if err := validateExtension(extension); err != nil {
		return "", err
	}
	return fmt.Sprintf("asset/%s.%s", assetID, extension), nil
}

func MediaObjectKey(fileID, extension string) (string, error) {
	if err := validateUUID(fileID); err != nil {
		return "", err
	}
	if err := validateExtension(extension); err != nil {
		return "", err
	}
	return fmt.Sprintf("media/%s.%s", fileID, extension), nil
}

func MediaHLSObjectPrefix(fileID, generationID string) (string, error) {
	if err := validateUUID(fileID); err != nil {
		return "", err
	}
	if err := validateUUID(generationID); err != nil {
		return "", err
	}
	return fmt.Sprintf("media/%s/hls/%s", fileID, generationID), nil
}

func AssetPath(assetID, filename, extension string) (string, error) {
	if _, err := AssetObjectKey(assetID, extension); err != nil {
		return "", err
	}
	if err := validateAssetFilename(filename); err != nil {
		return "", err
	}
	return fmt.Sprintf("/asset/%s/%s.%s", assetID, filename, extension), nil
}

func SignedMediaPath(token, fileID, extension string) (string, error) {
	if !validTokenWire(token) {
		return "", ErrInvalidPath
	}
	key, err := MediaObjectKey(fileID, extension)
	if err != nil {
		return "", err
	}
	return "/media/" + token + "/" + strings.TrimPrefix(key, "media/"), nil
}

func PublicMediaHLSPath(fileID, generationID, objectName string) (string, error) {
	if err := validateHLSObjectName(objectName); err != nil {
		return "", ErrInvalidPath
	}
	prefix, err := MediaHLSObjectPrefix(fileID, generationID)
	if err != nil {
		return "", err
	}
	return "/media/" + strings.TrimPrefix(prefix, "media/") + "/" + objectName, nil
}

func ParseDeliveryPath(requestPath string) (DeliveryPath, error) {
	if requestPath == "" || strings.ContainsAny(requestPath, `\\?#%`) {
		return DeliveryPath{}, ErrInvalidPath
	}
	if strings.HasPrefix(requestPath, "/asset/") {
		return parseAssetDeliveryPath(requestPath)
	}
	if strings.HasPrefix(requestPath, "/media/") {
		return parseMediaDeliveryPath(requestPath)
	}
	return DeliveryPath{}, ErrInvalidPath
}

func parseAssetDeliveryPath(requestPath string) (DeliveryPath, error) {
	parts := strings.Split(strings.TrimPrefix(requestPath, "/asset/"), "/")
	if len(parts) != 2 {
		return DeliveryPath{}, ErrInvalidPath
	}
	if err := validateUUID(parts[0]); err != nil {
		return DeliveryPath{}, err
	}
	filename, extension, err := splitFilenameAndExtension(parts[1])
	if err != nil {
		return DeliveryPath{}, err
	}
	key, _ := AssetObjectKey(parts[0], extension)
	return DeliveryPath{Kind: PathAsset, AssetID: parts[0], Filename: filename, Extension: extension, ObjectKey: key}, nil
}

func splitFilenameAndExtension(value string) (string, string, error) {
	dot := strings.LastIndexByte(value, '.')
	if dot <= 0 || dot == len(value)-1 {
		return "", "", ErrInvalidPath
	}
	filename, extension := value[:dot], value[dot+1:]
	if err := validateAssetFilename(filename); err != nil {
		return "", "", err
	}
	if err := validateExtension(extension); err != nil {
		return "", "", err
	}
	return filename, extension, nil
}

func parseMediaDeliveryPath(requestPath string) (DeliveryPath, error) {
	parts := strings.Split(strings.TrimPrefix(requestPath, "/media/"), "/")
	if len(parts) != 2 && len(parts) != 4 {
		return DeliveryPath{}, ErrInvalidPath
	}

	if len(parts) == 2 {
		if !validTokenWire(parts[0]) {
			return DeliveryPath{}, ErrInvalidPath
		}
		fileID, extension, err := splitIDAndExtension(parts[1])
		if err != nil {
			return DeliveryPath{}, err
		}
		key, _ := MediaObjectKey(fileID, extension)
		return DeliveryPath{Kind: PathMediaObject, Token: parts[0], FileID: fileID, Extension: extension, ObjectKey: key}, nil
	}

	if parts[1] != "hls" {
		return DeliveryPath{}, ErrInvalidPath
	}
	if err := validateUUID(parts[0]); err != nil {
		return DeliveryPath{}, err
	}
	if err := validateUUID(parts[2]); err != nil {
		return DeliveryPath{}, err
	}
	if err := validateHLSObjectName(parts[3]); err != nil {
		return DeliveryPath{}, ErrInvalidPath
	}
	prefix, _ := MediaHLSObjectPrefix(parts[0], parts[2])
	return DeliveryPath{
		Kind: PathMediaHLS, FileID: parts[0], GenerationID: parts[2],
		ObjectName: parts[3], ObjectKey: prefix + "/" + parts[3],
	}, nil
}

func splitIDAndExtension(value string) (string, string, error) {
	dot := strings.LastIndexByte(value, '.')
	if dot <= 0 || dot == len(value)-1 {
		return "", "", ErrInvalidPath
	}
	id, extension := value[:dot], value[dot+1:]
	if err := validateUUID(id); err != nil {
		return "", "", err
	}
	if err := validateExtension(extension); err != nil {
		return "", "", err
	}
	return id, extension, nil
}

func validateAssetFilename(value string) error {
	if len(value) == 0 || len(value) > maxAssetFilenameLength || !assetFilenamePattern.MatchString(value) {
		return ErrInvalidPath
	}
	switch value {
	case "image", "thumbnail", "waveform", "spectrogram", "mesh", "og", "avatar", "logo", "favicon", "loader",
		"gallery", "artwork", "poster", "map_image", "texture", "email_image":
		return nil
	default:
		return ErrInvalidPath
	}
}

func validateClaims(claims *Claims) error {
	if claims == nil || claims.Version != TokenVersion {
		return ErrInvalidToken
	}
	max, ok := maxTTL(claims.Purpose)
	if !ok {
		return ErrInvalidToken
	}
	if err := validateScope(claims.Purpose, claims.ScopeType, claims.ScopeValue); err != nil {
		return ErrInvalidToken
	}
	if claims.IssuedAtUnix <= 0 || claims.ExpiryUnix <= claims.IssuedAtUnix {
		return ErrInvalidToken
	}
	if claims.ExpiryUnix-claims.IssuedAtUnix > int64(max/time.Second) {
		return ErrInvalidToken
	}
	if claims.Purpose != PurposeDownload && claims.Filename != "" {
		return ErrInvalidToken
	}
	if !validFilename(claims.Filename) {
		return ErrInvalidToken
	}

	if len(claims.Methods) != 2 || claims.Methods[0] != MethodGet || claims.Methods[1] != MethodHead {
		return ErrInvalidToken
	}
	return nil
}

func validateScope(purpose Purpose, scopeType ScopeType, scope string) error {
	canonical, err := canonicalObjectPath(scope)
	if err != nil || canonical != scope {
		return ErrInvalidToken
	}

	switch purpose {
	case PurposeInline, PurposeDownload:
		if scopeType != ScopeExact {
			return ErrInvalidToken
		}
		parts := strings.Split(scope, "/")
		if len(parts) != 2 || parts[0] != "media" {
			return ErrInvalidToken
		}
		_, _, err := splitIDAndExtension(parts[1])
		if err != nil {
			return ErrInvalidToken
		}
	default:
		return ErrInvalidToken
	}
	return nil
}

func canonicalObjectPath(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return "", ErrInvalidPath
	}
	if strings.ContainsAny(value, `\\?#%`) || strings.Contains(value, "//") {
		return "", ErrInvalidPath
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidPath
		}
	}
	return value, nil
}

func validateUUID(value string) error {
	if !uuidPattern.MatchString(value) {
		return ErrInvalidPath
	}
	return nil
}

func validateExtension(value string) error {
	if !extensionPattern.MatchString(value) {
		return ErrInvalidPath
	}
	return nil
}

func validateHLSObjectName(value string) error {
	if len(value) > maxHLSObjectNameLength || !hlsObjectPattern.MatchString(value) || value == "." || value == ".." {
		return ErrInvalidPath
	}
	return nil
}

func validTokenWire(value string) bool {
	if len(value) > maxTokenLength || !tokenPattern.MatchString(value) {
		return false
	}
	_, signature, _ := strings.Cut(value, ".")
	return len(signature) == base64.RawURLEncoding.EncodedLen(sha256.Size)
}

func validFilename(value string) bool {
	if len(value) > maxFilenameLength || !utf8.ValidString(value) || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func maxTTL(purpose Purpose) (time.Duration, bool) {
	switch purpose {
	case PurposeInline:
		return InlineTTL, true
	case PurposeDownload:
		return DownloadTTL, true
	default:
		return 0, false
	}
}

func computeSignature(payloadEncoded, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(payloadEncoded))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
