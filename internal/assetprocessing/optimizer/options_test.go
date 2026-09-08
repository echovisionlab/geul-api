package optimizer

import (
	"reflect"
	"testing"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func wantOptimizeArgs(extra ...string) []string {
	args := []string{
		"optimize", "in.glb", "out.glb",
		"--compress", "draco",
		"--texture-compress", "webp",
		"--texture-size", "1024",
	}
	return append(args, extra...)
}

func TestOptionsFromEventValidatesDracoRatio(t *testing.T) {
	valid := &apiv1.MeshOptimizationOptions{
		CompressionMethod:  apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO,
		TargetRatioPercent: 50,
		Profile:            apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
	}

	options, err := OptionsFromEvent(valid)
	if err != nil {
		t.Fatalf("expected valid options: %v", err)
	}
	if options.TargetRatioPercent != 50 {
		t.Fatalf("unexpected target ratio: %v", options.TargetRatioPercent)
	}
	if options.Profile != ProfileParticleMeshV1 {
		t.Fatalf("unexpected profile: %v", options.Profile)
	}

	tests := []struct {
		name    string
		options *apiv1.MeshOptimizationOptions
	}{
		{name: "nil options", options: nil},
		{name: "unspecified compression", options: &apiv1.MeshOptimizationOptions{TargetRatioPercent: 50, Profile: apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1}},
		{name: "unspecified ratio", options: &apiv1.MeshOptimizationOptions{CompressionMethod: apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO, Profile: apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1}},
		{name: "too small ratio", options: &apiv1.MeshOptimizationOptions{CompressionMethod: apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO, TargetRatioPercent: 0, Profile: apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1}},
		{name: "too large ratio", options: &apiv1.MeshOptimizationOptions{CompressionMethod: apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO, TargetRatioPercent: 110, Profile: apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1}},
		{name: "unspecified profile", options: &apiv1.MeshOptimizationOptions{CompressionMethod: apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO, TargetRatioPercent: 50, Profile: apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED}},
		{name: "unknown profile", options: &apiv1.MeshOptimizationOptions{CompressionMethod: apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO, TargetRatioPercent: 50, Profile: apiv1.MeshOptimizationProfile(99)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := OptionsFromEvent(tt.options); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestOptionsFromEventAcceptsExplicitDracoWebPProfile(t *testing.T) {
	options, err := OptionsFromEvent(&apiv1.MeshOptimizationOptions{
		CompressionMethod:  apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO,
		TargetRatioPercent: 50,
		Profile:            apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1,
	})
	if err != nil {
		t.Fatalf("explicit DRACO_WEBP_V1 profile: %v", err)
	}
	if options.Profile != ProfileDracoWebPV1 {
		t.Fatalf("profile: got %q want %q", options.Profile, ProfileDracoWebPV1)
	}
}

func TestProfileMappings(t *testing.T) {
	for _, test := range []struct {
		profile Profile
		event   apiv1.MeshOptimizationProfile
		version string
	}{
		{
			profile: ProfileDracoWebPV1,
			event:   apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1,
			version: PipelineVersionDracoWebPV1,
		},
		{
			profile: ProfileParticleMeshV1,
			event:   apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1,
			version: PipelineVersionParticleMeshV1,
		},
	} {
		if got := test.profile.EventValue(); got != test.event {
			t.Fatalf("event value for %q: want %v got %v", test.profile, test.event, got)
		}
		if got := test.profile.PipelineVersion(); got != test.version {
			t.Fatalf("pipeline version for %q: want %q got %q", test.profile, test.version, got)
		}
	}
	if got := Profile("unknown").EventValue(); got != apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED {
		t.Fatalf("unknown profile event value: %v", got)
	}
}

func TestBuildOptimizeArgs(t *testing.T) {
	tests := []struct {
		name               string
		targetRatioPercent int32
		want               []string
	}{
		{
			name:               "minimum target uses most permissive bounded error",
			targetRatioPercent: 1,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.01",
				"--simplify-error", "0.05",
			),
		},
		{
			name:               "very low target boundary keeps most permissive bounded error",
			targetRatioPercent: 5,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.05",
				"--simplify-error", "0.05",
			),
		},
		{
			name:               "single digit target uses low-ratio bounded error",
			targetRatioPercent: 9,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.09",
				"--simplify-error", "0.02",
			),
		},
		{
			name:               "ten percent target preserves existing bounded error",
			targetRatioPercent: 10,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.10",
				"--simplify-error", "0.01",
			),
		},
		{
			name:               "off step target uses exact requested ratio",
			targetRatioPercent: 15,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.15",
				"--simplify-error", "0.01",
			),
		},
		{
			name:               "low target boundary keeps existing bounded error",
			targetRatioPercent: 20,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.20",
				"--simplify-error", "0.01",
			),
		},
		{
			name:               "middle target uses moderate error",
			targetRatioPercent: 30,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.30",
				"--simplify-error", "0.005",
			),
		},
		{
			name:               "middle target boundary keeps moderate error",
			targetRatioPercent: 40,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.40",
				"--simplify-error", "0.005",
			),
		},
		{
			name:               "high target uses tight error",
			targetRatioPercent: 50,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.50",
				"--simplify-error", "0.001",
			),
		},
		{
			name:               "high target boundary keeps tight error",
			targetRatioPercent: 90,
			want: wantOptimizeArgs(
				"--simplify-ratio", "0.90",
				"--simplify-error", "0.001",
			),
		},
		{
			name:               "full target disables simplification",
			targetRatioPercent: 100,
			want: wantOptimizeArgs(
				"--simplify", "false",
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := BuildOptimizeArgs("in.glb", "out.glb", Options{TargetRatioPercent: tt.targetRatioPercent})
			if !reflect.DeepEqual(args, tt.want) {
				t.Fatalf("unexpected args:\nwant %#v\n got %#v", tt.want, args)
			}
		})
	}
}

func TestBuildParticleMeshArgs(t *testing.T) {
	if got, want := BuildParticleMeshArgs("in.glb", "out.glb", Options{TargetRatioPercent: 50}), []string{
		"in.glb", "out.glb",
		"--simplify-ratio", "0.50",
		"--simplify-error", "0.001",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("particle args:\nwant %#v\n got %#v", want, got)
	}
	if got, want := BuildParticleMeshArgs("in.glb", "out.glb", Options{TargetRatioPercent: 100}), []string{
		"in.glb", "out.glb", "--simplify", "false",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("particle 100 args:\nwant %#v\n got %#v", want, got)
	}
}
