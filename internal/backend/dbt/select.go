package dbt

import (
	"context"
	"path/filepath"

	"github.com/ashish10alex/xf/internal/project"
)

// Selected are the actions that `dbt build` with selection, its selection
// flags as given (--select, --exclude, --selector, --resource-type …),
// builds: what `dbt ls` lists with them that has something to run. With no
// selection, that is every such action. xf run uses them to work out what
// a run reads from prod (ADR 0038).
func (b *Backend) Selected(ctx context.Context, root string, selection []string) ([]project.ActionID, error) {
	args := append(append([]string{}, selection...), "--output", "json", "--output-keys", "unique_id")
	inv, err := b.invoke(ctx, root, root, filepath.Join(b.variantDir(root), "select"), "ls", args...)
	if err != nil {
		return nil, err
	}
	if inv.failed != nil || inv.manifest == nil {
		return nil, b.failure(inv, "ls")
	}
	m, err := decodeFull(inv.manifest)
	if err != nil {
		return nil, err
	}
	var out []project.ActionID
	seen := map[project.ActionID]bool{}
	for _, id := range printedIDs(inv.stdout) {
		n, ok := m.nodes[id]
		if !ok {
			continue // a source, an exposure, an ephemeral model, …: nothing to run
		}
		if aid := target(n).ID(); !seen[aid] {
			seen[aid] = true
			out = append(out, aid)
		}
	}
	return out, nil
}
