package domain

import "strconv"

type DerivativeKind int32

const (
	DerivativeUnspecified DerivativeKind = iota
	DerivativeThumbnail
	DerivativeHLS
	DerivativeSpectrogram
	DerivativeWaveform
	DerivativeFaviconICO
	DerivativeFaviconPNG16
	DerivativeFaviconPNG32
	DerivativeFaviconPNG48
	DerivativeFaviconAppleTouch180
	DerivativeFaviconManifest192
	DerivativeFaviconManifest512
	DerivativeOptimizedMesh
)

var derivativeNames = [...]string{
	"FILE_DERIVATIVE_TYPE_UNSPECIFIED", "FILE_DERIVATIVE_TYPE_THUMBNAIL", "FILE_DERIVATIVE_TYPE_HLS", "FILE_DERIVATIVE_TYPE_SPECTROGRAM", "FILE_DERIVATIVE_TYPE_WAVEFORM", "FILE_DERIVATIVE_TYPE_FAVICON_ICO", "FILE_DERIVATIVE_TYPE_FAVICON_PNG_16", "FILE_DERIVATIVE_TYPE_FAVICON_PNG_32", "FILE_DERIVATIVE_TYPE_FAVICON_PNG_48", "FILE_DERIVATIVE_TYPE_FAVICON_APPLE_TOUCH_180", "FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_192", "FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_512", "FILE_DERIVATIVE_TYPE_OPTIMIZED_MESH",
}

func (k DerivativeKind) String() string {
	if k >= 0 && int(k) < len(derivativeNames) {
		return derivativeNames[k]
	}
	return strconv.FormatInt(int64(k), 10)
}
func ParseDerivativeKind(stored string) (DerivativeKind, bool) {
	for k, name := range derivativeNames {
		if stored == name {
			return DerivativeKind(k), true
		}
	}
	return DerivativeUnspecified, false
}
