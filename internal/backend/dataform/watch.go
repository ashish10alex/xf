package dataform

import (
	"context"
	"time"

	"github.com/ashish10alex/xf/internal/backend/watch"
)

// watchSpec is what affects a Dataform compile: definitions/ and includes/,
// and the project settings.
var watchSpec = watch.Spec{
	Dirs: []string{"definitions", "includes"},
	Exts: map[string]bool{
		".sqlx": true, ".js": true, ".sql": true, ".json": true,
		".yaml": true, ".yml": true, ".ipynb": true,
	},
	RootFiles: []string{SettingsFile, "dataform.json"},
}

// Watch reports changes to the files that affect compilation: anything with a
// watched extension under definitions/ or includes/, plus
// workflow_settings.yaml and dataform.json (see watch.Watch).
func Watch(ctx context.Context, root string, debounce time.Duration) (<-chan struct{}, error) {
	return watch.Watch(ctx, root, watchSpec, debounce)
}
