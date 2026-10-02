// rclone_test.go — unit tests for the rclone API DTOs.
package handlers

import (
	"encoding/json"
	"testing"

	"clever-connect/internal/rclone"
)

// TestProviderOptionOutAlwaysEmitsExamples guards the add-remote wizard crash:
// "examples" used to be omitted entirely when empty, so the form read
// `examples.length` on undefined and took the whole route down.
func TestProviderOptionOutAlwaysEmitsExamples(t *testing.T) {
	b, err := json.Marshal(providerOptionOut{Name: "acl", Examples: []rclone.Example{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["examples"]; !ok {
		t.Fatalf(`"examples" must always be present (even when empty), got %s`, b)
	}
	arr, ok := m["examples"].([]any)
	if !ok {
		t.Fatalf(`"examples" must be a JSON array, got %s`, b)
	}
	if len(arr) != 0 {
		t.Fatalf(`expected empty examples array, got %s`, b)
	}
}
