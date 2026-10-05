package filemedia

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// HandleUploadSource downloads verified original bytes without creating a file
// or starting ingest processing. The browser owns processing and upload.
func (s *FileService) HandleUploadSource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	opts, err := parseUploadSourceQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	request, err := s.prepareRemoteImport(r.Context(), opts)
	if err != nil {
		writeUploadSourceError(w, err)
		return
	}
	source, err := s.openBrowserUploadSource(r.Context(), request)
	if err != nil {
		writeUploadSourceError(w, err)
		return
	}
	defer source.close()
	if int64(len(source.prefix)) > source.sourceMaxSize {
		http.Error(w, "Remote file exceeds maximum size", http.StatusBadRequest)
		return
	}
	if (source.response.ContentLength >= 0 && source.response.ContentLength < request.config.MinSize) ||
		(len(source.prefix) < remoteImportSniffBytes && int64(len(source.prefix)) < request.config.MinSize) {
		http.Error(w, "Remote file is below minimum size", http.StatusBadRequest)
		return
	}
	filename := canonicalRemoteImportFilename(remoteImportFileName(source.parsedURL), "source", source.detectedMime)
	w.Header().Set("Content-Type", source.detectedMime)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	if source.response.ContentLength >= 0 {
		size := strconv.FormatInt(source.response.ContentLength, 10)
		w.Header().Set("Content-Length", size)
		w.Header().Set("X-Upload-Source-Size", size)
	}
	reader := &uploadSourceContextReader{ctx: r.Context(), reader: io.MultiReader(bytes.NewReader(source.prefix), source.body)}
	// Bound the download before copying, then probe for excess without sending it.
	limited := &io.LimitedReader{R: reader, N: source.sourceMaxSize}
	written, err := io.CopyBuffer(w, limited, make([]byte, 64*1024))
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	var excess [1]byte
	n, err := reader.Read(excess[:])
	if n != 0 || err != io.EOF || written < request.config.MinSize || (source.response.ContentLength >= 0 && written != source.response.ContentLength) {
		// Headers may already have been sent. net/http closes/reset the stream so
		// callers cannot mistake a truncated or failed transfer for a complete file.
		panic(http.ErrAbortHandler)
	}
}

type uploadSourceContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *uploadSourceContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func parseUploadSourceQuery(rawQuery string) (remoteFileImportOptions, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return remoteFileImportOptions{}, fmt.Errorf("invalid query")
	}
	for key, values := range query {
		if len(values) != 1 {
			return remoteFileImportOptions{}, fmt.Errorf("duplicate query parameter: %s", key)
		}
	}
	value, err := strconv.ParseInt(query.Get("uploadType"), 10, 32)
	if err != nil {
		return remoteFileImportOptions{}, fmt.Errorf("invalid uploadType")
	}
	if _, ok := managev1.UploadType_name[int32(value)]; !ok || value == 0 {
		return remoteFileImportOptions{}, fmt.Errorf("invalid uploadType")
	}
	opts := remoteFileImportOptions{
		uploadType: managev1.UploadType(value), sourceURL: query.Get("url"),
		entityID: strings.TrimSpace(query.Get("entityId")), slotID: strings.TrimSpace(query.Get("slotId")),
		checkPermission: true,
		// Preparation validates ingest identities, but the relay never uses or
		// persists this per-request correlation identity.
		correlationID: uuid.NewString(),
	}
	if opts.sourceURL == "" {
		return opts, fmt.Errorf("url is required")
	}
	if opts.entityID != "" {
		if _, err := uuid.Parse(opts.entityID); err != nil {
			return opts, fmt.Errorf("invalid entityId")
		}
	}
	if query.Has("entityType") {
		value, err := strconv.ParseInt(query.Get("entityType"), 10, 32)
		if err != nil {
			return opts, fmt.Errorf("invalid entityType")
		}
		if _, ok := managev1.TranscodeEntityType_name[int32(value)]; !ok || value == 0 {
			return opts, fmt.Errorf("invalid entityType")
		}
		opts.transcodeEntityType = managev1.TranscodeEntityType(value)
		opts.entityType = opts.transcodeEntityType.String()
	}
	if (opts.uploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE || isEditorFileIngestUploadType(opts.uploadType)) && (query.Has("entityId") || query.Has("entityType")) {
		return opts, fmt.Errorf("File library upload must omit entityId and entityType")
	}
	if query.Has("expectedCurrentFileId") {
		expected := strings.TrimSpace(query.Get("expectedCurrentFileId"))
		opts.expectedCurrentFileID = &expected
	}
	return opts, nil
}

func writeUploadSourceError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		status = http.StatusBadRequest
	case connect.CodeUnauthenticated:
		status = http.StatusUnauthorized
	case connect.CodePermissionDenied:
		status = http.StatusForbidden
	case connect.CodeNotFound:
		status = http.StatusNotFound
	case connect.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	http.Error(w, http.StatusText(status), status)
}
