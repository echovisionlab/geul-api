package hls

import (
	"bufio"
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

type hlsPlaylistParser struct {
	sawHeader                  bool
	mediaPlaylist              bool
	multivariantPlaylist       bool
	endList                    bool
	expectedReferenceExtension string
	references                 []string
}

func parseHLSPlaylist(data []byte) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	parser := hlsPlaylistParser{}
	for scanner.Scan() {
		if err := parser.consume(strings.TrimSpace(scanner.Text())); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := parser.validate(); err != nil {
		return nil, err
	}
	return parser.references, nil
}

func (p *hlsPlaylistParser) consume(line string) error {
	if line == "" {
		return nil
	}
	if !p.sawHeader {
		if line != "#EXTM3U" {
			return fmt.Errorf("missing #EXTM3U header")
		}
		p.sawHeader = true
		return nil
	}

	switch {
	case strings.HasPrefix(line, "#EXTINF:"):
		return p.expectMediaReference()
	case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
		return p.expectVariantReference()
	case line == "#EXT-X-ENDLIST":
		p.endList = true
		return nil
	case strings.HasPrefix(line, "#"):
		if strings.Contains(strings.ToUpper(line), "URI=") {
			return fmt.Errorf("URI-bearing HLS tags are not supported")
		}
		return nil
	default:
		return p.addReference(line)
	}
}

func (p *hlsPlaylistParser) expectMediaReference() error {
	if p.multivariantPlaylist {
		return fmt.Errorf("playlist mixes media segments and variants")
	}
	p.mediaPlaylist = true
	return p.expectReference(".ts")
}

func (p *hlsPlaylistParser) expectVariantReference() error {
	if p.mediaPlaylist {
		return fmt.Errorf("playlist mixes media segments and variants")
	}
	p.multivariantPlaylist = true
	return p.expectReference(".m3u8")
}

func (p *hlsPlaylistParser) expectReference(extension string) error {
	if p.expectedReferenceExtension != "" {
		return fmt.Errorf("missing URI after HLS entry tag")
	}
	p.expectedReferenceExtension = extension
	return nil
}

func (p *hlsPlaylistParser) addReference(reference string) error {
	if reference != filepath.Base(reference) ||
		strings.ContainsAny(reference, "?#\\\\") ||
		strings.Contains(reference, ":") {
		return fmt.Errorf("non-local HLS reference %q", reference)
	}
	if p.expectedReferenceExtension == "" {
		return fmt.Errorf("HLS reference %q has no entry tag", reference)
	}
	if strings.ToLower(filepath.Ext(reference)) != p.expectedReferenceExtension {
		return fmt.Errorf("unsupported HLS reference %q", reference)
	}
	p.references = append(p.references, reference)
	p.expectedReferenceExtension = ""
	return nil
}

func (p *hlsPlaylistParser) validate() error {
	if !p.sawHeader {
		return fmt.Errorf("empty playlist")
	}
	if p.expectedReferenceExtension != "" {
		return fmt.Errorf("missing URI after HLS entry tag")
	}
	if !p.mediaPlaylist && !p.multivariantPlaylist {
		return fmt.Errorf("playlist has no media entries")
	}
	if p.mediaPlaylist && !p.endList {
		return fmt.Errorf("VOD media playlist is missing #EXT-X-ENDLIST")
	}
	if p.multivariantPlaylist && p.endList {
		return fmt.Errorf("multivariant playlist must not contain #EXT-X-ENDLIST")
	}
	return nil
}
