package authentication

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
)

func unifiedAuthInputNode(name string, value unifiedAuthValue, group, inputType string) unifiedAuthObject {
	return unifiedAuthObject{
		"type":  "input",
		"group": group,
		"attributes": unifiedAuthObject{
			"name":  name,
			"type":  inputType,
			"value": value,
		},
		"messages": unifiedAuthValues{},
	}
}

func (h *UnifiedAuthHandler) copyResponse(w http.ResponseWriter, response bufferedKratosResponse) {
	response.Header = canonicalUnifiedAuthResponseHeaders(response.Header, response.Body)
	h.transport.copy(w, response, projectUnifiedAuthFlow)
}

func canonicalUnifiedAuthResponseHeaders(header http.Header, body []byte) http.Header {
	public := make(http.Header)
	if contentType := strings.TrimSpace(header.Get("Content-Type")); contentType != "" {
		public.Set("Content-Type", contentType)
	} else if json.Valid(body) {
		public.Set("Content-Type", "application/json")
	}
	public.Set("Cache-Control", "no-store")
	public.Set("Pragma", "no-cache")
	if retryAfter := strings.TrimSpace(header.Get("Retry-After")); retryAfter != "" {
		public.Set("Retry-After", retryAfter)
	}
	if location := canonicalUnifiedAuthLocation(header.Get("Location")); location != "" {
		public.Set("Location", location)
	}
	for _, cookie := range mergeUnifiedAuthSetCookies(header) {
		public.Add("Set-Cookie", cookie)
	}
	return public
}

func canonicalUnifiedAuthLocation(rawLocation string) string {
	location, err := url.Parse(strings.TrimSpace(rawLocation))
	if err != nil || rawLocation == "" {
		return ""
	}
	if strings.HasPrefix(location.Path, "/self-service/login") ||
		strings.HasPrefix(location.Path, "/self-service/registration") {
		public := &url.URL{Path: unifiedAuthPath}
		if returnTo := strings.TrimSpace(location.Query().Get("return_to")); returnTo != "" {
			public.RawQuery = url.Values{"return_to": {returnTo}}.Encode()
		}
		return public.String()
	}
	return location.String()
}

func mergeUnifiedAuthSetCookies(headers ...http.Header) []string {
	type cookieEntry struct {
		key   string
		value string
	}
	entries := make([]cookieEntry, 0)
	indexes := make(map[string]int)
	for _, header := range headers {
		response := &http.Response{Header: http.Header{"Set-Cookie": header.Values("Set-Cookie")}}
		for _, cookie := range response.Cookies() {
			key := strings.ToLower(cookie.Name) + "\x00" + strings.ToLower(cookie.Domain) + "\x00" + cookie.Path
			entry := cookieEntry{key: key, value: cookie.String()}
			if index, exists := indexes[key]; exists {
				entries[index] = entry
				continue
			}
			indexes[key] = len(entries)
			entries = append(entries, entry)
		}
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.value)
	}
	return result
}

func projectUnifiedAuthFlow(body []byte) []byte {
	var payload unifiedAuthObject
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	flowID, _ := payload["id"].(string)
	ui, _ := payload["ui"].(unifiedAuthObject)
	nodes, _ := ui["nodes"].(unifiedAuthValues)
	if strings.TrimSpace(flowID) == "" || ui == nil || nodes == nil {
		return body
	}

	delete(payload, "request_url")
	delete(payload, "identity_schema")
	delete(payload, "transient_payload")
	delete(payload, "type")
	delete(ui, "action")
	if unifiedAuthNodesContain(nodes, "code") {
		ui["nodes"] = canonicalUnifiedAuthCodeNodes(nodes)
		ui["messages"] = canonicalUnifiedAuthMessages(ui["messages"])
		projected, err := json.Marshal(payload)
		if err != nil {
			return body
		}
		return projected
	}
	projectedNodes := make(unifiedAuthValues, 0, len(nodes))
	for _, rawNode := range nodes {
		node, _ := rawNode.(unifiedAuthObject)
		attributes, _ := node["attributes"].(unifiedAuthObject)
		name, _ := attributes["name"].(string)
		switch name {
		case "traits.email":
			attributes["name"] = "identifier"
		case "traits.name", "traits.preferred_locale":
			continue
		}
		node["messages"] = canonicalUnifiedAuthMessages(node["messages"])
		projectedNodes = append(projectedNodes, rawNode)
	}
	ui["nodes"] = projectedNodes
	ui["messages"] = canonicalUnifiedAuthMessages(ui["messages"])
	projected, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return projected
}

func canonicalUnifiedAuthMessages(value unifiedAuthValue) unifiedAuthValues {
	messages, _ := value.(unifiedAuthValues)
	result := make(unifiedAuthValues, 0, len(messages))
	seen := map[string]bool{}
	for _, rawMessage := range messages {
		if message, ok := rawMessage.(unifiedAuthObject); ok {
			messageType, _ := message["type"].(string)
			messageType = strings.TrimSpace(strings.ToLower(messageType))
			if messageType == "" || seen[messageType] {
				continue
			}
			seen[messageType] = true
			result = append(result, unifiedAuthObject{"type": messageType, "text": ""})
		}
	}
	return result
}

func unifiedAuthNodesContain(nodes unifiedAuthValues, name string) bool {
	for _, rawNode := range nodes {
		node, _ := rawNode.(unifiedAuthObject)
		attributes, _ := node["attributes"].(unifiedAuthObject)
		if attributes["name"] == name {
			return true
		}
	}
	return false
}

func canonicalUnifiedAuthCodeNodes(nodes unifiedAuthValues) unifiedAuthValues {
	values := unifiedAuthObject{}
	messages := map[string]unifiedAuthValues{}
	for _, rawNode := range nodes {
		node, _ := rawNode.(unifiedAuthObject)
		attributes, _ := node["attributes"].(unifiedAuthObject)
		name, _ := attributes["name"].(string)
		if name == "traits.email" {
			name = "identifier"
		}
		if _, exists := values[name]; !exists {
			values[name] = attributes["value"]
		}
		messages[name] = canonicalUnifiedAuthMessages(node["messages"])
	}
	if identifier, ok := values["identifier"].(string); ok {
		values["identifier"] = email.NormalizeAddressForDelivery(identifier)
	}
	if values["method"] == nil {
		values["method"] = "code"
	}
	if values["resend"] == nil {
		values["resend"] = "code"
	}
	type nodeSpec struct {
		name, nodeType, group, inputType string
	}
	specs := []nodeSpec{
		{name: "csrf_token", nodeType: "input", group: "default", inputType: "hidden"},
		{name: "identifier", nodeType: "input", group: "default", inputType: "hidden"},
		{name: "code", nodeType: "input", group: "code", inputType: "text"},
		{name: "method", nodeType: "input", group: "code", inputType: "submit"},
		{name: "resend", nodeType: "input", group: "code", inputType: "submit"},
	}
	result := make(unifiedAuthValues, 0, len(specs))
	for _, spec := range specs {
		value := values[spec.name]
		if value == nil {
			value = ""
		}
		nodeMessages := messages[spec.name]
		if nodeMessages == nil {
			nodeMessages = unifiedAuthValues{}
		}
		result = append(result, unifiedAuthObject{
			"type":  spec.nodeType,
			"group": spec.group,
			"attributes": unifiedAuthObject{
				"name":  spec.name,
				"type":  spec.inputType,
				"value": value,
			},
			"messages": nodeMessages,
		})
	}
	return result
}
