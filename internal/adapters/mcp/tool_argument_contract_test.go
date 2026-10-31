package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

// The argument contract test checks every tools/list input schema against the
// Go value its handler decodes into, without a hand-kept tool-to-struct table:
// decodeArguments rejects unknown fields, so a probe that sends exactly one
// property shows whether the decode target has a field with that json name.
// Probes run against zero-value providers; handlers that get past decoding
// panic on their nil dependencies, which the probe recovers.

const contractProbeField = "__contract_probe__"

// toolProbeVariants supplies the discriminator values a tool reads before it
// picks the struct to decode into; each probe runs once per variant.
var toolProbeVariants = map[string][]map[string]any{
	ToolFileTransfer: {{"a": "begin"}, {"a": "status"}, {"a": "complete"}},
}

// acceptedUnadvertisedFields lists struct fields a decoder accepts although
// the schema does not advertise them, keyed by "tool path.field". Each one
// must be rejected by the handler before it is used.
var acceptedUnadvertisedFields = map[string]string{
	"document_heading_create block_id":        "shared heading struct; create rejects a supplied block_id",
	"document_heading_update after_block_id":  "shared heading struct; update rejects after_block_id",
	"document_heading_update parent_block_id": "shared heading struct; update rejects parent_block_id",
	"map_place_create clear_image":            "shared settings struct; create rejects clear_image",
	"map_place_create map_place_id":           "shared settings struct; create rejects map_place_id",
	"document_featured_image_delete file_id":  "drift: shared struct; delete ignores file_id",
	"work_credit_add credit_id":               "drift: shared struct; add ignores credit_id",
	"work_credit_update artist_id":            "drift: shared struct; update ignores artist_id",
	"work_credit_update document_id":          "drift: shared struct; update ignores document_id",
	"work_credit_update member_id":            "drift: shared struct; update ignores member_id",
	"work_credit_update name":                 "drift: shared struct; update ignores name",
	"work_credit_delete artist_id":            "drift: shared struct; delete ignores artist_id",
	"work_credit_delete credit_role":          "drift: shared struct; delete ignores credit_role",
	"work_credit_delete document_id":          "drift: shared struct; delete ignores document_id",
	"work_credit_delete group_id":             "drift: shared struct; delete ignores group_id",
	"work_credit_delete member_id":            "drift: shared struct; delete ignores member_id",
	"work_credit_delete name":                 "drift: shared struct; delete ignores name",
	"work_credit_group_create group_id":       "drift: shared struct; create ignores group_id",
	"work_credit_group_update document_id":    "drift: shared struct; update ignores document_id",
	"work_credit_group_delete document_id":    "drift: shared struct; delete ignores document_id",
	"work_credit_group_delete name":           "drift: shared struct; delete ignores name",
}

type schemaStep struct {
	key  string
	item bool
}

type schemaPath []schemaStep

func (path schemaPath) String() string {
	var builder strings.Builder
	for _, step := range path {
		if step.item {
			builder.WriteString("[]")
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('.')
		}
		builder.WriteString(step.key)
	}
	if builder.Len() == 0 {
		return "$"
	}
	return builder.String()
}

func (path schemaPath) with(step schemaStep) schemaPath {
	return append(append(schemaPath(nil), path...), step)
}

// value wraps leaf in the containers path names.
func (path schemaPath) value(leaf any) any {
	value := leaf
	for index := len(path) - 1; index >= 0; index-- {
		if path[index].item {
			value = []any{value}
		} else {
			value = map[string]any{path[index].key: value}
		}
	}
	return value
}

type schemaObject struct {
	path       schemaPath
	closed     bool
	properties map[string][]map[string]any
	required   []string
}

type schemaWalker struct {
	root    map[string]any
	objects []*schemaObject
}

func (walker *schemaWalker) resolve(node map[string]any, seen map[string]bool) (map[string]any, map[string]bool) {
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			return node, seen
		}
		if seen[ref] {
			return nil, seen
		}
		seen = copySeen(seen)
		seen[ref] = true
		name, found := strings.CutPrefix(ref, "#/$defs/")
		if !found {
			return nil, seen
		}
		definitions, _ := walker.root["$defs"].(map[string]any)
		target, _ := definitions[name].(map[string]any)
		if target == nil {
			return nil, seen
		}
		node = target
	}
}

func copySeen(seen map[string]bool) map[string]bool {
	result := make(map[string]bool, len(seen)+1)
	for key, value := range seen {
		result[key] = value
	}
	return result
}

// branches returns node plus every schema that can contribute properties to
// the same JSON object.
func (walker *schemaWalker) branches(node map[string]any, seen map[string]bool) []map[string]any {
	node, seen = walker.resolve(node, seen)
	if node == nil {
		return nil
	}
	result := []map[string]any{node}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		items, _ := node[keyword].([]any)
		for _, item := range items {
			if branch, ok := item.(map[string]any); ok {
				result = append(result, walker.branches(branch, seen)...)
			}
		}
	}
	for _, keyword := range []string{"then", "else"} {
		if branch, ok := node[keyword].(map[string]any); ok {
			result = append(result, walker.branches(branch, seen)...)
		}
	}
	return result
}

func (walker *schemaWalker) walk(node map[string]any, path schemaPath, seen map[string]bool) {
	if len(path) > 8 {
		return
	}
	branches := walker.branches(node, seen)
	object := &schemaObject{path: path, properties: map[string][]map[string]any{}}
	var items []map[string]any
	for _, branch := range branches {
		properties, _ := branch["properties"].(map[string]any)
		for name, schema := range properties {
			if typed, ok := schema.(map[string]any); ok {
				object.properties[name] = append(object.properties[name], typed)
			}
		}
		if open, ok := branch["additionalProperties"].(bool); ok && !open {
			object.closed = true
		}
		required, _ := branch["required"].([]any)
		for _, name := range required {
			object.required = append(object.required, fmt.Sprint(name))
		}
		if item, ok := branch["items"].(map[string]any); ok {
			items = append(items, item)
		}
	}
	if len(object.properties) > 0 {
		walker.objects = append(walker.objects, object)
		for name, schemas := range object.properties {
			for _, schema := range schemas {
				walker.walk(schema, path.with(schemaStep{key: name}), seen)
			}
		}
	}
	for _, item := range items {
		walker.walk(item, path.with(schemaStep{item: true}), seen)
	}
}

func schemaObjects(t *testing.T, tool mcpserver.Tool) []*schemaObject {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(tool.InputSchema, &root); err != nil {
		t.Fatalf("%s: decode input schema: %v", tool.Name, err)
	}
	walker := &schemaWalker{root: root}
	walker.walk(root, nil, map[string]bool{})
	// Branches can describe one object path more than once; merge them so
	// every path is probed against the union of its advertised properties.
	merged := map[string]*schemaObject{}
	var order []string
	for _, object := range walker.objects {
		key := object.path.String()
		existing, ok := merged[key]
		if !ok {
			merged[key] = object
			order = append(order, key)
			continue
		}
		for name, schemas := range object.properties {
			existing.properties[name] = append(existing.properties[name], schemas...)
		}
		existing.required = append(existing.required, object.required...)
		existing.closed = existing.closed || object.closed
	}
	sort.Strings(order)
	result := make([]*schemaObject, 0, len(order))
	for _, key := range order {
		result = append(result, merged[key])
	}
	return result
}

type argumentProber struct {
	set *ToolSet
}

// probe calls the tool with one value and returns the error text it produced
// before or during decoding.
func (prober argumentProber) probe(name string, arguments map[string]any) (message string) {
	encoded := mcpserver.ToolArguments{}
	for key, value := range arguments {
		raw, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		encoded[key] = raw
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			message = ""
		}
	}()
	_, err := prober.set.CallTool(context.Background(), mcpserver.Principal{}, name, encoded)
	if err == nil {
		return ""
	}
	return err.Error()
}

// accepts reports whether the decode target at path has a field named key.
// decodeArguments reports unknown fields; a few handlers check the argument
// names themselves first and report unexpected arguments.
func (prober argumentProber) accepts(name string, path schemaPath, key string) bool {
	unknown := []string{"unknown field " + strconv.Quote(key), "unexpected argument " + strconv.Quote(key)}
	variants := toolProbeVariants[name]
	if len(variants) == 0 {
		variants = []map[string]any{{}}
	}
	for _, variant := range variants {
		if _, discriminator := variant[key]; discriminator && len(path) == 0 {
			return true
		}
		if prober.acceptsIn(name, variant, path.with(schemaStep{key: key}), unknown) {
			return true
		}
	}
	return false
}

func (prober argumentProber) acceptsIn(name string, variant map[string]any, path schemaPath, unknown []string) bool {
	for _, leaf := range []any{nil, 0, ""} {
		arguments := path.value(leaf).(map[string]any)
		for key, value := range variant {
			arguments[key] = value
		}
		message := prober.probe(name, arguments)
		for _, marker := range unknown {
			if strings.Contains(message, marker) {
				return false
			}
		}
		if !strings.Contains(message, "cannot be null") {
			return true
		}
	}
	return true
}

// candidateFieldNames returns every json field name declared in this
// package's source plus every property name any tool schema advertises.
func candidateFieldNames(t *testing.T, tools []mcpserver.Tool) []string {
	t.Helper()
	names := map[string]bool{}
	for _, tool := range tools {
		for _, object := range schemaObjects(t, tool) {
			for name := range object.properties {
				names[name] = true
			}
		}
	}
	for _, file := range parsePackageSource(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				return true
			}
			jsonName, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
			if jsonName != "" && jsonName != "-" {
				names[jsonName] = true
			}
			return true
		})
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func TestToolInputSchemasMatchDecodedArguments(t *testing.T) {
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	set, err := NewToolSet(allToolProviders()...)
	if err != nil {
		t.Fatal(err)
	}
	prober := argumentProber{set: set}
	tools := listAllTools(t)
	candidates := candidateFieldNames(t, tools)
	strictPaths, rawPaths := 0, 0
	acceptedSeen := map[string]bool{}
	for _, tool := range tools {
		objects := schemaObjects(t, tool)
		if prober.accepts(tool.Name, nil, contractProbeField) {
			t.Errorf("%s: arguments are not decoded strictly; an unknown field was accepted", tool.Name)
			continue
		}
		hasRoot := false
		for _, object := range objects {
			if len(object.path) == 0 {
				hasRoot = true
			}
			advertised := map[string]bool{}
			for name := range object.properties {
				advertised[name] = true
			}
			for _, name := range object.required {
				if !advertised[name] {
					t.Errorf("%s %s: required property %q is not declared in properties", tool.Name, object.path, name)
				}
			}
			if len(object.path) > 0 && prober.accepts(tool.Name, object.path, contractProbeField) {
				// The value at this path is decoded as raw JSON, a map or
				// a custom unmarshaler; its own decoder owns the contract.
				rawPaths++
				t.Logf("%s %s: decoded as raw JSON, a map or a custom unmarshaler; not probed", tool.Name, object.path)
				continue
			}
			strictPaths++
			if !object.closed {
				t.Errorf("%s %s: decoded strictly but the schema does not set additionalProperties to false", tool.Name, object.path)
			}
			names := make([]string, 0, len(advertised))
			for name := range advertised {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !prober.accepts(tool.Name, object.path, name) {
					t.Errorf("%s %s: schema property %q is rejected by the decoded argument struct", tool.Name, object.path, name)
				}
			}
			for _, name := range candidates {
				if advertised[name] || !prober.accepts(tool.Name, object.path, name) {
					continue
				}
				key := tool.Name + " " + object.path.with(schemaStep{key: name}).String()
				acceptedSeen[key] = true
				if _, allowed := acceptedUnadvertisedFields[key]; !allowed {
					t.Errorf("%s %s: argument struct accepts %q, which the schema does not advertise", tool.Name, object.path, name)
				}
			}
		}
		if !hasRoot {
			// A tool without properties must still reject every argument.
			for _, name := range candidates {
				if prober.accepts(tool.Name, nil, name) {
					t.Errorf("%s: schema has no properties but the argument struct accepts %q", tool.Name, name)
				}
			}
		}
	}
	t.Logf("probed %d tools: %d strict object paths, %d raw object paths, %d candidate field names", len(tools), strictPaths, rawPaths, len(candidates))
	for key := range acceptedUnadvertisedFields {
		if !acceptedSeen[key] {
			t.Errorf("acceptedUnadvertisedFields lists %s, which no longer occurs", key)
		}
	}
}
