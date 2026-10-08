package serverconfig

import "testing"

func TestInfoMatchesSchema(t *testing.T) {
	for key := range fieldInfo {
		if _, ok := FieldByKey(key); !ok {
			t.Errorf("info for unknown setting %q", key)
		}
	}
	groups := map[string]bool{}
	for _, f := range Fields {
		groups[f.Group] = true
	}
	for g := range GroupInfo {
		if !groups[g] {
			t.Errorf("info for unknown group %q", g)
		}
	}
}
