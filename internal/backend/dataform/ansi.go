package dataform

import "github.com/ashish10alex/xf/internal/backend/cmdrun"

func stripANSI(s string) string { return cmdrun.StripANSI(s) }
