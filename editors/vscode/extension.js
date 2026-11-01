// xf in VS Code, for testing only: xf lsp for each Dataform or dbt project
// in the workspace, the TUI in a terminal beside the editor ("xf: Open"),
// which follows the active editor and opens files back in it (o).
//
// Never published: it exists to prove the editor experience on Windows.

const fs = require("fs");
const path = require("path");
const vscode = require("vscode");
const { LanguageClient } = require("vscode-languageclient/node");

/**
 * @typedef {{ root: string, kind: "dataform" | "dbt", selector: vscode.DocumentFilter[], client?: LanguageClient }} Project
 */

const dataformMarkers = ["workflow_settings.yaml", "dataform.json"];
const dbtMarkers = ["dbt_project.yml"];

/** @type {Map<string, Project>} by root */
const projects = new Map();
/** @type {Map<string, vscode.Terminal>} by root */
const terminals = new Map();
/** @type {vscode.LogOutputChannel} */
let output;
/** @type {vscode.ExtensionContext} */
let extension;

/** @param {vscode.ExtensionContext} context */
async function activate(context) {
  extension = context;
  // A log channel: the language client writes to it with levels.
  output = vscode.window.createOutputChannel("xf", { log: true });
  context.subscriptions.push(
    output,
    vscode.commands.registerCommand("xf.open", openTUI),
    vscode.window.onDidChangeActiveTextEditor((ed) => follow(ed, "BufEnter")),
    vscode.window.onDidChangeWindowState((s) => s.focused && follow(vscode.window.activeTextEditor, "FocusGained")),
    vscode.window.onDidCloseTerminal((t) => {
      for (const [root, term] of terminals) {
        if (term === t) terminals.delete(root);
      }
    }),
    vscode.workspace.onDidChangeWorkspaceFolders(() => findProjects()),
    vscode.workspace.onDidOpenTextDocument((doc) => startFor(doc)),
  );
  await findProjects();
  await Promise.all(vscode.workspace.textDocuments.map(startFor));
  follow(vscode.window.activeTextEditor, "BufEnter");
  // For the tests.
  return { projects, terminals };
}

async function deactivate() {
  await Promise.all([...projects.values()].map(stop));
  projects.clear();
}

// stop stops project p's client, if it runs.
async function stop(p) {
  if (p.client && p.client.isRunning()) await p.client.stop();
}

// xfPath is the xf to run: xf.path, or the one bundled in bin/.
function xfPath() {
  const configured = vscode.workspace.getConfiguration("xf").get("path");
  if (configured) return configured;
  const exe = process.platform === "win32" ? "xf.exe" : "xf";
  return path.join(extension.extensionPath, "bin", exe);
}

// findProjects finds the Dataform and dbt projects in the workspace (the
// directories holding their markers, outside node_modules), and stops the
// clients of those that have gone.
async function findProjects() {
  const kinds = new Map();
  const markers = [...dataformMarkers, ...dbtMarkers];
  const uris = await vscode.workspace.findFiles(`**/{${markers.join(",")}}`, "**/node_modules/**");
  for (const uri of uris) {
    const root = path.dirname(uri.fsPath);
    const kind = dbtMarkers.includes(path.basename(uri.fsPath)) ? "dbt" : "dataform";
    // A directory that is both is served as neither, as xf lsp does.
    kinds.set(root, kinds.has(root) && kinds.get(root) !== kind ? "both" : kind);
  }
  for (const [root, p] of projects) {
    if (kinds.get(root) !== p.kind) {
      projects.delete(root);
      await stop(p);
    }
  }
  for (const [root, kind] of kinds) {
    if (kind !== "both" && !projects.has(root)) {
      projects.set(root, { root, kind, selector: selectorFor(root, kind) });
    }
  }
}

// selectorFor picks a project's files: .sqlx and JavaScript in a Dataform
// project, SQL in a dbt one.
function selectorFor(root, kind) {
  const files = (glob) => new vscode.RelativePattern(vscode.Uri.file(root), glob);
  if (kind === "dbt") {
    return [
      { scheme: "file", language: "sql", pattern: files("**/*.sql") },
      { scheme: "file", language: "jinja-sql", pattern: files("**/*.sql") },
    ];
  }
  return [
    { scheme: "file", language: "sqlx", pattern: files("**/*.sqlx") },
    { scheme: "file", language: "javascript", pattern: files("**/*.js") },
  ];
}

// startFor starts the client of the project doc belongs to, if it is one
// of the project's files.
async function startFor(doc) {
  if (doc.uri.scheme !== "file") return;
  const p = projectOf(doc.uri.fsPath);
  if (p && vscode.languages.match(p.selector, doc) > 0) await start(p);
}

// start starts xf lsp for project p, once.
async function start(p) {
  if (p.client) return p.client.start();
  const command = xfPath();
  p.client = new LanguageClient(
    "xf",
    `xf (${path.basename(p.root)})`,
    // Over stdio, without the --stdio a transport would add.
    { command, args: ["lsp"], options: { cwd: p.root } },
    {
      documentSelector: p.selector,
      // xf lsp takes the project from the root the client gives it.
      workspaceFolder: { uri: vscode.Uri.file(p.root), name: path.basename(p.root), index: 0 },
      outputChannel: output,
      traceOutputChannel: output,
    },
  );
  output.info(`starting ${command} lsp for the ${p.kind} project ${p.root}`);
  try {
    await p.client.start();
  } catch (err) {
    output.error(`xf lsp for ${p.root}: ${err}`);
  }
}

// projectOf returns the project that holds file, the innermost if they
// nest.
function projectOf(file) {
  let best;
  for (const p of projects.values()) {
    const rel = path.relative(p.root, file);
    if (rel && !rel.startsWith("..") && !path.isAbsolute(rel) && (!best || p.root.length > best.root.length)) {
      best = p;
    }
  }
  return best;
}

// follow tells xf which file the editor made active, so the TUI shows it,
// and so the TUI's o opens files in this window.
function follow(editor, event) {
  if (!editor || editor.document.uri.scheme !== "file") return;
  const file = editor.document.uri.fsPath;
  const p = projectOf(file);
  if (!p || !p.client || !p.client.isRunning()) return;
  p.client.sendNotification("xf/activeFile", { event, file }).catch((err) => output.warn(`xf/activeFile: ${err}`));
}

// running reports whether term's process is still alive.
async function running(term) {
  if (term.exitStatus !== undefined) return false;
  const pid = await term.processId;
  if (!pid) return false;
  try {
    process.kill(pid, 0); // signal 0 only checks that the process exists
    return true;
  } catch {
    return false;
  }
}

// openTUI runs xf in a terminal beside the editor, as the terminal's own
// process, for the project of the active file (or one picked); it focuses
// the one already open for that project.
async function openTUI() {
  let p = vscode.window.activeTextEditor && projectOf(vscode.window.activeTextEditor.document.uri.fsPath);
  if (!p) {
    const all = [...projects.values()];
    if (all.length === 0) {
      vscode.window.showWarningMessage("xf: no Dataform or dbt project in this workspace");
      return;
    }
    if (all.length === 1) {
      p = all[0];
    } else {
      const pick = await vscode.window.showQuickPick(
        all.map((q) => ({ label: path.basename(q.root), description: q.root, project: q })),
        { placeHolder: "Open xf for which project?" },
      );
      if (!pick) return;
      p = pick.project;
    }
  }
  const open = terminals.get(p.root);
  if (open) {
    if (await running(open)) {
      open.show();
      return;
    }
    // Quit (q): a terminal hidden from the user stays listed, with no exit
    // status, after its process exits; show() would show nothing.
    terminals.delete(p.root);
    open.dispose();
  }
  const xf = xfPath();
  if (!fs.existsSync(xf)) {
    vscode.window.showErrorMessage(`xf: ${xf} doesn't exist; set xf.path`);
    return;
  }
  const term = vscode.window.createTerminal({
    name: `xf: ${path.basename(p.root)}`,
    shellPath: xf,
    shellArgs: [],
    cwd: p.root,
    // xf follows this editor rather than opening its palette.
    env: { XF_BESIDE: "1" },
    location: { viewColumn: vscode.ViewColumn.Beside },
    // Extensions that set up new terminals type into them: the Python
    // extensions send their venv's activation command, which xf would read
    // as keys (D, r, a, e: a run and the errors window). They leave alone a
    // terminal hidden from the user, which show() then reveals.
    hideFromUser: true,
    // Not revived after a reload: python-envs activates every terminal open
    // when it starts, hidden or not, and this extension no longer knows a
    // revived one.
    isTransient: true,
  });
  terminals.set(p.root, term);
  term.show();
}

module.exports = { activate, deactivate };
