package config

import "testing"

func TestNameAffixesCheck(t *testing.T) {
	for _, key := range []string{KeyTablePrefix, KeySchemaSuffix, KeyDatabaseSuffix} {
		for _, ok := range []string{"", "ashish", "dev_2", "feat-x"} {
			if err := Check(key, ok); err != nil {
				t.Errorf("Check(%s, %q) = %v", key, ok, err)
			}
		}
		for _, bad := range []string{"a b", "a.b", "a`b", "x;rm"} {
			if err := Check(key, bad); err == nil {
				t.Errorf("Check(%s, %q) took it", key, bad)
			}
		}
	}
}
