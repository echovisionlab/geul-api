package email

import (
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// LayoutMarkerPresence reports whether a stored wrapper contains structural
// and explicit-value marker classes. Malformed private markers fail closed.
func LayoutMarkerPresence(content string) (bool, bool, error) {
	tokenizer := html.NewTokenizer(strings.NewReader(content))
	unitFound := false
	valueFound := false
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			if errors.Is(tokenizer.Err(), io.EOF) {
				return unitFound, valueFound, nil
			}
			return false, false, tokenizer.Err()
		}
		if tokenType != html.CommentToken {
			continue
		}
		data := tokenizer.Token().Data
		if data == layoutOverlayMarker {
			valueFound = true
			continue
		}
		_, unit, err := parseLayoutUnitMarker(data)
		if err != nil {
			return false, false, err
		}
		_, value, err := parseLayoutValueMarker(data)
		if err != nil {
			return false, false, err
		}
		unitFound = unitFound || unit
		valueFound = valueFound || value
	}
}
