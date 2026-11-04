package frontend

import (
	"context"
	"path/filepath"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/project"
)

// SearchProjects is xf search's walk, which xf web's picker also runs: it
// searches each of dirs (~ is the home folder) for want's projects ("" for
// either tool) and gives each to each as it is found, a project found from
// more than one of dirs once. It stops with ctx's error once ctx is done.
func SearchProjects(ctx context.Context, home string, dirs []string, want string, each func(backend.Found)) error {
	seen := map[string]bool{}
	for _, d := range dirs {
		abs, err := filepath.Abs(ExpandPath(home, d))
		if err != nil {
			return err
		}
		err = backend.SearchEach(ctx, abs, want, func(f backend.Found) {
			if real := project.RealPath(f.Root); !seen[real] {
				seen[real] = true
				each(f)
			}
		})
		if err != nil {
			return err
		}
	}
	return nil
}
