package optimizer

import (
	"fmt"
	"strconv"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	PipelineVersionDracoWebPV1    = "draco-webp-v1"
	PipelineVersionParticleMeshV1 = "particle-mesh-v1"
	TextureCompress               = "webp"
	TextureSize                   = "1024"
)

type Profile string

const (
	ProfileDracoWebPV1    Profile = PipelineVersionDracoWebPV1
	ProfileParticleMeshV1 Profile = PipelineVersionParticleMeshV1
)

type Options struct {
	TargetRatioPercent int32
	Profile            Profile
}

func OptionsFromEvent(options *apiv1.MeshOptimizationOptions) (Options, error) {
	if options == nil {
		return Options{}, fmt.Errorf("options are required")
	}
	if options.GetCompressionMethod() != apiv1.MeshOptimizationCompressionMethod_MESH_OPTIMIZATION_COMPRESSION_METHOD_DRACO {
		return Options{}, fmt.Errorf("compression_method must be DRACO")
	}
	targetRatioPercent := options.GetTargetRatioPercent()
	if !ValidTargetRatioPercent(targetRatioPercent) {
		return Options{}, fmt.Errorf("target_ratio_percent must be 1..100")
	}
	profile, err := profileFromEvent(options.GetProfile())
	if err != nil {
		return Options{}, err
	}
	return Options{TargetRatioPercent: targetRatioPercent, Profile: profile}, nil
}

func profileFromEvent(profile apiv1.MeshOptimizationProfile) (Profile, error) {
	switch profile {
	case apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1:
		return ProfileDracoWebPV1, nil
	case apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1:
		return ProfileParticleMeshV1, nil
	default:
		return "", fmt.Errorf("profile must be explicitly set to DRACO_WEBP_V1 or PARTICLE_MESH_V1")
	}
}

func (p Profile) EventValue() apiv1.MeshOptimizationProfile {
	switch p {
	case ProfileDracoWebPV1:
		return apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_DRACO_WEBP_V1
	case ProfileParticleMeshV1:
		return apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1
	default:
		return apiv1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED
	}
}

func (p Profile) PipelineVersion() string {
	return string(p)
}

func ValidTargetRatioPercent(targetRatioPercent int32) bool {
	return targetRatioPercent >= 1 && targetRatioPercent <= 100
}

func BuildOptimizeArgs(inputPath, outputPath string, options Options) []string {
	args := []string{
		"optimize",
		inputPath,
		outputPath,
		"--compress",
		"draco",
		"--texture-compress",
		TextureCompress,
		"--texture-size",
		TextureSize,
	}

	if options.TargetRatioPercent == 100 {
		return append(args, "--simplify", "false")
	}

	return append(args,
		"--simplify-ratio", simplifyRatioArg(options.TargetRatioPercent),
		"--simplify-error", simplifyErrorArg(options.TargetRatioPercent),
	)
}

func BuildParticleMeshArgs(inputPath, outputPath string, options Options) []string {
	args := []string{inputPath, outputPath}
	if options.TargetRatioPercent == 100 {
		return append(args, "--simplify", "false")
	}
	return append(args,
		"--simplify-ratio", simplifyRatioArg(options.TargetRatioPercent),
		"--simplify-error", simplifyErrorArg(options.TargetRatioPercent),
	)
}

func simplifyRatioArg(targetRatioPercent int32) string {
	value := float64(targetRatioPercent) / 100
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func simplifyErrorArg(targetRatioPercent int32) string {
	// glTF Transform's default error cap can stop simplification before the
	// requested ratio changes output geometry on real production models. Keep
	// existing 10%+ behavior stable, and loosen only for newly exposed 1..9%
	// targets where the caller is explicitly asking for much smaller geometry.
	switch {
	case targetRatioPercent <= 5:
		return "0.05"
	case targetRatioPercent < 10:
		return "0.02"
	case targetRatioPercent <= 20:
		return "0.01"
	case targetRatioPercent <= 40:
		return "0.005"
	default:
		return "0.001"
	}
}
