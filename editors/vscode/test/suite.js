// The extension's end-to-end test, run inside VS Code with the repo open:
//
//   code <repo> --extensionDevelopmentPath=<extension> \
//     --extensionTestsPath=<extension>/test/suite.js
//
// It needs the tools xf runs (dataform, dbt) and writes what it checked, one
// line each, to $XF_VSCODE_RESULT. Not packed into the VSIX.

const fs = require("fs");
const os = require("os");
const path = require("path");
const vscode = require("vscode");

const results = [];
const out = process.env.XF_VSCODE_RESULT;

function log(line) {
  results.push(line);
  if (out) fs.appendFileSync(out, line + "\n");
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// eventually polls fn until it returns something truthy, for up to ms.
async function eventually(what, ms, fn) {
  const deadline = Date.now() + ms;
  for (;;) {
    const got = await fn();
    if (got) return got;
    if (Date.now() > deadline) throw new Error(`timed out: ${what}`);
    await sleep(250);
  }
}

async function check(name, fn) {
  try {
    const detail = await fn();
    log(`ok   ${name}${detail ? ": " + detail : ""}`);
  } catch (err) {
    log(`FAIL ${name}: ${err && err.stack ? err.stack : err}`);
  }
}

// at opens file and returns it with the position inside needle.
async function at(file, needle, into = 2) {
  const doc = await vscode.workspace.openTextDocument(file);
  await vscode.window.showTextDocument(doc);
  const i = doc.getText().indexOf(needle);
  if (i < 0) throw new Error(`${needle} isn't in ${file}`);
  return { doc, pos: doc.positionAt(i + needle.indexOf("(") + into + 1) };
}

async function definitionAt(file, needle, wantFile) {
  const { doc, pos } = await at(file, needle);
  const locs = await eventually(`a definition in ${file}`, 90000, async () => {
    const got = await vscode.commands.executeCommand("vscode.executeDefinitionProvider", doc.uri, pos);
    return got && got.length > 0 ? got : null;
  });
  const target = (locs[0].targetUri || locs[0].uri).fsPath;
  if (path.basename(target) !== wantFile) throw new Error(`went to ${target}, want ${wantFile}`);
  return target;
}

async function run() {
  if (out) fs.writeFileSync(out, "");
  const repo = vscode.workspace.workspaceFolders[0].uri.fsPath;
  const ex = (...p) => path.join(repo, "examples", ...p);
  const ext = vscode.extensions.all.find((e) => e.packageJSON.name === "xf-test");
  const api = await ext.activate();
  log(`xf at ${vscode.workspace.getConfiguration("xf").get("path") || "the bundled xf"}`);

  const fct = ex("dataform", "definitions", "marts", "fct_orders.sqlx");
  await check("dataform: definition", () => definitionAt(fct, 'ref("stg_orders")', "stg_orders.sqlx"));
  await check("dataform: hover", async () => {
    const { doc, pos } = await at(fct, 'ref("stg_orders")');
    const hovers = await vscode.commands.executeCommand("vscode.executeHoverProvider", doc.uri, pos);
    const text = (hovers || []).flatMap((h) => h.contents.map((c) => c.value || String(c))).join("\n");
    if (!text.includes("stg_orders")) throw new Error(`hover: ${text}`);
    return text.split("\n")[0];
  });
  await check("dataform: diagnostics", async () => {
    const broken = vscode.Uri.file(ex("dataform-broken", "definitions", "order_totals.sqlx"));
    await vscode.window.showTextDocument(await vscode.workspace.openTextDocument(broken));
    const diags = await eventually("diagnostics in dataform-broken", 90000, () => {
      const d = vscode.languages.getDiagnostics(broken);
      return d.length > 0 ? d : null;
    });
    return diags[0].message.split("\n")[0];
  });
  await check("dbt: definition", () =>
    definitionAt(ex("dbt", "models", "reporting", "revenue_report.sql"), "ref('fct_orders')", "fct_orders.sql"),
  );
  await check("SQL outside the projects gets no client", async () => {
    const file = path.join(os.tmpdir(), "xf-outside.sql");
    fs.writeFileSync(file, "select 1\n");
    const doc = await vscode.workspace.openTextDocument(file);
    await vscode.window.showTextDocument(doc);
    await sleep(1000);
    const serving = [...api.projects.values()].filter((p) => vscode.languages.match(p.selector, doc) > 0);
    if (serving.length > 0) throw new Error(`served by ${serving.map((p) => p.root)}`);
    const started = [...api.projects.values()].filter((p) => p.client).map((p) => path.basename(p.root));
    return `clients for ${started.join(", ")}`;
  });
  await check("a killed xf lsp restarts", async () => {
    const p = api.projects.get(ex("dataform"));
    const proc = p.client._serverProcess;
    if (!proc || !proc.pid) throw new Error("no server process");
    const pid = proc.pid;
    process.kill(pid);
    await eventually("a new xf lsp", 30000, () => {
      const np = p.client._serverProcess;
      return p.client.isRunning() && np && np.pid !== pid;
    });
    await definitionAt(fct, 'ref("stg_orders")', "stg_orders.sqlx");
    return `pid ${pid} -> ${p.client._serverProcess.pid}`;
  });
  await check("xf: Open, then o opens the file in VS Code", async () => {
    const legacy = ex("dataform", "definitions", "marts", "legacy_orders.sqlx");
    await vscode.window.showTextDocument(await vscode.workspace.openTextDocument(legacy));
    await vscode.commands.executeCommand("xf.open");
    const term = api.terminals.get(ex("dataform"));
    if (!term) throw new Error("no terminal");
    const pid = await term.processId;
    // Hidden at creation, so the Python extensions don't type into it.
    if (!term.creationOptions.hideFromUser) throw new Error("the xf terminal isn't created hidden from the user");
    // The TUI follows legacy_orders; move the editor to a file outside the
    // project, which it doesn't follow, then press o until the TUI, once
    // compiled, sends the editor back.
    const other = path.join(os.tmpdir(), "xf-outside.sql");
    await vscode.window.showTextDocument(await vscode.workspace.openTextDocument(other), vscode.ViewColumn.One);
    await eventually("o to open legacy_orders.sqlx", 120000, async () => {
      term.sendText("o", false);
      await sleep(1500);
      const ed = vscode.window.activeTextEditor;
      return ed && ed.document.uri.fsPath.toLowerCase() === legacy.toLowerCase();
    });
    await vscode.commands.executeCommand("xf.open"); // focuses, doesn't start another
    if (vscode.window.terminals.filter((t) => t.name === term.name).length !== 1) throw new Error("a second xf");
    // After q, xf: Open starts a new one.
    term.sendText("q", false);
    await sleep(3000);
    await vscode.commands.executeCommand("xf.open");
    const again = api.terminals.get(ex("dataform"));
    if (!again || again === term) throw new Error("xf: Open didn't start xf again after q");
    const pid2 = await again.processId;
    again.sendText("q", false);
    return `TUI pid ${pid}, then ${pid2}`;
  });

  const failed = results.filter((r) => r.startsWith("FAIL"));
  log(failed.length === 0 ? "PASS" : `FAILED ${failed.length}`);
  if (failed.length > 0) throw new Error(failed.join("\n"));
}

module.exports = { run };
