package version

import "testing"

func TestCurrentHasDevelopmentFallback(t *testing.T) {
	if Current() == "" {
		t.Fatal("version must not be empty")
	}
}
