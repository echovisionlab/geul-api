package proxy

import (
	"fmt"
	"net/url"
	"strconv"
)

type imageOptions struct {
	Width   int
	Height  int
	Quality int
	Fit     string
	Format  string
}

var publicImageDimensions = map[int]struct{}{
	32: {}, 48: {}, 64: {}, 96: {}, 128: {}, 160: {}, 192: {}, 256: {},
	320: {}, 480: {}, 640: {}, 768: {}, 960: {}, 1280: {}, 1600: {}, 2048: {},
	2560: {}, 3200: {}, 4096: {},
}

func parseImageOptions(query url.Values, public bool) (imageOptions, error) {
	if err := validateImageQuery(query, public); err != nil {
		return imageOptions{}, err
	}
	width, err := parseImageDimension(query, "w", "width", public)
	if err != nil {
		return imageOptions{}, err
	}
	height, err := parseImageDimension(query, "h", "height", public)
	if err != nil {
		return imageOptions{}, err
	}
	quality, err := parseImageQuality(query)
	if err != nil {
		return imageOptions{}, err
	}
	fit, err := parseImageFit(query)
	if err != nil {
		return imageOptions{}, err
	}
	format, err := parseImageFormat(query, public)
	if err != nil {
		return imageOptions{}, err
	}
	return imageOptions{Width: width, Height: height, Quality: quality, Fit: fit, Format: format}, nil
}

func validateImageQuery(query url.Values, public bool) error {
	for key, values := range query {
		if key != "w" && key != "h" && key != "q" && key != "fit" && (public || key != "format") {
			return fmt.Errorf("unsupported image option %q", key)
		}
		if len(values) != 1 || values[0] == "" {
			return fmt.Errorf("image option %q must have one value", key)
		}
	}
	return nil
}

func parseImageDimension(query url.Values, key, label string, public bool) (int, error) {
	value, err := getQueryInt(query, key)
	if err != nil {
		return 0, err
	}
	if value > 4096 || (value > 0 && public && !isPublicImageDimension(value)) {
		return 0, fmt.Errorf("unsupported image %s %d", label, value)
	}
	return value, nil
}

func parseImageQuality(query url.Values) (int, error) {
	quality, err := getQueryInt(query, "q")
	if err != nil {
		return 0, err
	}
	if quality > 100 {
		return 0, fmt.Errorf("unsupported image quality %d", quality)
	}
	if quality == 0 {
		return 80, nil
	}
	return quality, nil
}

func parseImageFit(query url.Values) (string, error) {
	fit := getQueryString(query, "fit")
	if fit != "" && fit != "fit" && fit != "fill" {
		return "", fmt.Errorf("unsupported image fit %q", fit)
	}
	if fit == "" {
		return "fit", nil
	}
	return fit, nil
}

func parseImageFormat(query url.Values, public bool) (string, error) {
	if public {
		return "", nil
	}
	format := getQueryString(query, "format")
	switch format {
	case "", "png", "jpg", "jpeg", "webp", "avif":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported image format %q", format)
	}
}

func isPublicImageDimension(value int) bool {
	_, ok := publicImageDimensions[value]
	return ok
}

func getQueryInt(query url.Values, key string) (int, error) {
	value := getQueryString(query, key)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("image option %q must be a positive integer", key)
	}
	return parsed, nil
}

func getQueryString(query url.Values, key string) string {
	values := query[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
