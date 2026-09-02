package authentication

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// kratosFlowTransport is the single request clone, body, capture, header, and
// response-copy boundary used by public authentication flow orchestration.
type kratosFlowTransport struct {
	handler http.Handler
}

func newKratosFlowTransport(handler http.Handler) *kratosFlowTransport {
	return &kratosFlowTransport{handler: handler}
}

func (t *kratosFlowTransport) forward(
	w http.ResponseWriter,
	request *http.Request,
	method string,
	path string,
	query url.Values,
	body []byte,
	acceptJSON bool,
) {
	forwardRequest := request.Clone(request.Context())
	forwardRequest.Method = method
	forwardRequest.URL = &url.URL{Path: path, RawQuery: query.Encode()}
	forwardRequest.RequestURI = ""
	forwardRequest.Header = request.Header.Clone()
	forwardRequest.Header.Del("Accept-Encoding")
	if acceptJSON {
		forwardRequest.Header.Set("Accept", "application/json")
	}
	if body == nil {
		forwardRequest.Body = nil
		forwardRequest.ContentLength = 0
	} else {
		forwardRequest.Body = io.NopCloser(bytes.NewReader(body))
		forwardRequest.ContentLength = int64(len(body))
	}
	t.handler.ServeHTTP(w, forwardRequest)
}

func (t *kratosFlowTransport) capture(
	request *http.Request,
	method string,
	path string,
	query url.Values,
	body []byte,
	acceptJSON bool,
) bufferedKratosResponse {
	capture := newKratosResponseCapture(nil, 0)
	t.forward(capture, request, method, path, query, body, acceptJSON)
	return bufferedKratosResponse{
		StatusCode: capture.statusCode(),
		Header:     capture.Header().Clone(),
		Body:       append([]byte(nil), capture.body.Bytes()...),
	}
}

func (t *kratosFlowTransport) copy(
	w http.ResponseWriter,
	response bufferedKratosResponse,
	project func([]byte) []byte,
) {
	copyBufferedKratosResponse(w, response, project)
}

func copyBufferedKratosResponse(
	w http.ResponseWriter,
	response bufferedKratosResponse,
	project func([]byte) []byte,
) {
	body := response.Body
	if project != nil {
		body = project(body)
	}
	for key, values := range response.Header {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

type bufferedKratosResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type kratosProxyResponseWriter struct {
	downstream   http.ResponseWriter
	header       http.Header
	status       int
	body         bytes.Buffer
	maxBodyBytes int
}

func newKratosResponseCapture(
	downstream http.ResponseWriter,
	maxBodyBytes int,
) *kratosProxyResponseWriter {
	return &kratosProxyResponseWriter{
		downstream:   downstream,
		header:       make(http.Header),
		maxBodyBytes: maxBodyBytes,
	}
}

func (w *kratosProxyResponseWriter) Header() http.Header {
	if w.downstream != nil {
		return w.downstream.Header()
	}
	return w.header
}

func (w *kratosProxyResponseWriter) Unwrap() http.ResponseWriter {
	return w.downstream
}

func (w *kratosProxyResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if w.downstream != nil {
		w.downstream.WriteHeader(status)
	}
}

func (w *kratosProxyResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.maxBodyBytes <= 0 {
		_, _ = w.body.Write(body)
	} else if w.body.Len() < w.maxBodyBytes {
		remaining := w.maxBodyBytes - w.body.Len()
		_, _ = w.body.Write(body[:min(len(body), remaining)])
	}
	if w.downstream != nil {
		return w.downstream.Write(body)
	}
	return len(body), nil
}

func (w *kratosProxyResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
