package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// BenchmarkToolsCallResponse includes request parsing, catalog validation,
// dispatch, complete RPC encoding, and response writes. Fixtures, request
// construction, and response-byte checks stay outside the measured loop.
func BenchmarkToolsCallResponse(b *testing.B) {
	for _, fixture := range []struct {
		name   string
		blocks int
	}{
		{name: "Small", blocks: 1},
		{name: "Large", blocks: 1024},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			blocks := make([]map[string]any, fixture.blocks)
			for index := range blocks {
				blocks[index] = map[string]any{
					"h": fmt.Sprintf("block-%d", index), "r": "block-revision",
					"text": strings.Repeat("paragraph text ", 32),
				}
			}
			structured := map[string]any{"dr": "document-revision", "blocks": blocks}
			text, err := json.Marshal(structured)
			if err != nil {
				b.Fatal(err)
			}
			result := ToolResult{Content: []ContentBlock{TextContent(string(text))}, StructuredContent: structured}
			expected, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`5`), Result: result})
			if err != nil {
				b.Fatal(err)
			}
			expected = append(expected, '\n')
			tool := validTestTool("document_read", `{"type":"object"}`)
			handler, err := NewHandler(Config{
				Registry: registryFunc(func(context.Context, Principal) ([]Tool, error) { return []Tool{tool}, nil }),
				Dispatcher: dispatcherFunc(func(context.Context, Principal, string, ToolArguments) (ToolResult, error) {
					return result, nil
				}),
				ServerInfo: Implementation{Name: "geul", Version: "benchmark"},
			})
			if err != nil {
				b.Fatal(err)
			}
			const body = `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"document_read","arguments":{}}}`
			request := rpcRequest(body)
			reader := strings.NewReader(body)
			request.Body = io.NopCloser(reader)
			response := &benchmarkResponseWriter{header: make(http.Header)}
			checkResponse := func() {
				b.Helper()
				if response.status != http.StatusOK || !bytes.Equal(response.body.Bytes(), expected) ||
					response.header.Get("Cache-Control") != "no-store" || response.header.Get("Content-Type") != "application/json" {
					b.Fatal("tools/call response bytes, status, or headers changed")
				}
			}
			handler.ServeHTTP(response, request)
			checkResponse()
			b.Logf("response_bytes=%d sha256=%x", len(expected), sha256.Sum256(expected))
			b.SetBytes(int64(len(expected)))
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				reader.Reset(body)
				response.body.Reset()
				clear(response.header)
				response.status = 0
				handler.ServeHTTP(response, request)
			}
			b.StopTimer()
			checkResponse()
		})
	}
}

type benchmarkResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (writer *benchmarkResponseWriter) Header() http.Header    { return writer.header }
func (writer *benchmarkResponseWriter) WriteHeader(status int) { writer.status = status }
func (writer *benchmarkResponseWriter) Write(data []byte) (int, error) {
	return writer.body.Write(data)
}
