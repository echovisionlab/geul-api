package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
