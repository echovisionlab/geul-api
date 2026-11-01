package favicon

import "github.com/echovisionlab/geul-api/internal/mediaasset"

type DerivativeType string

const (
	ICO           DerivativeType = "FILE_DERIVATIVE_TYPE_FAVICON_ICO"
	PNG16         DerivativeType = "FILE_DERIVATIVE_TYPE_FAVICON_PNG_16"
	PNG32         DerivativeType = "FILE_DERIVATIVE_TYPE_FAVICON_PNG_32"
	PNG48         DerivativeType = "FILE_DERIVATIVE_TYPE_FAVICON_PNG_48"
	AppleTouch180 DerivativeType = "FILE_DERIVATIVE_TYPE_FAVICON_APPLE_TOUCH_180"
	Manifest192   DerivativeType = "FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_192"
	Manifest512   DerivativeType = "FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_512"
)

func (t DerivativeType) String() string { return string(t) }

type Set struct {
	IconICO, IconPNG16, IconPNG32, IconPNG48         *mediaasset.Reference
	AppleTouch180, Manifest192, Manifest512, IconSVG *mediaasset.Reference
}
