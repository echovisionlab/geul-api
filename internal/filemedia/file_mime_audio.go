package filemedia

import (
	"bytes"
	"encoding/binary"
)

func detectISOBaseMediaMime(body []byte, allowedSet map[string]struct{}) string {
	if len(body) < 16 {
		return ""
	}

	limit := min(len(body), 64*1024)

	isMP4Brand := func(brand string) bool {
		switch brand {
		case "isom", "iso2", "mp41", "mp42", "avc1", "hvc1", "hev1", "mmp4", "M4V ", "MSNV":
			return true
		default:
			return false
		}
	}

	isAudioMP4Brand := func(brand string) bool {
		switch brand {
		case "M4A ", "M4B ", "M4P ", "M4R ", "f4a ", "F4A ":
			return true
		default:
			return false
		}
	}

	resolveBrand := func(brand string) string {
		if brand == "qt  " {
			if _, ok := allowedSet["video/quicktime"]; ok {
				return "video/quicktime"
			}
		}
		if isAudioMP4Brand(brand) {
			if _, ok := allowedSet["audio/mp4"]; ok {
				return "audio/mp4"
			}
		}
		if isMP4Brand(brand) {
			if _, ok := allowedSet["video/mp4"]; ok {
				return "video/mp4"
			}
		}
		return ""
	}

	for off := 4; off+12 <= limit; off++ {
		if !bytes.Equal(body[off:off+4], []byte("ftyp")) {
			continue
		}

		major := string(body[off+4 : off+8])
		if mime := resolveBrand(major); mime != "" {
			return mime
		}

		boxStart := off - 4
		boxSize := int(binary.BigEndian.Uint32(body[boxStart:off]))
		boxEnd := boxStart + boxSize
		if boxSize < 16 || boxEnd > len(body) {
			boxEnd = min(off+32, len(body))
		}

		for p := off + 8; p+4 <= boxEnd; p += 4 {
			if mime := resolveBrand(string(body[p : p+4])); mime != "" {
				return mime
			}
		}
	}

	return ""
}

func detectRIFFMime(body []byte, allowedSet map[string]struct{}) string {
	if len(body) < 12 {
		return ""
	}
	if !bytes.Equal(body[:4], []byte("RIFF")) {
		return ""
	}

	riffType := string(body[8:12])
	switch riffType {
	case "AVI ":
		if _, ok := allowedSet["video/x-msvideo"]; ok {
			return "video/x-msvideo"
		}
	case "WAVE":
		if _, ok := allowedSet["audio/wav"]; ok {
			return "audio/wav"
		}
	}

	return ""
}

func detectAIFFMime(body []byte, allowedSet map[string]struct{}) string {
	if _, ok := allowedSet["audio/aiff"]; !ok {
		return ""
	}
	if len(body) < 12 || !bytes.Equal(body[:4], []byte("FORM")) {
		return ""
	}

	formSize := uint64(binary.BigEndian.Uint32(body[4:8])) + 8
	if formSize < 12 || formSize < uint64(len(body)) {
		return ""
	}

	formType := string(body[8:12])
	if formType != "AIFF" && formType != "AIFC" {
		return ""
	}

	for offset := 12; offset+8 <= len(body); {
		chunkSize := uint64(binary.BigEndian.Uint32(body[offset+4 : offset+8]))
		chunkDataStart := uint64(offset + 8)
		chunkDataEnd := chunkDataStart + chunkSize
		if chunkDataEnd > uint64(len(body)) || chunkDataEnd > formSize {
			return ""
		}

		if bytes.Equal(body[offset:offset+4], []byte("COMM")) {
			comm := body[chunkDataStart:chunkDataEnd]
			if validAIFFCommonChunk(comm, formType) {
				return "audio/aiff"
			}
			return ""
		}

		nextOffset := chunkDataEnd + chunkSize%2
		if nextOffset > uint64(len(body)) {
			return ""
		}
		offset = int(nextOffset)
	}

	return ""
}

func validAIFFCommonChunk(chunk []byte, formType string) bool {
	minimumSize := 18
	if formType == "AIFC" {
		minimumSize = 22
	}
	if len(chunk) < minimumSize {
		return false
	}
	sampleRateExponent := binary.BigEndian.Uint16(chunk[8:10])
	if binary.BigEndian.Uint16(chunk[0:2]) == 0 ||
		binary.BigEndian.Uint32(chunk[2:6]) == 0 ||
		binary.BigEndian.Uint16(chunk[6:8]) == 0 ||
		sampleRateExponent == 0 ||
		sampleRateExponent&0x8000 != 0 ||
		sampleRateExponent == 0x7fff ||
		chunk[10]&0x80 == 0 {
		return false
	}
	if formType != "AIFC" {
		return true
	}
	for _, part := range chunk[18:22] {
		if part < 0x20 || part > 0x7e {
			return false
		}
	}
	return true
}

func detectFLACMime(body []byte, allowedSet map[string]struct{}) string {
	if _, ok := allowedSet["audio/flac"]; !ok {
		return ""
	}
	if len(body) < 42 || !bytes.Equal(body[:4], []byte("fLaC")) {
		return ""
	}

	firstMetadataType := body[4] & 0x7f
	firstMetadataLength := int(body[5])<<16 | int(body[6])<<8 | int(body[7])
	if firstMetadataType != 0 || firstMetadataLength != 34 {
		return ""
	}

	streamInfo := body[8:42]
	minimumBlockSize := binary.BigEndian.Uint16(streamInfo[0:2])
	maximumBlockSize := binary.BigEndian.Uint16(streamInfo[2:4])
	minimumFrameSize := int(streamInfo[4])<<16 | int(streamInfo[5])<<8 | int(streamInfo[6])
	maximumFrameSize := int(streamInfo[7])<<16 | int(streamInfo[8])<<8 | int(streamInfo[9])
	sampleDescription := binary.BigEndian.Uint64(streamInfo[10:18])
	sampleRate := (sampleDescription >> 44) & 0xfffff
	bitsPerSample := ((sampleDescription >> 36) & 0x1f) + 1
	if minimumBlockSize < 16 ||
		maximumBlockSize < minimumBlockSize ||
		minimumFrameSize != 0 && maximumFrameSize != 0 && maximumFrameSize < minimumFrameSize ||
		sampleRate == 0 ||
		bitsPerSample < 4 {
		return ""
	}

	return "audio/flac"
}

type bitReader struct {
	data   []byte
	bitPos int
}

func (r *bitReader) readBits(count int) (uint64, bool) {
	if count < 0 || count > 64 || count > len(r.data)*8-r.bitPos {
		return 0, false
	}

	var value uint64
	for range count {
		byteIndex := r.bitPos / 8
		bitIndex := 7 - r.bitPos%8
		value = value<<1 | uint64((r.data[byteIndex]>>bitIndex)&1)
		r.bitPos++
	}
	return value, true
}

func (r *bitReader) skipBits(count int) bool {
	_, ok := r.readBits(count)
	return ok
}

func (r *bitReader) alignToByte() bool {
	padding := (8 - r.bitPos%8) % 8
	return r.skipBits(padding)
}

func (r *bitReader) remainingBits() int {
	return len(r.data)*8 - r.bitPos
}

func parseAACProgramConfigElement(reader *bitReader) bool {
	if !reader.skipBits(4) || !reader.skipBits(2) {
		return false
	}
	sampleRateIndex, ok := reader.readBits(4)
	if !ok || sampleRateIndex >= 13 {
		return false
	}

	frontCount, ok := reader.readBits(4)
	if !ok {
		return false
	}
	sideCount, ok := reader.readBits(4)
	if !ok {
		return false
	}
	backCount, ok := reader.readBits(4)
	if !ok {
		return false
	}
	lfeCount, ok := reader.readBits(2)
	if !ok {
		return false
	}
	assocDataCount, ok := reader.readBits(3)
	if !ok {
		return false
	}
	validCCCount, ok := reader.readBits(4)
	if !ok || frontCount+sideCount+backCount+lfeCount == 0 {
		return false
	}

	monoMixdownPresent, ok := reader.readBits(1)
	if !ok || monoMixdownPresent == 1 && !reader.skipBits(4) {
		return false
	}
	stereoMixdownPresent, ok := reader.readBits(1)
	if !ok || stereoMixdownPresent == 1 && !reader.skipBits(4) {
		return false
	}
	matrixMixdownPresent, ok := reader.readBits(1)
	if !ok || matrixMixdownPresent == 1 && !reader.skipBits(3) {
		return false
	}

	channelElementCount := int(frontCount + sideCount + backCount)
	if !reader.skipBits(channelElementCount*5) ||
		!reader.skipBits(int(lfeCount)*4) ||
		!reader.skipBits(int(assocDataCount)*4) ||
		!reader.skipBits(int(validCCCount)*5) ||
		!reader.alignToByte() {
		return false
	}

	commentLength, ok := reader.readBits(8)
	return ok && reader.skipBits(int(commentLength)*8)
}

func detectADIFMime(body []byte) bool {
	if len(body) < 5 || !bytes.Equal(body[:4], []byte("ADIF")) {
		return false
	}

	reader := bitReader{data: body[4:]}
	copyrightPresent, ok := reader.readBits(1)
	if !ok || copyrightPresent == 1 && !reader.skipBits(72) {
		return false
	}
	if !reader.skipBits(2) {
		return false
	}
	bitstreamType, ok := reader.readBits(1)
	if !ok || !reader.skipBits(23) {
		return false
	}
	programConfigCountMinusOne, ok := reader.readBits(4)
	if !ok {
		return false
	}

	for range int(programConfigCountMinusOne) + 1 {
		if bitstreamType == 0 && !reader.skipBits(20) {
			return false
		}
		if !parseAACProgramConfigElement(&reader) {
			return false
		}
	}

	return reader.remainingBits() >= 8
}

type adtsHeader struct {
	frameLength     int
	mpegVersion     byte
	profile         byte
	sampleRateIndex byte
	channelConfig   byte
}

func parseADTSHeader(body []byte, offset int) (adtsHeader, bool) {
	if offset < 0 || offset+7 > len(body) {
		return adtsHeader{}, false
	}
	header := body[offset : offset+7]
	if header[0] != 0xff || header[1]&0xf6 != 0xf0 {
		return adtsHeader{}, false
	}

	sampleRateIndex := (header[2] >> 2) & 0x0f
	if sampleRateIndex >= 13 {
		return adtsHeader{}, false
	}

	headerLength := 7
	if header[1]&1 == 0 {
		headerLength = 9
	}
	frameLength := int(header[3]&0x03)<<11 | int(header[4])<<3 | int(header[5]>>5)
	if frameLength <= headerLength || offset+frameLength > len(body) {
		return adtsHeader{}, false
	}

	return adtsHeader{
		frameLength:     frameLength,
		mpegVersion:     (header[1] >> 3) & 1,
		profile:         header[2] >> 6,
		sampleRateIndex: sampleRateIndex,
		channelConfig:   (header[2]&1)<<2 | header[3]>>6,
	}, true
}

func detectADTSMime(body []byte) bool {
	first, ok := parseADTSHeader(body, 0)
	if !ok {
		return false
	}
	second, ok := parseADTSHeader(body, first.frameLength)
	if !ok {
		return false
	}

	return first.mpegVersion == second.mpegVersion &&
		first.profile == second.profile &&
		first.sampleRateIndex == second.sampleRateIndex &&
		first.channelConfig == second.channelConfig
}

func detectAACMime(body []byte, allowedSet map[string]struct{}) string {
	if _, ok := allowedSet["audio/aac"]; !ok {
		return ""
	}
	if !detectADTSMime(body) && !detectADIFMime(body) {
		return ""
	}
	return "audio/aac"
}

func detectMPEGAudioMime(body []byte, allowedSet map[string]struct{}) string {
	if _, ok := allowedSet["audio/mpeg"]; !ok {
		return ""
	}

	offset := 0
	if len(body) >= 10 && bytes.Equal(body[:3], []byte("ID3")) {
		tagSize := int(body[6]&0x7f)<<21 |
			int(body[7]&0x7f)<<14 |
			int(body[8]&0x7f)<<7 |
			int(body[9]&0x7f)
		offset = 10 + tagSize
	}
	firstLength, ok := mpegLayerIIIFrameLength(body, offset)
	if !ok {
		return ""
	}
	if _, ok := mpegLayerIIIFrameLength(body, offset+firstLength); !ok {
		return ""
	}
	return "audio/mpeg"
}

func mpegLayerIIIFrameLength(body []byte, offset int) (int, bool) {
	if offset < 0 || offset+4 > len(body) {
		return 0, false
	}
	header := binary.BigEndian.Uint32(body[offset : offset+4])
	if header&0xffe00000 != 0xffe00000 {
		return 0, false
	}
	version := int((header >> 19) & 0x3)
	layer := int((header >> 17) & 0x3)
	bitrateIndex := int((header >> 12) & 0xf)
	sampleRateIndex := int((header >> 10) & 0x3)
	if version == 1 || layer != 1 || bitrateIndex == 0 || bitrateIndex == 15 || sampleRateIndex == 3 {
		return 0, false
	}

	bitrateV1 := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	bitrateV2 := [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	sampleRates := [...]int{44100, 48000, 32000}
	bitrate := bitrateV2[bitrateIndex]
	sampleRate := sampleRates[sampleRateIndex]
	coefficient := 72
	switch version {
	case 3:
		bitrate = bitrateV1[bitrateIndex]
		coefficient = 144
	case 2:
		sampleRate /= 2
	case 0:
		sampleRate /= 4
	}
	padding := int((header >> 9) & 0x1)
	frameLength := coefficient*bitrate*1000/sampleRate + padding
	return frameLength, frameLength > 4 && offset+frameLength <= len(body)
}
