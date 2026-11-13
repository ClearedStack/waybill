package strictjson

import (
	"strings"
	"testing"
)

type sample struct {
	A string `json:"a"`
	B []int  `json:"b"`
}

func TestDecode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain", `{"a":"x","b":[1,2]}`, true},
		{"unknown field", `{"a":"x","c":1}`, false},
		{"duplicate key", `{"a":"x","a":"y"}`, false},
		{"nested duplicate key", `{"b":[],"a":"x","b":[]}`, false},
		{"trailing value", `{"a":"x"} {}`, false},
		{"trailing garbage", `{"a":"x"} x`, false},
		{"truncated", `{"a":"x"`, false},
		{"empty", ``, false},
		{"wrong type", `{"a":1}`, false},
		{"too deep", strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1), false},
	}
	for _, c := range cases {
		var v sample
		err := Decode([]byte(c.in), &v)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}
}
