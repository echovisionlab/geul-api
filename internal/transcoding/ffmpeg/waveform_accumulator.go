package ffmpeg

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

type waveformBucketRange struct {
	start int
	end   int
}

type waveformAccumulator struct {
	frameSize   int
	ranges      []waveformBucketRange
	peaks       [][]float64
	precision   int
	leftover    []byte
	active      []int
	nextBucket  int
	frameNumber int
}

func buildWaveformPeaksFromPCMReader(
	source io.Reader,
	channels int,
	totalFrames int,
	maxLength int,
	precision int,
) ([][]float64, error) {
	accumulator, err := newWaveformAccumulator(channels, totalFrames, maxLength, precision)
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReaderSize(source, accumulator.frameSize*4096)
	buffer := make([]byte, accumulator.frameSize*4096)
	for {
		bytesRead, readErr := reader.Read(buffer)
		if bytesRead > 0 {
			accumulator.consume(buffer[:bytesRead])
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		return nil, fmt.Errorf("read waveform pcm: %w", readErr)
	}
	return accumulator.result()
}

func newWaveformAccumulator(
	channels int,
	totalFrames int,
	maxLength int,
	precision int,
) (*waveformAccumulator, error) {
	frameSize := channels * 2
	if frameSize <= 0 {
		return nil, fmt.Errorf("invalid waveform channel count")
	}
	if totalFrames <= 0 {
		return nil, fmt.Errorf("waveform pcm has no frames")
	}
	if maxLength <= 0 || precision <= 0 {
		return nil, fmt.Errorf("waveform length and precision must be positive")
	}

	peaks := make([][]float64, channels)
	for channel := range peaks {
		peaks[channel] = make([]float64, maxLength)
	}
	return &waveformAccumulator{
		frameSize: frameSize,
		ranges:    buildWaveformBucketRanges(totalFrames, maxLength),
		peaks:     peaks,
		precision: precision,
		leftover:  make([]byte, 0, frameSize),
		active:    make([]int, 0, 8),
	}, nil
}

func (a *waveformAccumulator) consume(chunk []byte) {
	data := make([]byte, 0, len(a.leftover)+len(chunk))
	data = append(data, a.leftover...)
	data = append(data, chunk...)
	completeBytes := len(data) - (len(data) % a.frameSize)
	for offset := 0; offset < completeBytes; offset += a.frameSize {
		a.consumeFrame(data[offset : offset+a.frameSize])
	}
	a.leftover = append(a.leftover[:0], data[completeBytes:]...)
}

func (a *waveformAccumulator) consumeFrame(samples []byte) {
	for a.nextBucket < len(a.ranges) && a.ranges[a.nextBucket].start <= a.frameNumber {
		a.active = append(a.active, a.nextBucket)
		a.nextBucket++
	}

	stillActive := a.active[:0]
	for _, bucket := range a.active {
		if a.frameNumber >= a.ranges[bucket].end {
			continue
		}
		a.capturePeaks(samples, bucket)
		stillActive = append(stillActive, bucket)
	}
	a.active = stillActive
	a.frameNumber++
}

func (a *waveformAccumulator) capturePeaks(samples []byte, bucket int) {
	for channel := range a.peaks {
		sample := int16(binary.LittleEndian.Uint16(samples[channel*2 : channel*2+2]))
		value := float64(sample) / 32768.0
		if math.Abs(value) > math.Abs(a.peaks[channel][bucket]) {
			a.peaks[channel][bucket] = value
		}
	}
}

func (a *waveformAccumulator) result() ([][]float64, error) {
	if a.frameNumber == 0 {
		return nil, fmt.Errorf("waveform pcm is empty")
	}
	if len(a.leftover) != 0 {
		return nil, fmt.Errorf("waveform pcm ended with incomplete frame")
	}
	for channel := range a.peaks {
		for index, value := range a.peaks[channel] {
			a.peaks[channel][index] = math.Round(value*float64(a.precision)) / float64(a.precision)
		}
	}
	return a.peaks, nil
}

func buildWaveformBucketRanges(totalFrames int, maxLength int) []waveformBucketRange {
	if totalFrames <= 0 || maxLength <= 0 {
		return nil
	}

	sampleSize := float64(totalFrames) / float64(maxLength)
	ranges := make([]waveformBucketRange, maxLength)
	for i := 0; i < maxLength; i++ {
		start := int(math.Floor(float64(i) * sampleSize))
		end := int(math.Ceil(float64(i+1) * sampleSize))
		ranges[i] = waveformBucketRange{start: start, end: end}
	}
	return ranges
}
