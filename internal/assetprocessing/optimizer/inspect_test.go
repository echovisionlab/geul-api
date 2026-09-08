package optimizer

import (
	"os"
	"testing"
)

const triangleInspectCSV = `
 OVERVIEW
 ────────────────────────────────────────────
key,value
version,2.0
generator,example immersive scene fixture
extensionsUsed,none
extensionsRequired,none

 SCENES
 ────────────────────────────────────────────
#,name,rootName,bboxMin,bboxMax,renderVertexCount,uploadVertexCount,uploadNaiveVertexCount
0,,FixtureMesh,"0, 0, 0","1, 1, 0",3,3,3

 MESHES
 ────────────────────────────────────────────
#,name,mode,meshPrimitives,glPrimitives,vertices,indices,attributes,instances,size
0,,TRIANGLES,1,1,3,,POSITION:f32,1,36

 MATERIALS
 ────────────────────────────────────────────
No materials found.

 TEXTURES
 ────────────────────────────────────────────
No textures found.

 ANIMATIONS
 ────────────────────────────────────────────
No animations found.
`

func TestParseInspectCSV(t *testing.T) {
	stats, err := ParseInspectCSV(triangleInspectCSV)
	if err != nil {
		t.Fatalf("parse inspect CSV: %v", err)
	}
	if stats.Version != "2.0" || stats.Generator != "example immersive scene fixture" {
		t.Fatalf("unexpected overview: %#v", stats)
	}
	if stats.SceneCount != 1 || stats.MeshCount != 1 {
		t.Fatalf("unexpected counts: scenes=%d meshes=%d", stats.SceneCount, stats.MeshCount)
	}
	if stats.RenderVertexCount != 3 || stats.UploadVertexCount != 3 {
		t.Fatalf("unexpected vertices: render=%d upload=%d", stats.RenderVertexCount, stats.UploadVertexCount)
	}
	if stats.MeshPrimitiveCount != 1 || stats.GLPrimitiveCount != 1 || stats.MeshSizeBytes != 36 {
		t.Fatalf("unexpected mesh stats: %#v", stats)
	}
	if stats.UsesDracoCompression {
		t.Fatalf("triangle fixture should not report Draco compression")
	}
}

func TestTriangleGLBFixtureExists(t *testing.T) {
	info, err := os.Stat("../testdata/triangle.glb")
	if err != nil {
		t.Fatalf("expected copied GLB fixture: %v", err)
	}
	if info.Size() != 516 {
		t.Fatalf("unexpected fixture size: %d", info.Size())
	}
}

func TestParseInspectCSVAllSectionsAndCompression(t *testing.T) {
	report := `
OVERVIEW
key,value
extensionsUsed,"EXT_meshopt_compression, KHR_draco_mesh_compression, none, "
extensionsRequired,none
garbage
SCENES
#,renderVertexCount,uploadVertexCount
0,invalid,
MESHES
#,meshPrimitives,glPrimitives,size
0,invalid,,invalid
MATERIALS
#,name
0,material
TEXTURES
#,name
0,texture
ANIMATIONS
#,name
0,animation
`
	stats, err := ParseInspectCSV(report)
	if err != nil {
		t.Fatalf("parse report: %v", err)
	}
	if !stats.UsesDracoCompression || stats.MaterialCount != 1 || stats.TextureCount != 1 || stats.AnimationCount != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if stats.RenderVertexCount != 0 || stats.MeshPrimitiveCount != 0 || stats.MeshSizeBytes != 0 {
		t.Fatalf("invalid numeric fields should be ignored: %#v", stats)
	}
}

func TestInspectCSVHelpers(t *testing.T) {
	if _, err := parseCSVLine("\"unterminated"); err == nil {
		t.Fatal("expected malformed CSV error")
	}
	if isHeader(nil) {
		t.Fatal("empty fields are not a header")
	}
	if !isHeader([]string{"key"}) || !isHeader([]string{"#"}) || isHeader([]string{"value"}) {
		t.Fatal("unexpected header classification")
	}
}

func TestInspectCSVListHelpers(t *testing.T) {
	stats := Inspection{}
	applyOverviewField(&stats, "unknown", "ignored")
	if stats.Version != "" || stats.Generator != "" || stats.SceneCount != 0 || stats.ExtensionsUsed != nil {
		t.Fatalf("unknown overview field mutated stats: %#v", stats)
	}
	if got := splitList(""); got != nil {
		t.Fatalf("expected nil empty list, got %#v", got)
	}
	if got := splitList("none"); got != nil {
		t.Fatalf("expected nil none list, got %#v", got)
	}
	if got := splitList(" alpha, none, , beta "); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("unexpected split list: %#v", got)
	}
	if !contains([]string{"alpha", "beta"}, "beta") || contains([]string{"alpha"}, "gamma") {
		t.Fatal("unexpected contains result")
	}
}

func TestInspectCSVFieldHelpers(t *testing.T) {
	headers := map[string]int{"value": 0, "outside": 3, "negative": -1}
	if got := stringField([]string{" 42 "}, headers, "missing"); got != "" {
		t.Fatalf("missing field: %q", got)
	}
	if got := stringField([]string{" 42 "}, headers, "outside"); got != "" {
		t.Fatalf("outside field: %q", got)
	}
	if got := stringField([]string{" 42 "}, headers, "negative"); got != "" {
		t.Fatalf("negative field: %q", got)
	}
	if got := intField([]string{" 42 "}, headers, "value"); got != 42 {
		t.Fatalf("parsed int: %d", got)
	}
	if got := intField([]string{" "}, headers, "value"); got != 0 {
		t.Fatalf("empty int: %d", got)
	}
	if got := parseSizeBytes(" "); got != 0 {
		t.Fatalf("empty size: %d", got)
	}
	if got := parseSizeBytes("invalid"); got != 0 {
		t.Fatalf("invalid size: %d", got)
	}
}
