package decisiontree_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ClearedStack/waybill/internal/buildspec"
	"github.com/ClearedStack/waybill/internal/compile"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current behaviour")

func TestFixtures(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join(dir, "record.json"))
			if err != nil {
				t.Fatal(err)
			}
			res := compile.Compile(input)
			if again := compile.Compile(input); !bytes.Equal(encode(t, res), encode(t, again)) {
				t.Fatal("two runs on the same record gave different results")
			}

			wantCode := ""
			switch {
			case strings.HasPrefix(name, "ok-"):
			case strings.HasPrefix(name, "needs-subtool-"):
				wantCode = "E_NEEDS_SUBTOOL"
			case strings.HasPrefix(name, "parse-"):
				wantCode = "E_PARSE"
			default:
				t.Fatalf("fixture folder %q has no known prefix", name)
			}

			if wantCode == "" {
				if res.Error != nil {
					t.Fatalf("unexpected error: %v", res.Error)
				}
				if res.State != compile.StateApproved && res.State != compile.StateBuilding {
					t.Errorf("state = %q", res.State)
				}
				if res.Spec == nil || res.Spec.Pattern != "decision-tree" {
					t.Errorf("spec = %+v", res.Spec)
				}
				golden(t, filepath.Join(dir, "credit-note.golden.txt"), []byte(res.CreditNote))
				golden(t, filepath.Join(dir, "build-spec.golden.json"), encode(t, res.BuildSpec))
				return
			}
			if res.Error == nil || string(res.Error.Code) != wantCode {
				t.Fatalf("error = %v, want %s", res.Error, wantCode)
			}
			if res.BuildSpec != nil || res.CreditNote != "" {
				t.Error("a failed item still produced output")
			}
			if (res.State == compile.StateNeedsSubtool) != (wantCode == "E_NEEDS_SUBTOOL") {
				t.Errorf("state = %q for %s", res.State, wantCode)
			}
			golden(t, filepath.Join(dir, "error.golden.json"), encode(t, res))
		})
	}
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	out, err := buildspec.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs\n--- got ---\n%s--- want ---\n%s", path, got, want)
	}
}
