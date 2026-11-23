package config

import "testing"

func TestQueryHistorySettingsValidationAndParsing(t *testing.T) {
	for _, good := range []string{"", "auto", "org", "acme-bi", "acme-bi,acme-prod", "auto,acme-bi"} {
		if err := Check(KeyQueryHistoryProjects, good); err != nil {
			t.Errorf("Check(queryHistoryProjects, %q) = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{"bad project", "acme,,bi", ";DROP TABLE"} {
		if err := Check(KeyQueryHistoryProjects, bad); err == nil {
			t.Errorf("Check(queryHistoryProjects, %q) = nil, want error", bad)
		}
	}

	for _, good := range []string{"0", "1", "14", "90"} {
		if err := Check(KeyQueryHistoryDays, good); err != nil {
			t.Errorf("Check(queryHistoryDays, %q) = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{"-1", "91", "abc", "1.5"} {
		if err := Check(KeyQueryHistoryDays, bad); err == nil {
			t.Errorf("Check(queryHistoryDays, %q) = nil, want error", bad)
		}
	}

	for _, good := range []string{"0", "10", "25.5"} {
		if err := Check(KeyQueryHistoryMaxGB, good); err != nil {
			t.Errorf("Check(queryHistoryMaxGB, %q) = %v, want nil", good, err)
		}
	}
	for _, bad := range []string{"-1", "abc"} {
		if err := Check(KeyQueryHistoryMaxGB, bad); err == nil {
			t.Errorf("Check(queryHistoryMaxGB, %q) = nil, want error", bad)
		}
	}

	if d, ok := ParseQueryHistoryDays(""); d != 14 || !ok {
		t.Errorf("ParseQueryHistoryDays(\"\") = (%d, %v), want (14, true)", d, ok)
	}
	if d, ok := ParseQueryHistoryDays("0"); d != 0 || ok {
		t.Errorf("ParseQueryHistoryDays(\"0\") = (%d, %v), want (0, false)", d, ok)
	}
	if d, ok := ParseQueryHistoryDays("30"); d != 30 || !ok {
		t.Errorf("ParseQueryHistoryDays(\"30\") = (%d, %v), want (30, true)", d, ok)
	}
	if b := ParseQueryHistoryMaxBytes(""); b != 10<<30 {
		t.Errorf("ParseQueryHistoryMaxBytes(\"\") = %d, want %d", b, 10<<30)
	}
	if b := ParseQueryHistoryMaxBytes("0"); b != 0 {
		t.Errorf("ParseQueryHistoryMaxBytes(\"0\") = %d, want 0", b)
	}
	if b := ParseQueryHistoryMaxBytes("2.5"); b != int64(2.5*float64(1<<30)) {
		t.Errorf("ParseQueryHistoryMaxBytes(\"2.5\") = %d", b)
	}
	// Invalid or negative input fails closed to the default 10 GiB cap (never 0/unlimited).
	for _, bad := range []string{"abc", "-5", "NaN", "Inf"} {
		if b := ParseQueryHistoryMaxBytes(bad); b != DefaultQueryHistoryMaxGB<<30 {
			t.Errorf("ParseQueryHistoryMaxBytes(%q) = %d, want default %d", bad, b, DefaultQueryHistoryMaxGB<<30)
		}
	}
	// Tiny positive values must not truncate down to 0 (unlimited).
	if b := ParseQueryHistoryMaxBytes("1e-10"); b != 1 {
		t.Errorf("ParseQueryHistoryMaxBytes(\"1e-10\") = %d, want 1", b)
	}
}
