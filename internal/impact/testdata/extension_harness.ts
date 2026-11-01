// Runs the extension's own column impact code on cases from stdin (one JSON
// array) and prints its answers as one JSON array, for xf's parity test.
import {
    impactMarkdown, impactReaders, sortColumns, sortTables,
} from 'EXTENSION_DIR/src/shared/columnLineage/impactSummary';

let input = '';
process.stdin.on('data', (chunk) => (input += chunk));
process.stdin.on('end', () => {
    const cases = JSON.parse(input);
    const out = cases.map((c: any) => {
        switch (c.kind) {
            case 'markdown': {
                const r = c.report;
                const view = {
                    status: 'ready',
                    changedCount: r.changedCount,
                    atRisk: r.atRisk ?? [],
                    safe: r.safe ?? [],
                    new: r.new ?? [],
                    unchecked: r.unchecked ?? [],
                    ...(r.unset ? { unset: r.unset } : {}),
                    ...(r.against ? { against: r.against } : {}),
                    ...(r.comparison ? { comparison: { headRef: r.comparison.headRef, baseRef: r.comparison.baseRef, mergeBaseSha: r.comparison.mergeBase, headLabel: r.comparison.headLabel } } : {}),
                };
                return impactMarkdown(view as any);
            }
            case 'readers': {
                const x = c.context;
                const ctx = {
                    resolveFile: (t: string) => x.files[t],
                    changed: new Set(x.changed ?? []),
                    sqlOf: (t: string) => x.sql[t],
                    toProd: (t: string) => x.toProd[t] ?? t,
                    deleted: new Set(x.deleted ?? []),
                    isAssertion: (t: string) => (x.assertions ?? []).includes(t),
                };
                return impactReaders(c.links ?? [], c.table, c.name, ctx);
            }
            case 'sort':
                return sortTables((c.tables ?? []).map((t: any) => ({ ...t, columns: sortColumns(t.columns ?? []) }))).map((t: any) => ({ table: t.table, columns: t.columns.map((col: any) => col.column) }));
        }
        throw new Error(`unknown case ${c.kind}`);
    });
    process.stdout.write(JSON.stringify(out));
});
