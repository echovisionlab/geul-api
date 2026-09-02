package filemedia

import (
	"bytes"
	"strings"
)

const (
	ebmlHeaderElementID     = 0x1a45dfa3
	ebmlDocTypeElementID    = 0x4282
	ebmlSegmentElementID    = 0x18538067
	ebmlTracksElementID     = 0x1654ae6b
	ebmlTrackEntryElementID = 0xae
	ebmlTrackTypeElementID  = 0x83

	ebmlTrackTypeVideo = 1
	ebmlTrackTypeAudio = 2
)

type ebmlElement struct {
	id       uint64
	data     []byte
	complete bool
}

func readEBMLVInt(body []byte, keepLengthMarker bool, maxLength int) (uint64, int, bool, bool) {
	if len(body) == 0 || body[0] == 0 {
		return 0, 0, false, false
	}

	length := 1
	marker := byte(0x80)
	for body[0]&marker == 0 {
		length++
		marker >>= 1
	}
	if length > maxLength || length > len(body) {
		return 0, 0, false, false
	}

	value := uint64(body[0])
	if !keepLengthMarker {
		value = uint64(body[0] &^ marker)
	}
	for i := 1; i < length; i++ {
		value = value<<8 | uint64(body[i])
	}

	unknown := false
	if !keepLengthMarker {
		maxValue := uint64(1)<<(7*length) - 1
		unknown = value == maxValue
	}
	return value, length, unknown, true
}

func readEBMLElement(body []byte) (ebmlElement, int, bool) {
	id, idLength, _, ok := readEBMLVInt(body, true, 4)
	if !ok {
		return ebmlElement{}, 0, false
	}
	size, sizeLength, unknownSize, ok := readEBMLVInt(body[idLength:], false, 8)
	if !ok {
		return ebmlElement{}, 0, false
	}

	dataStart := idLength + sizeLength
	if unknownSize {
		return ebmlElement{id: id, data: body[dataStart:], complete: false}, len(body), true
	}
	if size > uint64(len(body)-dataStart) {
		return ebmlElement{id: id, data: body[dataStart:], complete: false}, len(body), true
	}

	totalLength := dataStart + int(size)
	return ebmlElement{id: id, data: body[dataStart:totalLength], complete: true}, totalLength, true
}

func findEBMLChild(body []byte, wantedID uint64) (ebmlElement, bool) {
	for offset := 0; offset < len(body); {
		element, length, ok := readEBMLElement(body[offset:])
		if !ok {
			return ebmlElement{}, false
		}
		if element.id == wantedID {
			return element, true
		}
		if !element.complete || length <= 0 {
			return ebmlElement{}, false
		}
		offset += length
	}
	return ebmlElement{}, false
}

func parseEBMLUnsignedInteger(body []byte) (uint64, bool) {
	if len(body) == 0 || len(body) > 8 {
		return 0, false
	}
	var value uint64
	for _, part := range body {
		value = value<<8 | uint64(part)
	}
	return value, true
}

func detectWebMTrackTypes(tracks []byte) (hasAudio bool, hasVideo bool, ok bool) {
	foundTrack := false
	for offset := 0; offset < len(tracks); {
		entry, length, parsed := readEBMLElement(tracks[offset:])
		if !parsed {
			return false, false, false
		}
		if entry.id == ebmlTrackEntryElementID {
			if !entry.complete {
				return false, false, false
			}
			trackTypeElement, found := findEBMLChild(entry.data, ebmlTrackTypeElementID)
			if !found || !trackTypeElement.complete {
				return false, false, false
			}
			trackType, valid := parseEBMLUnsignedInteger(trackTypeElement.data)
			if !valid {
				return false, false, false
			}
			foundTrack = true
			switch trackType {
			case ebmlTrackTypeAudio:
				hasAudio = true
			case ebmlTrackTypeVideo:
				hasVideo = true
			}
		}
		if !entry.complete || length <= 0 {
			return false, false, false
		}
		offset += length
	}
	return hasAudio, hasVideo, foundTrack
}

func parseMatroskaPrefix(body []byte) (docType string, tracks []byte, ok bool) {
	if len(body) > multipartSniffBytes {
		body = body[:multipartSniffBytes]
	}

	header, headerLength, parsed := readEBMLElement(body)
	if !parsed || header.id != ebmlHeaderElementID || !header.complete {
		return "", nil, false
	}
	docTypeElement, found := findEBMLChild(header.data, ebmlDocTypeElementID)
	if !found || !docTypeElement.complete || len(docTypeElement.data) == 0 {
		return "", nil, false
	}
	docType = strings.ToLower(string(docTypeElement.data))

	segment, _, parsed := readEBMLElement(body[headerLength:])
	if !parsed || segment.id != ebmlSegmentElementID {
		return docType, nil, true
	}
	tracksElement, found := findEBMLChild(segment.data, ebmlTracksElementID)
	if !found || !tracksElement.complete {
		return docType, nil, true
	}
	return docType, tracksElement.data, true
}

func detectMatroskaMime(body []byte, allowedSet map[string]struct{}) string {
	if len(body) < 4 || !bytes.Equal(body[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return ""
	}

	docType, tracks, parsed := parseMatroskaPrefix(body)
	if !parsed || docType != "webm" {
		if _, ok := allowedSet["video/x-matroska"]; ok {
			return "video/x-matroska"
		}
		return ""
	}

	hasAudio, hasVideo, validTracks := detectWebMTrackTypes(tracks)
	if !validTracks {
		if _, ok := allowedSet["video/webm"]; ok {
			return "video/webm"
		}
		return ""
	}
	if hasVideo {
		if _, ok := allowedSet["video/webm"]; ok {
			return "video/webm"
		}
		return ""
	}
	if hasAudio {
		if _, ok := allowedSet["audio/webm"]; ok {
			return "audio/webm"
		}
	}
	return ""
}
