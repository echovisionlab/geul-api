package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

func (p *ImageProxy) buildImgproxyURL(s3Path string, opts imageOptions) (string, error) {
	optParts := imageOptionPathParts(opts)
	sourceURL := fmt.Sprintf("s3://%s/%s", p.cfg.S3MediaBucket, s3Path)
	pathToSign := fmt.Sprintf("/%s/plain/%s", strings.Join(optParts, "/"), sourceURL)
	signature, err := p.sign(pathToSign)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s%s", p.cfg.ImgproxyURL, signature, pathToSign), nil
}

func imageOptionPathParts(opts imageOptions) []string {
	parts := make([]string, 0, 3)
	if opts.Width > 0 || opts.Height > 0 {
		parts = append(parts, fmt.Sprintf("rs:%s:%d:%d", opts.Fit, opts.Width, opts.Height))
	}
	parts = append(parts, fmt.Sprintf("q:%d", opts.Quality))
	if opts.Format != "" {
		parts = append(parts, fmt.Sprintf("f:%s", opts.Format))
	}
	return parts
}

func (p *ImageProxy) sign(path string) (string, error) {
	key, err := decodeImgproxySecret("key", p.cfg.ImgproxyKey)
	if err != nil {
		return "", err
	}
	salt, err := decodeImgproxySecret("salt", p.cfg.ImgproxySalt)
	if err != nil {
		return "", err
	}

	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(salt)
	_, _ = mac.Write([]byte(path))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func decodeImgproxySecret(name, encoded string) ([]byte, error) {
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("decode imgproxy %s: invalid hexadecimal secret", name)
	}
	return decoded, nil
}
