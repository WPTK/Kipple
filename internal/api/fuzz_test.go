package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

// FuzzSettingValidators: every setting's validator, fed arbitrary JSON, never
// panics; an accepted value is JSON-serializable and re-validates to itself.
func FuzzSettingValidators(f *testing.F) {
	for _, s := range []string{
		`null`, `true`, `"dark"`, `18`, `1e400`, `-1`, `1.5`, `[]`, `{}`, `"x"`, `[{"t":"feed","id":"12"}]`,
		`[{"t":"folder","id":"0"},{"t":"folder","id":"0"}]`, `[{"id":"a","name":"n","q":"x","scope":"all"}]`,
		`[{"t":"feed","id":"99999999999999999999"}]`, `[[[[[[]]]]]]`, `{"a":{"b":1}}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return
		}
		for _, d := range settingDefs {
			if d.check == nil {
				continue
			}
			out, msg := d.check(v)
			if msg != "" {
				continue
			}
			b, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("%s: accepted value does not marshal: %v", d.Key, err)
			}
			var back any
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatalf("%s: %v", d.Key, err)
			}
			out2, msg2 := d.check(back)
			if msg2 != "" {
				t.Fatalf("%s: accepted %s but rejects its own output %s: %s", d.Key, raw, b, msg2)
			}
			b2, _ := json.Marshal(out2)
			var back2 any
			_ = json.Unmarshal(b2, &back2)
			if !reflect.DeepEqual(back, back2) {
				t.Fatalf("%s: not idempotent: %s vs %s", d.Key, b, b2)
			}
		}
	})
}
