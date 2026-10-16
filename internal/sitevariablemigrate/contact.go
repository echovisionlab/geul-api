package sitevariablemigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var contactTokens = strings.NewReplacer("{{contact.supportEmail}}", "{{contact.email}}", "{{contact.privacyEmail}}", "{{contact.email}}")
var contactEmailTokens = regexp.MustCompile(`(?i)\{\s*\{\s*contact\.(?:supportemail|privacyemail)\s*\}\s*\}`)

// ContactText replaces exact reserved tokens without interpreting setting values
// or changing unrelated branding, addresses, template variables or HTML markers.
func ContactText(source string) string { return contactTokens.Replace(source) }

// Email historically accepts case-folded keys and formatter whitespace.
func ContactEmailText(source string) string {
	return contactEmailTokens.ReplaceAllString(source, "{{contact.email}}")
}

func hasContactTokensJSON(source []byte) (bool, error) {
	var value any
	if err := json.Unmarshal(source, &value); err != nil {
		return false, err
	}
	var has func(any) bool
	has = func(value any) bool {
		switch node := value.(type) {
		case string:
			return ContactEmailText(node) != node
		case []any:
			for _, item := range node {
				if has(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range node {
				if has(item) {
					return true
				}
			}
		}
		return false
	}
	return has(value), nil
}

func ContactRichJSON(source []byte) ([]byte, error) {
	return contactJSON(source, false)
}

func ContactMenuJSON(source []byte) ([]byte, error) {
	return contactJSON(source, true)
}

func contactJSON(source []byte, menu bool) ([]byte, error) {
	if !json.Valid(source) {
		return nil, fmt.Errorf("invalid contact JSON")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, item := range node {
				walk(item)
			}
		case map[string]any:
			if variable, ok := node["siteVariable"].(map[string]any); ok {
				if key := variable["key"]; key == "contact.supportEmail" || key == "contact.privacyEmail" {
					variable["key"] = "contact.email"
				}
			}
			for key, item := range node {
				// Rich payloads can contain executable source and metadata. Limit
				// text replacement to authored text and link hrefs; menu labels use label.
				if text, ok := item.(string); ok && (key == "text" || key == "href" || (menu && key == "label")) {
					node[key] = ContactText(text)
				} else {
					walk(item)
				}
			}
		}
	}
	walk(value)
	return json.Marshal(value)
}
