package compile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/clearedstack/waybill/internal/fault"
)

const base = "../../tools/spec-profiles/calculator/testdata/ok-capped-allowance/record.json"

// mutate loads the base record, changes it and compiles it.
func mutate(t *testing.T, change func(map[string]any)) Result {
	t.Helper()
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	change(rec)
	out, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return Compile(out)
}

// The wall check is a backstop behind the field checks, so it is exercised
// here directly: a product name short enough to pass as an input name.
func TestWallCatchesProductNameInSchema(t *testing.T) {
	res := mutate(t, func(rec map[string]any) {
		rec["source"].(map[string]any)["product"] = "Daily Amount"
	})
	if res.Error == nil || res.Error.Code != fault.Parse || res.Error.Field != "build_spec" {
		t.Fatalf("result: %+v, error: %v", res, res.Error)
	}
	if res.BuildSpec != nil || res.CreditNote != "" {
		t.Error("output was produced for a record that breached the wall")
	}
}

// Whatever is wrong with a record, the error must not repeat its contents.
func TestErrorsDoNotEchoInput(t *testing.T) {
	const marker = "zzmarkerzz"
	changes := map[string]func(map[string]any){
		"tool type":   func(r map[string]any) { r["tool_type"] = marker },
		"state":       func(r map[string]any) { r["state"] = marker },
		"decision":    func(r map[string]any) { r["determination"].(map[string]any)["decision"] = marker },
		"title":       func(r map[string]any) { r["title"] = marker + "\n" + marker },
		"unknown key": func(r map[string]any) { r[marker] = marker },
		"formula": func(r map[string]any) {
			r["tool"].(map[string]any)["outputs"].([]any)[0].(map[string]any)["formula"] = marker + "(days)"
		},
		"input name": func(r map[string]any) {
			r["tool"].(map[string]any)["inputs"].([]any)[0].(map[string]any)["name"] = marker
		},
	}
	for name, change := range changes {
		res := mutate(t, change)
		if res.Error == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		out, _ := json.Marshal(res)
		if strings.Contains(string(out), marker) {
			t.Errorf("%s: the error repeats the input: %s", name, out)
		}
	}
}
