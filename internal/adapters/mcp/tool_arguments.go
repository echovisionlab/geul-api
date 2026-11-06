package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

func decodeArguments(arguments mcpserver.ToolArguments, target any) error {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple argument values are not allowed")
	}
	return nil
}

func rejectNullArguments(arguments mcpserver.ToolArguments, names ...string) error {
	for _, name := range names {
		if bytes.Equal(bytes.TrimSpace(arguments[name]), []byte("null")) {
			return fmt.Errorf("%s cannot be null", name)
		}
	}
	return nil
}

// rejectUnadvertisedArguments keeps tools that share one decode struct
// strict: a field only a sibling tool reads must not be silently ignored.
func rejectUnadvertisedArguments(arguments mcpserver.ToolArguments, advertised ...string) error {
	names := make([]string, 0, len(arguments))
	for name := range arguments {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !slices.Contains(advertised, name) {
			return fmt.Errorf("unexpected argument %q", name)
		}
	}
	return nil
}

// withoutArguments copies arguments without the named ones.
func withoutArguments(arguments mcpserver.ToolArguments, names ...string) mcpserver.ToolArguments {
	kept := make(mcpserver.ToolArguments, len(arguments))
	for name, value := range arguments {
		if !slices.Contains(names, name) {
			kept[name] = value
		}
	}
	return kept
}
