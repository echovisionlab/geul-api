package favicon

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	faviconProcessorTimeout = 10 * time.Second
	// MaxSourceSize is the largest accepted source favicon upload.
	MaxSourceSize          = 2 * 1024 * 1024
	faviconMaxSourceSide   = 4096
	faviconMaxSourcePixels = 16 * 1024 * 1024
)

// Spec describes one required generated favicon output.
type Spec struct {
	DerivativeType managev1.FileDerivativeType
	Extension      string
	MimeType       string
	PixelSize      int
}

var requiredOutputSpecs = []Spec{
	{managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_ICO, "ico", "image/vnd.microsoft.icon", 0},
	{managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_16, "png", "image/png", 16},
	{managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_32, "png", "image/png", 32},
	{managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_PNG_48, "png", "image/png", 48},
	{managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_APPLE_TOUCH_180, "png", "image/png", 180},
	{managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_192, "png", "image/png", 192},
	{managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_FAVICON_MANIFEST_512, "png", "image/png", 512},
}

// RequiredOutputs returns the exact generated derivatives required for a favicon.
func RequiredOutputs() []Spec {
	return append([]Spec(nil), requiredOutputSpecs...)
}

// Output is one generated favicon derivative.
type Output struct {
	Spec Spec
	Data []byte
}

// Processor transforms a validated source into the complete favicon set.
type Processor interface {
	Process(ctx context.Context, source []byte, sourceMime string) ([]Output, error)
}

type imageMagickProcessor struct {
	binary  string
	timeout time.Duration
}

// NewProcessor returns the ImageMagick favicon processor with fixed resource limits.
func NewProcessor() Processor {
	return &imageMagickProcessor{binary: "magick", timeout: faviconProcessorTimeout}
}

func (p *imageMagickProcessor) Process(
	ctx context.Context,
	source []byte,
	sourceMime string,
) ([]Output, error) {
	sourceMime = normalizedMimeType(sourceMime)
	if sourceMime == "image/vnd.microsoft.icon" {
		sourceMime = "image/x-icon"
	}
	if err := ValidateSource(source, sourceMime); err != nil {
		return nil, err
	}

	timeout := p.timeout
	if timeout <= 0 {
		timeout = faviconProcessorTimeout
	}
	processCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tempDir, err := os.MkdirTemp("", "geul-favicon-")
	if err != nil {
		return nil, fmt.Errorf("create favicon processor workspace: %w", err)
	}
	defer os.RemoveAll(tempDir)

	sourceExtension := model.GetExtensionFromMime(sourceMime)
	if sourceExtension == "bin" {
		return nil, fmt.Errorf("unsupported favicon source MIME %q", sourceMime)
	}
	sourcePath := filepath.Join(tempDir, "source."+sourceExtension)
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		return nil, fmt.Errorf("write favicon processor source: %w", err)
	}

	inputPath := sourcePath
	if sourceMime == "image/x-icon" {
		entries, err := parseICODirectory(source)
		if err != nil {
			return nil, err
		}
		largestIndex := 0
		for i := range entries {
			if entries[i].Width*entries[i].Height > entries[largestIndex].Width*entries[largestIndex].Height {
				largestIndex = i
			}
		}
		inputPath = fmt.Sprintf("%s[%d]", sourcePath, largestIndex)
	}

	pngPaths := make(map[int]string, len(requiredOutputSpecs)-1)
	outputs := make([]Output, 0, len(requiredOutputSpecs))
	for _, spec := range requiredOutputSpecs {
		if spec.MimeType != "image/png" {
			continue
		}
		outputPath := filepath.Join(tempDir, fmt.Sprintf("favicon-%d.png", spec.PixelSize))
		if err := p.renderSquarePNG(processCtx, inputPath, outputPath, spec.PixelSize, tempDir); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(outputPath)
		if err != nil {
			return nil, fmt.Errorf("read generated %dpx favicon: %w", spec.PixelSize, err)
		}
		outputs = append(outputs, Output{Spec: spec, Data: data})
		pngPaths[spec.PixelSize] = outputPath
	}

	icoPath := filepath.Join(tempDir, "favicon.ico")
	icoInputs := []string{pngPaths[16], pngPaths[32], pngPaths[48]}
	if err := p.run(processCtx, tempDir, append(p.resourceArgs(), append(icoInputs, icoPath)...)...); err != nil {
		return nil, fmt.Errorf("generate multi-frame favicon ICO: %w", err)
	}
	icoData, err := os.ReadFile(icoPath)
	if err != nil {
		return nil, fmt.Errorf("read generated favicon ICO: %w", err)
	}
	outputs = append(outputs, Output{Spec: requiredOutputSpecs[0], Data: icoData})

	if err := ValidateOutputs(outputs, sourceMime); err != nil {
		return nil, err
	}
	sort.Slice(outputs, func(i, j int) bool {
		return outputs[i].Spec.DerivativeType < outputs[j].Spec.DerivativeType
	})
	return outputs, nil
}

func (p *imageMagickProcessor) renderSquarePNG(
	ctx context.Context,
	inputPath string,
	outputPath string,
	size int,
	tempDir string,
) error {
	sizeArg := fmt.Sprintf("%dx%d", size, size)
	args := append(p.resourceArgs(),
		inputPath,
		"-auto-orient",
		"-background", "none",
		"-alpha", "on",
		"-resize", sizeArg,
		"-gravity", "center",
		"-extent", sizeArg,
		"-strip",
		"-define", "png:exclude-chunks=date,time",
		outputPath,
	)
	if err := p.run(ctx, tempDir, args...); err != nil {
		return fmt.Errorf("render %dpx favicon PNG: %w", size, err)
	}
	return nil
}

func (p *imageMagickProcessor) resourceArgs() []string {
	return []string{
		"-limit", "thread", "1",
		"-limit", "width", fmt.Sprintf("%d", faviconMaxSourceSide),
		"-limit", "height", fmt.Sprintf("%d", faviconMaxSourceSide),
		"-limit", "area", "16MP",
		"-limit", "memory", "128MiB",
		"-limit", "map", "256MiB",
		"-limit", "disk", "256MiB",
	}
}

func (p *imageMagickProcessor) run(ctx context.Context, tempDir string, args ...string) error {
	binary := strings.TrimSpace(p.binary)
	if binary == "" {
		binary = "magick"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(), "MAGICK_TEMPORARY_PATH="+tempDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	var stderr cappedBuffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("processor deadline exceeded: %w", ctx.Err())
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type cappedBuffer struct {
	data []byte
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	const max = 4096
	remaining := max - len(b.data)
	if remaining > 0 {
		if len(p) < remaining {
			remaining = len(p)
		}
		b.data = append(b.data, p[:remaining]...)
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.data) }

// ValidateSource checks source bytes and dimensions before processing.
func ValidateSource(source []byte, sourceMime string) error {
	if len(source) == 0 || len(source) > MaxSourceSize {
		return fmt.Errorf("favicon source must be between 1 byte and %d bytes", MaxSourceSize)
	}
	switch sourceMime {
	case "image/png":
		cfg, err := png.DecodeConfig(bytes.NewReader(source))
		if err != nil {
			return fmt.Errorf("decode favicon PNG: %w", err)
		}
		if cfg.Width <= 0 || cfg.Height <= 0 ||
			cfg.Width > faviconMaxSourceSide || cfg.Height > faviconMaxSourceSide ||
			cfg.Width*cfg.Height > faviconMaxSourcePixels {
			return fmt.Errorf("favicon PNG dimensions exceed the processor limit")
		}
	case "image/x-icon", "image/vnd.microsoft.icon":
		entries, err := parseICODirectory(source)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return fmt.Errorf("favicon ICO has no image frames")
		}
	case "image/svg+xml":
		if err := validateFaviconSVGSource(source); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported favicon source MIME %q", sourceMime)
	}
	return nil
}

func validateFaviconSVGSource(source []byte) error {
	validator := faviconSVGValidator{decoder: xml.NewDecoder(bytes.NewReader(source))}
	for {
		token, err := validator.decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("parse favicon SVG: %w", err)
		}
		if err := validator.validateToken(token); err != nil {
			return err
		}
	}
	if !validator.rootSeen {
		return fmt.Errorf("favicon SVG has no svg root element")
	}
	return nil
}

type faviconSVGValidator struct {
	decoder    *xml.Decoder
	rootSeen   bool
	rootClosed bool
	depth      int
}

func (v *faviconSVGValidator) validateToken(token xml.Token) error {
	switch value := token.(type) {
	case xml.Directive:
		return fmt.Errorf("favicon SVG directives are not allowed")
	case xml.ProcInst:
		return fmt.Errorf("favicon SVG processing instructions are not allowed")
	case xml.StartElement:
		return v.validateStartElement(value)
	case xml.EndElement:
		return v.validateEndElement()
	case xml.CharData:
		return v.validateCharacterData(value)
	default:
		return nil
	}
}

func (v *faviconSVGValidator) validateStartElement(value xml.StartElement) error {
	const svgNamespace = "http://www.w3.org/2000/svg"
	if v.rootClosed {
		return fmt.Errorf("favicon SVG must contain exactly one root element")
	}
	element := strings.ToLower(value.Name.Local)
	if value.Name.Space != "" && value.Name.Space != svgNamespace {
		return fmt.Errorf("favicon SVG foreign element namespaces are not allowed")
	}
	if err := v.validateRootElement(element); err != nil {
		return err
	}
	v.depth++
	if isActiveFaviconSVGElement(element) {
		return fmt.Errorf("favicon SVG active element %s is not allowed", element)
	}
	for _, attribute := range value.Attr {
		if err := validateFaviconSVGAttribute(attribute); err != nil {
			return err
		}
	}
	return nil
}

func (v *faviconSVGValidator) validateRootElement(element string) error {
	if v.depth != 0 {
		return nil
	}
	if v.rootSeen {
		return fmt.Errorf("favicon SVG must contain exactly one root element")
	}
	if element != "svg" {
		return fmt.Errorf("favicon SVG root element must be svg")
	}
	v.rootSeen = true
	return nil
}

func (v *faviconSVGValidator) validateEndElement() error {
	v.depth--
	if v.depth < 0 {
		return fmt.Errorf("favicon SVG has an invalid element structure")
	}
	if v.depth == 0 {
		v.rootClosed = true
	}
	return nil
}

func (v *faviconSVGValidator) validateCharacterData(value xml.CharData) error {
	if v.rootClosed && strings.TrimSpace(string(value)) != "" {
		return fmt.Errorf("favicon SVG contains data outside its root element")
	}
	return nil
}

func isActiveFaviconSVGElement(element string) bool {
	switch element {
	case "script", "foreignobject", "iframe", "object", "embed", "style",
		"animate", "animatemotion", "animatetransform", "set", "discard",
		"audio", "video", "canvas", "handler", "listener":
		return true
	default:
		return false
	}
}

func validateFaviconSVGAttribute(attribute xml.Attr) error {
	const xmlNamespace = "http://www.w3.org/XML/1998/namespace"
	name := strings.ToLower(attribute.Name.Local)
	if strings.HasPrefix(name, "on") {
		return fmt.Errorf("favicon SVG event handler attributes are not allowed")
	}
	if name == "style" {
		return fmt.Errorf("favicon SVG style attributes are not allowed")
	}
	if attribute.Name.Space == xmlNamespace && name == "base" {
		return fmt.Errorf("favicon SVG xml:base is not allowed")
	}
	if strings.Contains(attribute.Value, "\\") {
		return fmt.Errorf("favicon SVG escaped attribute values are not allowed")
	}
	if err := validateFaviconSVGResourceAttribute(name, attribute.Value); err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(attribute.Value), "url(") {
		return validateFaviconSVGLocalURLs(attribute.Value)
	}
	return nil
}

func validateFaviconSVGResourceAttribute(name, value string) error {
	if name != "href" && name != "src" {
		return nil
	}
	reference := strings.TrimSpace(value)
	if reference != "" && !strings.HasPrefix(reference, "#") {
		return fmt.Errorf("favicon SVG contains a non-local resource reference")
	}
	return nil
}

func validateFaviconSVGLocalURLs(value string) error {
	lower := strings.ToLower(value)
	remaining := lower
	for {
		start := strings.Index(remaining, "url(")
		if start < 0 {
			return nil
		}
		remaining = remaining[start+4:]
		end := strings.IndexByte(remaining, ')')
		if end < 0 {
			return fmt.Errorf("favicon SVG contains a malformed URL reference")
		}
		reference := strings.TrimSpace(remaining[:end])
		reference = strings.Trim(strings.TrimSpace(reference), "\"'")
		if !strings.HasPrefix(reference, "#") {
			return fmt.Errorf("favicon SVG contains a non-local URL reference")
		}
		remaining = remaining[end+1:]
	}
}

type icoDirectoryEntry struct {
	Width  int
	Height int
	Size   uint32
	Offset uint32
}

func parseICODirectory(data []byte) ([]icoDirectoryEntry, error) {
	if len(data) < 6 || binary.LittleEndian.Uint16(data[0:2]) != 0 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		return nil, fmt.Errorf("invalid favicon ICO header")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count <= 0 || count > 64 || len(data) < 6+16*count {
		return nil, fmt.Errorf("invalid favicon ICO directory")
	}
	entries := make([]icoDirectoryEntry, 0, count)
	for i := range count {
		base := 6 + i*16
		width := int(data[base])
		height := int(data[base+1])
		if width == 0 {
			width = 256
		}
		if height == 0 {
			height = 256
		}
		size := binary.LittleEndian.Uint32(data[base+8 : base+12])
		offset := binary.LittleEndian.Uint32(data[base+12 : base+16])
		end := uint64(offset) + uint64(size)
		if width <= 0 || height <= 0 || size == 0 || offset < uint32(6+16*count) || end > uint64(len(data)) {
			return nil, fmt.Errorf("invalid favicon ICO frame %d", i)
		}
		entries = append(entries, icoDirectoryEntry{Width: width, Height: height, Size: size, Offset: offset})
	}
	return entries, nil
}

// ValidateOutputs checks that a processor returned the exact required set.
func ValidateOutputs(outputs []Output, _ string) error {
	expected := make(map[managev1.FileDerivativeType]Spec, len(requiredOutputSpecs))
	for _, spec := range requiredOutputSpecs {
		expected[spec.DerivativeType] = spec
	}
	if len(outputs) != len(expected) {
		return fmt.Errorf("favicon processor returned %d outputs, want %d", len(outputs), len(expected))
	}
	seen := make(map[managev1.FileDerivativeType]struct{}, len(outputs))
	for _, output := range outputs {
		if err := validateGeneratedFaviconOutput(output, expected, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateGeneratedFaviconOutput(
	output Output,
	expected map[managev1.FileDerivativeType]Spec,
	seen map[managev1.FileDerivativeType]struct{},
) error {
	spec, ok := expected[output.Spec.DerivativeType]
	if !ok {
		return fmt.Errorf("favicon processor returned unexpected derivative %s", output.Spec.DerivativeType.String())
	}
	if _, duplicate := seen[spec.DerivativeType]; duplicate {
		return fmt.Errorf("favicon processor returned duplicate derivative %s", spec.DerivativeType.String())
	}
	seen[spec.DerivativeType] = struct{}{}
	if output.Spec.Extension != spec.Extension || output.Spec.MimeType != spec.MimeType || len(output.Data) == 0 {
		return fmt.Errorf("favicon derivative %s has invalid metadata", spec.DerivativeType.String())
	}
	switch spec.MimeType {
	case "image/png":
		return validateGeneratedFaviconPNG(output.Data, spec)
	case "image/vnd.microsoft.icon":
		return validateGeneratedFaviconICO(output.Data, spec)
	default:
		return fmt.Errorf("favicon derivative %s has unsupported MIME type", spec.DerivativeType.String())
	}
}

func validateGeneratedFaviconPNG(data []byte, spec Spec) error {
	if normalizedMimeType(http.DetectContentType(data)) != "image/png" {
		return fmt.Errorf("favicon derivative %s is not PNG", spec.DerivativeType.String())
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != spec.PixelSize || config.Height != spec.PixelSize {
		return fmt.Errorf(
			"favicon derivative %s must be %dx%d PNG",
			spec.DerivativeType.String(), spec.PixelSize, spec.PixelSize,
		)
	}
	return nil
}

func validateGeneratedFaviconICO(data []byte, spec Spec) error {
	detected := normalizedMimeType(http.DetectContentType(data))
	if detected != "image/vnd.microsoft.icon" && detected != "image/x-icon" {
		return fmt.Errorf("favicon derivative %s is not ICO", spec.DerivativeType.String())
	}
	entries, err := parseICODirectory(data)
	if err != nil {
		return err
	}
	expectedSizes := []int{16, 32, 48}
	if len(entries) != len(expectedSizes) {
		return fmt.Errorf("favicon ICO must contain exactly three frames")
	}
	for index, size := range expectedSizes {
		if entries[index].Width != size || entries[index].Height != size {
			return fmt.Errorf("favicon ICO frame %d must be %dx%d", index, size, size)
		}
	}
	return nil
}
