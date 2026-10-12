package main

import (
	"errors"
	"testing"
)

func TestFormatErrorUsesInvokedExecutableName(t *testing.T) {
	got := formatError("flare-cli", errors.New("not authenticated"))
	if got != "flare-cli: not authenticated" {
		t.Fatalf("unexpected error %q", got)
	}
}
