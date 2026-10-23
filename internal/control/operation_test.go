package control

import (
	"strings"
	"testing"
)

func TestUnexpectedOperationFailureIsIsolatedAndRedacted(t *testing.T) {
	result, e := runOperation(func() (any, error) { panic("private-source-url-and-password") })
	if result != nil || e == nil || strings.Contains(e.Error(), "private") {
		t.Fatal("panic escaped or secret leaked")
	}
	if e.Error() != "internal_operation_failure" {
		t.Fatal(e)
	}
	result, e = runOperation(func() (any, error) { return "next operation", nil })
	if e != nil || result != "next operation" {
		t.Fatal("subsequent operation cannot continue")
	}
}
