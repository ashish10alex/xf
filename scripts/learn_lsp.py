#!/usr/bin/env python3
"""
learn_lsp.py — A standalone Python client to interact with `xf lsp`.

Demonstrates how Language Server Protocol (LSP) works under the hood
using raw JSON-RPC 2.0 frames over stdin and stdout.
"""

import json
import os
import subprocess
import sys
import time

class LSPClient:
    def __init__(self, project_root: str):
        self.project_root = os.path.abspath(project_root)
        self.req_id = 0
        
        # Start `xf lsp` as a child process with piped stdin/stdout
        self.proc = subprocess.Popen(
            ["xf", "lsp"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=False
        )

    def send(self, method: str, params: dict = None, is_request: bool = True) -> int:
        """Sends an LSP message with Content-Length framing."""
        payload = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            payload["params"] = params
            
        current_id = None
        if is_request:
            self.req_id += 1
            current_id = self.req_id
            payload["id"] = current_id

        body = json.dumps(payload).encode("utf-8")
        header = f"Content-Length: {len(body)}\r\n\r\n".encode("utf-8")
        
        self.proc.stdin.write(header + body)
        self.proc.stdin.flush()
        return current_id

    def read_message(self) -> dict:
        """Reads one JSON-RPC message from stdout."""
        content_length = None
        while True:
            line = self.proc.stdout.readline().decode("utf-8")
            if not line:
                return None
            if line in ("\r\n", "\n"):
                break
            if line.lower().startswith("content-length:"):
                content_length = int(line.split(":")[1].strip())

        if content_length is None:
            return None
            
        body = self.proc.stdout.read(content_length).decode("utf-8")
        return json.loads(body)

    def wait_for_response(self, request_id: int, timeout: float = 10.0) -> dict:
        """Waits for the response matching request_id, printing any notifications."""
        start_time = time.time()
        while time.time() - start_time < timeout:
            msg = self.read_message()
            if not msg:
                break
            # Handle incoming notifications (e.g., diagnostics)
            if "method" in msg:
                print(f"  📢 [Notification from Server: {msg['method']}]")
                continue
            # Match response ID
            if msg.get("id") == request_id:
                return msg.get("result")
        return None

    def initialize(self) -> dict:
        """Step 1: Handshake with server."""
        print(f"\n🤝 1. Sending 'initialize' request for root: {self.project_root}...")
        req_id = self.send("initialize", {
            "rootUri": f"file://{self.project_root}",
            "capabilities": {
                "general": {"positionEncodings": ["utf-8", "utf-16"]}
            }
        })
        res = self.wait_for_response(req_id)
        
        # Must notify server that initialization is complete
        self.send("initialized", {}, is_request=False)
        return res

    def open_file(self, file_path: str):
        """Step 2: Tell server a file was opened in the editor."""
        abs_path = os.path.abspath(file_path)
        print(f"\n📂 2. Sending 'textDocument/didOpen' for: {os.path.basename(abs_path)}...")
        with open(abs_path, "r", encoding="utf-8") as f:
            text = f.read()

        lang = "sqlx" if abs_path.endswith(".sqlx") else "sql"
        self.send("textDocument/didOpen", {
            "textDocument": {
                "uri": f"file://{abs_path}",
                "languageId": lang,
                "version": 1,
                "text": text
            }
        }, is_request=False)

    def get_document_symbols(self, file_path: str) -> list:
        """Step 3: Ask for outline / symbols."""
        abs_path = os.path.abspath(file_path)
        print(f"\n📑 3. Sending 'textDocument/documentSymbol'...")
        req_id = self.send("textDocument/documentSymbol", {
            "textDocument": {"uri": f"file://{abs_path}"}
        })
        return self.wait_for_response(req_id)

    def get_definition(self, file_path: str, line: int, col: int) -> list:
        """Step 4: Go-to-definition lookup at 0-indexed line and col."""
        abs_path = os.path.abspath(file_path)
        print(f"\n🎯 4. Sending 'textDocument/definition' at Line {line + 1}, Col {col + 1}...")
        req_id = self.send("textDocument/definition", {
            "textDocument": {"uri": f"file://{abs_path}"},
            "position": {"line": line, "character": col}
        })
        return self.wait_for_response(req_id)

    def get_hover(self, file_path: str, line: int, col: int) -> dict:
        """Step 5: Hover metadata / BigQuery schema."""
        abs_path = os.path.abspath(file_path)
        print(f"\n🔍 5. Sending 'textDocument/hover' at Line {line + 1}, Col {col + 1}...")
        req_id = self.send("textDocument/hover", {
            "textDocument": {"uri": f"file://{abs_path}"},
            "position": {"line": line, "character": col}
        })
        return self.wait_for_response(req_id)

    def close(self):
        """Clean shutdown."""
        try:
            self.send("shutdown", {})
            self.send("exit", {}, is_request=False)
        except Exception:
            pass
        self.proc.terminate()


def main():
    root = os.path.abspath("examples/dataform")
    target_file = os.path.join(root, "definitions/marts/dim_customers.sqlx")

    print("=" * 65)
    print(" 🚀 Python LSP Client for `xf lsp` (Dataform & dbt)")
    print("=" * 65)

    client = LSPClient(project_root=root)
    try:
        # 1. Initialize
        init_info = client.initialize()
        caps = init_info.get("capabilities", {})
        print("  ✅ Server capabilities registered:")
        print(f"     • Definition Provider: {caps.get('definitionProvider')}")
        print(f"     • Hover Provider:      {caps.get('hoverProvider')}")
        print(f"     • Trigger Characters:  {caps.get('completionProvider', {}).get('triggerCharacters')}")

        # 2. Open document
        client.open_file(target_file)

        # 3. Outline symbols
        symbols = client.get_document_symbols(target_file)
        print("  ✅ Document Symbols found in file:")
        for s in symbols or []:
            print(f"     • {s.get('name')} [{s.get('detail')}] at line {s.get('range', {}).get('start', {}).get('line') + 1}")

        # 4. Go to definition on ${ref("stg_orders")} (Line 30 in editor -> index 29, col 16)
        locs = client.get_definition(target_file, line=29, col=16)
        print("  ✅ Go-to-Definition Jump Target:")
        for loc in locs or []:
            uri = loc.get("uri", "")
            print(f"     ➜ {os.path.relpath(uri.replace('file://', ''))}")

        # 5. Hover on ${ref("stg_orders")}
        hover = client.get_hover(target_file, line=29, col=16)
        print("  ✅ Hover Tooltip Content:")
        raw_val = hover.get("contents", {}).get("value", "") if hover else ""
        for line in raw_val.split("\n")[:14]:  # Show first 14 lines
            print(f"     {line}")
        if len(raw_val.split("\n")) > 14:
            print("     ... [truncated schema rows] ...")

        print("\n🎉 Everything completed successfully!")
    finally:
        client.close()

if __name__ == "__main__":
    main()
