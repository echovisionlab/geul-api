package optimizer

import (
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
)

type Inspection struct {
	Version              string
	Generator            string
	ExtensionsUsed       []string
	ExtensionsRequired   []string
	SceneCount           int
	MeshCount            int
	MaterialCount        int
	TextureCount         int
	AnimationCount       int
	RenderVertexCount    int64
	UploadVertexCount    int64
	MeshPrimitiveCount   int64
	GLPrimitiveCount     int64
	MeshSizeBytes        int64
	UsesDracoCompression bool
	RawReport            string
}

func ParseInspectCSV(report string) (Inspection, error) {
	parser := inspectParser{
		stats:   Inspection{RawReport: report},
		headers: map[string]int{},
	}

	for _, rawLine := range strings.Split(report, "\n") {
		if err := parser.consume(rawLine); err != nil {
			return Inspection{}, err
		}
	}

	parser.stats.UsesDracoCompression = contains(parser.stats.ExtensionsUsed, "KHR_draco_mesh_compression") ||
		contains(parser.stats.ExtensionsRequired, "KHR_draco_mesh_compression")
	return parser.stats, nil
}

type inspectParser struct {
	stats   Inspection
	section string
	headers map[string]int
}

func (p *inspectParser) consume(rawLine string) error {
	line := strings.TrimSpace(rawLine)
	if line == "" || strings.HasPrefix(line, "─") || strings.HasPrefix(line, "No ") {
		return nil
	}
	if isInspectionSection(line) {
		p.section = line
		p.headers = map[string]int{}
		return nil
	}
	if !strings.Contains(line, ",") {
		return nil
	}
	fields, err := parseCSVLine(line)
	if err != nil {
		return err
	}
	if isHeader(fields) {
		p.headers = indexHeaders(fields)
		return nil
	}
	p.applyRow(fields)
	return nil
}

func isInspectionSection(line string) bool {
	switch line {
	case "OVERVIEW", "SCENES", "MESHES", "MATERIALS", "TEXTURES", "ANIMATIONS":
		return true
	default:
		return false
	}
}

func (p *inspectParser) applyRow(fields []string) {
	switch p.section {
	case "OVERVIEW":
		if len(fields) >= 2 {
			applyOverviewField(&p.stats, fields[0], fields[1])
		}
	case "SCENES":
		p.stats.SceneCount++
		p.stats.RenderVertexCount += intField(fields, p.headers, "renderVertexCount")
		p.stats.UploadVertexCount += intField(fields, p.headers, "uploadVertexCount")
	case "MESHES":
		p.stats.MeshCount++
		p.stats.MeshPrimitiveCount += intField(fields, p.headers, "meshPrimitives")
		p.stats.GLPrimitiveCount += intField(fields, p.headers, "glPrimitives")
		p.stats.MeshSizeBytes += parseSizeBytes(stringField(fields, p.headers, "size"))
	case "MATERIALS":
		p.stats.MaterialCount++
	case "TEXTURES":
		p.stats.TextureCount++
	case "ANIMATIONS":
		p.stats.AnimationCount++
	}
}

func parseCSVLine(line string) ([]string, error) {
	reader := csv.NewReader(strings.NewReader(line))
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("parse inspect csv line %q: %w", line, err)
	}
	return fields, nil
}

func isHeader(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "key", "#":
		return true
	default:
		return false
	}
}

func indexHeaders(fields []string) map[string]int {
	index := make(map[string]int, len(fields))
	for i, field := range fields {
		index[field] = i
	}
	return index
}

func applyOverviewField(stats *Inspection, key, value string) {
	switch key {
	case "version":
		stats.Version = value
	case "generator":
		stats.Generator = value
	case "extensionsUsed":
		stats.ExtensionsUsed = splitList(value)
	case "extensionsRequired":
		stats.ExtensionsRequired = splitList(value)
	}
}

func splitList(value string) []string {
	if value == "" || value == "none" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && part != "none" {
			out = append(out, part)
		}
	}
	return out
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func stringField(fields []string, headers map[string]int, name string) string {
	index, ok := headers[name]
	if !ok || index < 0 || index >= len(fields) {
		return ""
	}
	return fields[index]
}

func intField(fields []string, headers map[string]int, name string) int64 {
	value := strings.TrimSpace(stringField(fields, headers, name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseSizeBytes(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		return parsed
	}
	return 0
}
