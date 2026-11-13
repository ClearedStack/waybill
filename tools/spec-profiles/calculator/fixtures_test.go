package calculator_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clearedstack/waybill/internal/buildspec"
	"github.com/clearedstack/waybill/internal/compile"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current behaviour")

// TestFixtures runs every saved record under testdata through the whole
// compiler and compares the result, byte for byte, with the saved output.
//
// A folder named ok-* holds credit-note.golden.txt and build-spec.golden.json.
// A folder named needs-subtool-* or parse-* holds error.golden.json.
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
				if res.State != compile.StateBuilding {
					t.Errorf("state = %q", res.State)
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

// TestVendorMaterialChangesNothing checks the wall directly: a record with
// vendor observations compiles to the same schema, vectors and authority as
// the same record without them.
func TestVendorMaterialChangesNothing(t *testing.T) {
	plain := load(t, "ok-capped-allowance")
	loaded := load(t, "ok-vendor-material-stripped")
	if !bytes.Equal(encode(t, plain.BuildSpec.Schema), encode(t, loaded.BuildSpec.Schema)) ||
		!bytes.Equal(encode(t, plain.BuildSpec.Vectors), encode(t, loaded.BuildSpec.Vectors)) ||
		!bytes.Equal(encode(t, plain.BuildSpec.Authority), encode(t, loaded.BuildSpec.Authority)) {
		t.Fatal("vendor observations changed the build specification")
	}
	spec := string(encode(t, loaded.BuildSpec))
	for _, leak := range []string{"Stipend", "qm-card", "quillmarsh.example", "http", "765", "<div"} {
		if strings.Contains(spec, leak) {
			t.Errorf("the build specification contains %q", leak)
		}
	}
}

func load(t *testing.T, name string) compile.Result {
	t.Helper()
	input, err := os.ReadFile(filepath.Join("testdata", name, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	res := compile.Compile(input)
	if res.Error != nil {
		t.Fatalf("%s: %v", name, res.Error)
	}
	return res
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
		t.Fatalf("missing golden file (run the tests with -update, then read the diff): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs\n--- got ---\n%s--- want ---\n%s", path, got, want)
	}
}
