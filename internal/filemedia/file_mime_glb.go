package filemedia

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

func parseGLBHeader(body []byte) (totalLength uint32, firstChunkLength uint32, ok bool) {
	if len(body) < 20 || !bytes.Equal(body[:4], []byte("glTF")) {
		return 0, 0, false
	}

	version := binary.LittleEndian.Uint32(body[4:8])
	totalLength = binary.LittleEndian.Uint32(body[8:12])
	firstChunkLength = binary.LittleEndian.Uint32(body[12:16])
	if version != 2 || totalLength < 20 || firstChunkLength == 0 {
		return 0, 0, false
	}
	if uint64(totalLength) < uint64(len(body)) ||
		uint64(totalLength) < uint64(20)+uint64(firstChunkLength) {
		return 0, 0, false
	}
	if !bytes.Equal(body[16:20], []byte("JSON")) {
		return 0, 0, false
	}

	return totalLength, firstChunkLength, true
}

func validateGLBUploadSize(body []byte, expectedSize int64) error {
	if expectedSize <= 0 {
		return errs.InvalidArgument("file_size", "GLB upload size is required")
	}
	totalLength, _, ok := parseGLBHeader(body)
	if !ok {
		return nil
	}
	if uint64(totalLength) != uint64(expectedSize) {
		return errs.InvalidArgument(
			"file_size",
			fmt.Sprintf("GLB total length %d does not match upload size %d", totalLength, expectedSize),
		)
	}
	return nil
}

func detectGLBMime(body []byte, allowedSet map[string]struct{}) string {
	if _, ok := allowedSet["model/gltf-binary"]; !ok {
		return ""
	}

	_, firstChunkLength, ok := parseGLBHeader(body)
	if !ok {
		return ""
	}
	jsonEnd := 20 + int(firstChunkLength)
	if jsonEnd <= len(body) && !json.Valid(body[20:jsonEnd]) {
		return ""
	}

	return "model/gltf-binary"
}
