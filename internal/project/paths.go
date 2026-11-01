package project

import "strings"

// SlashPath writes a file path a tool printed with forward slashes, on
// every OS: Dataform and dbt-core print backslashes on Windows, dbt v2
// prints forward slashes, and a Graph holds one style so that comparing,
// joining and matching file names works the same everywhere. Convert back
// with filepath.FromSlash before touching the filesystem.
func SlashPath(p string) string { return strings.ReplaceAll(p, `\`, "/") }
