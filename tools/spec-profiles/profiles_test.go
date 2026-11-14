package specprofiles

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ClearedStack/waybill/internal/fault"
	"github.com/ClearedStack/waybill/internal/record"
)

const root = "../.."

// TestRegistry checks tools/registry.yaml against the code and the disk. The
// file is read line by line; it needs no YAML library.
func TestRegistry(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root, "tools", "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var listed, subTools []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "- ")
		key, value, ok := strings.Cut(line, ": ")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		switch key {
		case "name":
			subTools = append(subTools, value)
		case "tool_type":
			listed = append(listed, value)
		case "path", "match", "logic", "fixtures":
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(value))); err != nil {
				t.Errorf("registry.yaml names %s, which is not there", value)
			}
		default:
			t.Errorf("registry.yaml has an unexpected key %q", key)
		}
	}
	if !reflect.DeepEqual(subTools, []string{"spec-profiles"}) {
		t.Errorf("sub-tools listed: %v", subTools)
	}
	built := ToolTypes()
	sort.Strings(listed)
	sort.Strings(built)
	if !reflect.DeepEqual(listed, built) {
		t.Errorf("registry.yaml lists %v, the binary has %v", listed, built)
	}
}

func TestSelect(t *testing.T) {
	for _, tt := range []string{"calculator", "checklist", "validator", "decision-tree"} {
		p, err := Select(&record.Record{ToolType: tt})
		if err != nil || p.ToolType != tt {
			t.Fatalf("%s: %v, %v", tt, p, err)
		}
	}
	if _, err := Select(&record.Record{ToolType: "schedule-builder"}); err == nil || err.Code != fault.NeedsSubtool {
		t.Fatalf("unknown tool type: %v", err)
	}
}
