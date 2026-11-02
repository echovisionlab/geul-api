package pathslug

import (
	"testing"

	"connectrpc.com/connect"
)

func TestValidate(t *testing.T) {
	if err := Validate("summer-2026"); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	err := Validate("summer/2026")
	if connect.CodeOf(err) != connect.CodeInvalidArgument || err.Error() != "invalid_argument: slug: must not contain '/'" {
		t.Fatalf("Validate() error = %v", err)
	}
}
