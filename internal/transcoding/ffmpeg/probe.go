package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

// ProbeResult contains media file metadata
type ProbeResult struct {
	DurationSeconds float64 `json:"duration_seconds"`
	Bitrate         int     `json:"bitrate"`
	Codec           string  `json:"codec"`
	SampleRate      int     `json:"sample_rate"`
	Channels        int     `json:"channels"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	FormatName      string  `json:"format_name"`
	HasAudio        bool    `json:"has_audio"`
	HasVideo        bool    `json:"has_video"`
}

type probeOutput struct {
	Format  probeFormat   `json:"format"`
	Streams []probeStream `json:"streams"`
}

type probeFormat struct {
	Duration   string `json:"duration"`
	BitRate    string `json:"bit_rate"`
	FormatName string `json:"format_name"`
}

type probeStream struct {
	CodecName  string `json:"codec_name"`
	SampleRate string `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

// Probe extracts metadata from a media file
func (e *Executor) Probe(ctx context.Context, inputPath string) (*ProbeResult, error) {
	cmd := exec.CommandContext(ctx, e.ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration,bit_rate,format_name:stream=codec_name,sample_rate,channels,width,height",
		"-of", "json",
		inputPath,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	return parseProbeOutput(output)
}

func parseProbeOutput(output []byte) (*ProbeResult, error) {
	var data probeOutput
	if err := json.Unmarshal(output, &data); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}
	result := &ProbeResult{
		FormatName: data.Format.FormatName,
	}
	result.DurationSeconds, _ = strconv.ParseFloat(data.Format.Duration, 64)
	result.Bitrate, _ = strconv.Atoi(data.Format.BitRate)
	for index, stream := range data.Streams {
		applyProbeStream(result, stream, index == 0)
	}
	return result, nil
}

func applyProbeStream(result *ProbeResult, stream probeStream, first bool) {
	isAudio, isVideo := classifyProbeStream(stream)
	result.HasAudio = result.HasAudio || isAudio
	result.HasVideo = result.HasVideo || isVideo
	if first {
		result.Codec = stream.CodecName
	}
	applyAudioProbe(result, stream, isAudio)
	applyVideoProbe(result, stream, isVideo)
}

func classifyProbeStream(stream probeStream) (bool, bool) {
	isAudio := stream.SampleRate != "" || stream.Channels > 0
	isVideo := stream.Width > 0 && stream.Height > 0
	return isAudio, isVideo
}

func applyAudioProbe(result *ProbeResult, stream probeStream, isAudio bool) {
	if !isAudio || result.SampleRate != 0 {
		return
	}
	result.Codec = stream.CodecName
	result.Channels = stream.Channels
	result.SampleRate, _ = strconv.Atoi(stream.SampleRate)
}

func applyVideoProbe(result *ProbeResult, stream probeStream, isVideo bool) {
	if !isVideo || result.Width != 0 || result.Height != 0 {
		return
	}
	result.Width = stream.Width
	result.Height = stream.Height
}
