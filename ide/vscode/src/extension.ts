// extension.ts — VS Code extension entrypoint for r1-agent.
//
// Three commands are registered:
//   r1.chat.open           opens a webview chat backed by /api/task
//   r1.run.task            quick-prompt + submit + stream into output
//   r1.explain.selection   submit current selection + filename
//
// Daemon contract: ../PROTOCOL.md.

import * as vscode from "vscode";
import { DaemonClient, DaemonError, TaskRequestBody } from "./daemon";

const OUTPUT_CHANNEL_NAME = "R1 Agent";

let outputChannel: vscode.OutputChannel | undefined;

function getOutput(): vscode.OutputChannel {
  if (!outputChannel) {
    outputChannel = vscode.window.createOutputChannel(OUTPUT_CHANNEL_NAME);
  }
  return outputChannel;
}

// Build a fresh DaemonClient from the live workspace config so users
// don't have to reload the window after editing settings.
export function buildClient(): DaemonClient {
  const cfg = vscode.workspace.getConfiguration("r1");
  return new DaemonClient({
    baseUrl: cfg.get<string>("daemonUrl", "http://127.0.0.1:7777"),
    apiKey: cfg.get<string>("apiKey", ""),
    timeoutMs: cfg.get<number>("timeoutMs", 120_000)
  });
}

function defaultTaskType(): string {
  return vscode.workspace.getConfiguration("r1").get<string>("taskType", "explain");
}

function describeError(err: unknown): string {
  if (err instanceof DaemonError) {
    return `${err.message} (HTTP ${err.status})`;
  }
  if (err instanceof Error) {
    return err.message;
  }
  return String(err);
}

// ----------------------------------------------------------------------------
// Command: r1.run.task
// ----------------------------------------------------------------------------

export async function runTaskCommand(): Promise<void> {
  const objective = await vscode.window.showInputBox({
    prompt: "Describe the task for r1-agent",
    placeHolder: "e.g. refactor the auth middleware to drop legacy tokens",
    ignoreFocusOut: true
  });
  if (!objective || objective.trim().length === 0) {
    return;
  }
  const out = getOutput();
  out.show(true);
  out.appendLine(`> task: ${objective}`);
  await withProgress(`R1: running task`, async (progress) => {
    const client = buildClient();
    const body: TaskRequestBody = {
      task_type: defaultTaskType(),
      description: objective,
      extra: { source: "vscode", command: "r1.run.task" }
    };
    try {
      progress.report({ message: "submitting..." });
      const state = await client.submitTask(body);
      out.appendLine(`< status=${state.status} id=${state.id}`);
      if (state.summary) {
        out.appendLine(state.summary);
      }
      if (state.error) {
        out.appendLine(`! error: ${state.error}`);
      }
    } catch (err) {
      const msg = describeError(err);
      out.appendLine(`! ${msg}`);
      vscode.window.showErrorMessage(`R1: ${msg}`);
    }
  });
}

// ----------------------------------------------------------------------------
// Command: r1.explain.selection
// ----------------------------------------------------------------------------

export async function explainSelectionCommand(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) {
    vscode.window.showWarningMessage("R1: open a file and select some text first.");
    return;
  }
  const text = editor.document.getText(editor.selection);
  if (!text || text.trim().length === 0) {
    vscode.window.showWarningMessage("R1: selection is empty.");
    return;
  }
  const out = getOutput();
  out.show(true);
  out.appendLine(`> explain ${editor.document.fileName} (${text.length} chars selected)`);
  await withProgress(`R1: explaining selection`, async () => {
    const client = buildClient();
    const body: TaskRequestBody = {
      task_type: defaultTaskType(),
      description: `Explain the following code from ${editor.document.fileName}`,
      query: text,
      extra: {
        source: "vscode",
        command: "r1.explain.selection",
        filename: editor.document.fileName,
        languageId: editor.document.languageId
      }
    };
    try {
      const state = await client.submitTask(body);
      out.appendLine(`< status=${state.status}`);
      if (state.summary) {
        out.appendLine(state.summary);
      }
      if (state.error) {
        out.appendLine(`! error: ${state.error}`);
      }
    } catch (err) {
      const msg = describeError(err);
      out.appendLine(`! ${msg}`);
      vscode.window.showErrorMessage(`R1: ${msg}`);
    }
  });
}

// ----------------------------------------------------------------------------
// Command: r1.chat.open
// ----------------------------------------------------------------------------

let chatPanel: vscode.WebviewPanel | undefined;

export async function openChatCommand(context: vscode.ExtensionContext): Promise<void> {
  if (chatPanel) {
    chatPanel.reveal(vscode.ViewColumn.Beside);
    return;
  }
  chatPanel = vscode.window.createWebviewPanel(
    "r1Chat",
    "R1 Chat",
    vscode.ViewColumn.Beside,
    { enableScripts: true, retainContextWhenHidden: true }
  );
  chatPanel.webview.html = renderChatHtml();
  chatPanel.onDidDispose(() => {
    chatPanel = undefined;
  }, null, context.subscriptions);

  chatPanel.webview.onDidReceiveMessage(async (msg: { type: string; text?: string }) => {
    if (msg.type !== "send" || !msg.text) {
      return;
    }
    const client = buildClient();
    try {
      const state = await client.submitTask({
        task_type: defaultTaskType(),
        description: msg.text,
        extra: { source: "vscode-chat" }
      });
      chatPanel?.webview.postMessage({
        type: "reply",
        ok: state.status === "completed",
        body: state.summary || state.error || `(no body, status=${state.status})`
      });
    } catch (err) {
      chatPanel?.webview.postMessage({
        type: "reply",
        ok: false,
        body: describeError(err)
      });
    }
  }, null, context.subscriptions);
}

function renderChatHtml(): string {
  // Single-file webview. Plain CSS, no external assets, so the
  // panel works offline and behind strict CSP.
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline';">
<title>R1 Chat</title>
<style>
  body { font-family: var(--vscode-font-family); background: var(--vscode-editor-background); color: var(--vscode-foreground); margin: 0; padding: 12px; }
  #log { white-space: pre-wrap; height: calc(100vh - 120px); overflow-y: auto; border: 1px solid var(--vscode-panel-border); padding: 8px; border-radius: 4px; }
  .row { margin-top: 8px; display: flex; gap: 6px; }
  textarea { flex: 1; min-height: 60px; background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border); padding: 6px; font-family: var(--vscode-font-family); }
  button { background: var(--vscode-button-background); color: var(--vscode-button-foreground); border: none; padding: 8px 14px; cursor: pointer; }
  .me { color: var(--vscode-textLink-foreground); }
  .err { color: var(--vscode-errorForeground); }
</style>
</head>
<body>
<div id="log"></div>
<div class="row">
  <textarea id="prompt" aria-label="Type a message for r1-agent and press Send"></textarea>
  <button id="send">Send</button>
</div>
<script>
  const vscode = acquireVsCodeApi();
  const log = document.getElementById('log');
  const prompt = document.getElementById('prompt');
  const send = document.getElementById('send');
  function append(cls, who, text) {
    const div = document.createElement('div');
    div.className = cls;
    div.textContent = '[' + who + '] ' + text;
    log.appendChild(div);
    log.scrollTop = log.scrollHeight;
  }
  send.addEventListener('click', () => {
    const text = prompt.value.trim();
    if (!text) return;
    append('me', 'you', text);
    prompt.value = '';
    vscode.postMessage({ type: 'send', text });
  });
  window.addEventListener('message', (event) => {
    const msg = event.data;
    if (msg.type === 'reply') {
      append(msg.ok ? '' : 'err', msg.ok ? 'r1' : 'error', msg.body);
    }
  });
</script>
</body>
</html>`;
}

// ----------------------------------------------------------------------------
// Activation
// ----------------------------------------------------------------------------

function withProgress<T>(title: string, fn: (p: vscode.Progress<{ message?: string }>) => Promise<T>): Thenable<T> {
  return vscode.window.withProgress(
    { location: vscode.ProgressLocation.Notification, title, cancellable: false },
    fn
  );
}

export function activate(context: vscode.ExtensionContext): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("r1.chat.open", () => openChatCommand(context)),
    vscode.commands.registerCommand("r1.run.task", () => runTaskCommand()),
    vscode.commands.registerCommand("r1.explain.selection", () => explainSelectionCommand())
  );
}

export function deactivate(): void {
  if (chatPanel) {
    chatPanel.dispose();
    chatPanel = undefined;
  }
  if (outputChannel) {
    outputChannel.dispose();
    outputChannel = undefined;
  }
}
