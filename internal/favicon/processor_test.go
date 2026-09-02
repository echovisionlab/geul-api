package favicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateFaviconSourceFormats(t *testing.T) {
	pngSource := faviconTestPNG(t, 151, 152, color.NRGBA{R: 220, A: 255})
	require.NoError(t, ValidateSource(pngSource, "image/png"))

	svgSource := []byte(
		`<svg xmlns="http://www.w3.org/2000/svg" width="151" height="152">` +
			`<rect width="151" height="152" fill="red"/></svg>`,
	)
	require.NoError(t, ValidateSource(svgSource, "image/svg+xml"))

	icoSource := faviconTestICO(t, 16, 32, 48)
	require.NoError(t, ValidateSource(icoSource, "image/x-icon"))
	require.NoError(t, ValidateSource(icoSource, "image/vnd.microsoft.icon"))
}

func TestValidateFaviconSVGRejectsExternalReferences(t *testing.T) {
	for _, source := range [][]byte{
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><image href="https://example.com/a.png"/></svg>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>.x{fill:url(https://example.com/x)}</style></svg>`),
		[]byte(`<!DOCTYPE svg><svg xmlns="http://www.w3.org/2000/svg"/>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div/></foreignObject></svg>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><style>@import "https://example.com/x.css";</style></svg>`),
		[]byte(`<?unsafe value?><svg xmlns="http://www.w3.org/2000/svg"/>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:red"/></svg>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:u\72l(https://example.com/x)"/></svg>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><animate attributeName="opacity" values="0;1"/></svg>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><set attributeName="fill" to="red"/></svg>`),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg" xml:base="https://example.com/"><use href="#x"/></svg>`),
		[]byte(
			`<svg xmlns="http://www.w3.org/2000/svg" xmlns:h="http://www.w3.org/1999/xhtml">` +
				`<h:img src="https://example.com/x"/></svg>`,
		),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`),
	} {
		require.Error(t, validateFaviconSVGSource(source))
	}
}

func TestValidateFaviconSVGAllowsNamespacesLocalReferencesAndBenignText(t *testing.T) {
	source := []byte(`<svg xmlns="http://www.w3.org/2000/svg"
		xmlns:xlink="http://www.w3.org/1999/xlink" width="151" height="152">
		<defs><linearGradient id="g"><stop offset="0" stop-color="red"/></linearGradient></defs>
		<title>file: is ordinary text here</title>
		<rect width="151" height="152" fill="url(#g)" filter="url('#shadow')"/>
		<use xlink:href="#g"/>
	</svg>`)
	require.NoError(t, validateFaviconSVGSource(source))
}

func TestValidateGeneratedFaviconOutputsRequiresExactSet(t *testing.T) {
	outputs := faviconTestGeneratedOutputs(t)
	require.NoError(t, ValidateOutputs(outputs, "image/png"))

	missing := append([]Output(nil), outputs[:len(outputs)-1]...)
	require.ErrorContains(t, ValidateOutputs(missing, "image/png"), "want 7")

	wrongSize := append([]Output(nil), outputs...)
	wrongSize[1].Data = faviconTestPNG(t, 17, 17, color.NRGBA{R: 1, A: 255})
	require.Error(t, ValidateOutputs(wrongSize, "image/png"))
}

func TestImageMagickFaviconProcessorPNGSVGAndICO(t *testing.T) {
	_, err := exec.LookPath("magick")
	require.NoError(t, err, "ImageMagick is required for favicon processor tests")
	processor := NewProcessor()
	fixtures := []struct {
		name string
		mime string
		data []byte
	}{
		{
			name: "png-151x152",
			mime: "image/png",
			data: faviconTestPNG(t, 151, 152, color.NRGBA{R: 220, A: 255}),
		},
		{
			name: "svg",
			mime: "image/svg+xml",
			data: []byte(
				`<svg xmlns="http://www.w3.org/2000/svg" width="151" height="152">` +
					`<rect width="151" height="152" fill="red"/></svg>`,
			),
		},
		{name: "ico", mime: "image/vnd.microsoft.icon", data: faviconTestICO(t, 16, 32, 48)},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			outputs, err := processor.Process(t.Context(), fixture.data, fixture.mime)
			require.NoError(t, err)
			require.NoError(t, ValidateOutputs(outputs, fixture.mime))
			if fixture.name != "png-151x152" {
				return
			}
			var icon180 []byte
			for _, output := range outputs {
				if output.Spec.PixelSize == 180 {
					icon180 = output.Data
					break
				}
			}
			img, err := png.Decode(bytes.NewReader(icon180))
			require.NoError(t, err)
			_, _, _, centerAlpha := img.At(90, 90).RGBA()
			require.NotZero(t, centerAlpha)
			transparentCorner := false
			for _, point := range []image.Point{{0, 0}, {179, 0}, {0, 179}, {179, 179}} {
				_, _, _, alpha := img.At(point.X, point.Y).RGBA()
				transparentCorner = transparentCorner || alpha == 0
			}
			require.True(t, transparentCorner, "151x152 source should be transparently padded, not cropped")
		})
	}
}

func TestImageMagickFaviconProcessorTimeout(t *testing.T) {
	_, err := exec.LookPath("sh")
	require.NoError(t, err, "a POSIX shell is required for the timeout fixture")
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-magick")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o700))
	processor := &imageMagickProcessor{binary: script, timeout: 20 * time.Millisecond}
	_, err = processor.Process(t.Context(), faviconTestPNG(t, 16, 16, color.NRGBA{R: 1, A: 255}), "image/png")
	require.ErrorContains(t, err, "deadline exceeded")
}

func faviconTestGeneratedOutputs(t *testing.T) []Output {
	t.Helper()
	outputs := make([]Output, 0, len(requiredOutputSpecs))
	for _, spec := range requiredOutputSpecs {
		var data []byte
		if spec.MimeType == "image/vnd.microsoft.icon" {
			data = faviconTestICO(t, 16, 32, 48)
		} else {
			data = faviconTestPNG(t, spec.PixelSize, spec.PixelSize, color.NRGBA{R: 200, A: 255})
		}
		outputs = append(outputs, Output{Spec: spec, Data: data})
	}
	return outputs
}

func faviconTestPNG(t *testing.T, width int, height int, fill color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, fill)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func faviconTestICO(t *testing.T, sizes ...int) []byte {
	t.Helper()
	frames := make([][]byte, len(sizes))
	directorySize := 6 + 16*len(sizes)
	totalSize := directorySize
	for i, size := range sizes {
		frames[i] = faviconTestPNG(t, size, size, color.NRGBA{R: uint8(50 + i*40), A: 255})
		totalSize += len(frames[i])
	}
	data := make([]byte, totalSize)
	binary.LittleEndian.PutUint16(data[2:4], 1)
	binary.LittleEndian.PutUint16(data[4:6], uint16(len(frames)))
	offset := directorySize
	for i, frame := range frames {
		base := 6 + i*16
		size := sizes[i]
		if size < 256 {
			data[base] = byte(size)
			data[base+1] = byte(size)
		}
		binary.LittleEndian.PutUint16(data[base+4:base+6], 1)
		binary.LittleEndian.PutUint16(data[base+6:base+8], 32)
		binary.LittleEndian.PutUint32(data[base+8:base+12], uint32(len(frame)))
		binary.LittleEndian.PutUint32(data[base+12:base+16], uint32(offset))
		copy(data[offset:], frame)
		offset += len(frame)
	}
	return data
}
