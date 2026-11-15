// Drives a scripted debug session in VS Code and logs every DAP message it
// exchanges with the adapter, for comparison with dap-debugger.
//
// PROBE_SCRIPT is a scenario file (see internal/ui/testdata/vscode/*.json):
//   config               the launch configuration, as for dap-debugger
//   configs              several configurations started together, as a
//                        compound starts them, in place of config
//   vscode               fields that replace config's for VS Code, if any
//   breakpoints          [{file, line, condition?, hitCondition?, logMessage?}]
//   functionBreakpoints  [{name}]
//   steps                one action per stop: stepOver, stepInto, stepOut,
//                        continue, continueThenPause, restart or stop
//   server               {command, ready}: a shell command started first, in
//                        the workspace, whose output matches ready once it
//                        is listening (for attach scenarios); it runs with
//                        VIRTUAL_ENV set to the repository's .venv, the
//                        directory of run.sh's dlv appended to PATH and
//                        PROBE_TMP set to a temporary directory
// "{port}" anywhere in a scenario is replaced with a free port.
const vscode = require("vscode");
const fs = require("fs");
const net = require("net");
const path = require("path");
const cp = require("child_process");

const out = process.env.PROBE_OUT;
// run.sh passes the .venv as PROBE_VENV because the login shell's environment,
// which VS Code merges over run.sh's, may set its own VIRTUAL_ENV or PYTHONPATH.
const pythonEnv = { VIRTUAL_ENV: process.env.PROBE_VENV, PYTHONPATH: "", PYTHONHOME: "" };
const log = [];
const disposeOnFinish = [];
let finished = false;

function finish(error) {
  if (finished) return;
  finished = true;
  for (const d of disposeOnFinish) d();
  fs.writeFileSync(out, JSON.stringify({ error: error ? String(error) : null, log }, null, 1));
  setTimeout(() => vscode.commands.executeCommand("workbench.action.quit"), 300);
}

function freePort() {
  return new Promise((resolve) => {
    const s = net.createServer().listen(0, "127.0.0.1", () => {
      const p = s.address().port;
      s.close(() => resolve(p));
    });
  });
}

const commands = {
  stepOver: "workbench.action.debug.stepOver",
  stepInto: "workbench.action.debug.stepInto",
  stepOut: "workbench.action.debug.stepOut",
  continue: "workbench.action.debug.continue",
  restart: "workbench.action.debug.restart",
  stop: "workbench.action.debug.stop",
};

async function activate(context) {
  if (!out) return;
  // {port} in a scenario is a free port, for attach scenarios.
  const port = await freePort();
  const script = JSON.parse(fs.readFileSync(process.env.PROBE_SCRIPT, "utf8").replaceAll("{port}", String(port)));
  if (script.vscode && script.vscode.debugServer) script.vscode.debugServer = Number(script.vscode.debugServer);
  const folder = vscode.workspace.workspaceFolders[0];

  // Delve and debugpy registered under the probe's own debug types, so no other extension is needed.
  context.subscriptions.push(vscode.debug.registerDebugAdapterDescriptorFactory("probe-go", {
    async createDebugAdapterDescriptor(session) {
      const port = await freePort();
      const cwd = session.configuration.program || folder.uri.fsPath;
      const dlv = cp.spawn(process.env.PROBE_DLV, ["dap", `--listen=127.0.0.1:${port}`], { cwd, stdio: ["ignore", "pipe", "pipe"] });
      context.subscriptions.push({ dispose: () => dlv.kill() });
      // Connecting to probe readiness would use up dlv dap's only client.
      await new Promise((resolve) => {
        dlv.stdout.on("data", (d) => { if (String(d).includes("listening")) resolve(); });
        setTimeout(resolve, 10000);
      });
      return new vscode.DebugAdapterServer(port, "127.0.0.1");
    },
  }));
  // Like the Python extension, a configuration with connect (such as a
  // subprocess session, which debugpy starts as type debugpy) connects to a
  // running adapter instead of starting one.
  const python = {
    createDebugAdapterDescriptor(session) {
      const connect = session.configuration.connect;
      if (connect) return new vscode.DebugAdapterServer(connect.port, connect.host || "127.0.0.1");
      return new vscode.DebugAdapterExecutable(path.join(process.env.PROBE_VENV, "bin", "python"), ["-m", "debugpy.adapter"], { env: pythonEnv });
    },
  };
  context.subscriptions.push(vscode.debug.registerDebugAdapterDescriptorFactory("probe-python", python));
  context.subscriptions.push(vscode.debug.registerDebugAdapterDescriptorFactory("debugpy", python));

  const steps = [...script.steps];
  let lastStep = null;
  context.subscriptions.push(vscode.debug.registerDebugAdapterTrackerFactory("*", {
    createDebugAdapterTracker(session) {
      return {
        onWillReceiveMessage: (m) => log.push({ session: session.name, direction: "client", message: m }),
        onDidSendMessage: (m) => {
          log.push({ session: session.name, direction: "adapter", message: m });
          if (m.type === "event" && m.event === "stopped" && steps.length > 0) {
            const step = (lastStep = steps.shift());
            // Give VS Code time to load the stack and variables, as a user would.
            setTimeout(() => {
              if (step === "continueThenPause") {
                vscode.commands.executeCommand(commands.continue);
                setTimeout(() => vscode.commands.executeCommand("workbench.action.debug.pause"), 1500);
              } else if (step === "stop" && script.config && script.config.request === "attach") {
                // Shift+F5 disconnects from attached sessions in VS Code.
                vscode.commands.executeCommand("workbench.action.debug.disconnect");
              } else {
                vscode.commands.executeCommand(commands[step]);
              }
            }, 1500);
          }
        },
        onError: (e) => log.push({ direction: "error", message: String(e) }),
      };
    },
  }));
  // Finish once the last step has run and every session has ended; a
  // restart starts a new session.
  let live = 0;
  context.subscriptions.push(vscode.debug.onDidStartDebugSession(() => live++));
  context.subscriptions.push(vscode.debug.onDidTerminateDebugSession(() => {
    live--;
    setTimeout(() => {
      if (live === 0 && steps.length === 0 && lastStep !== "restart") finish();
    }, 1500);
  }));

  const types = { go: "probe-go", debugpy: "probe-python", python: "probe-python" };
  const toUri = (file) => vscode.Uri.file(path.join(folder.uri.fsPath, file));
  vscode.debug.addBreakpoints([
    ...(script.breakpoints || []).map((b) => new vscode.SourceBreakpoint(
      new vscode.Location(toUri(b.file), new vscode.Position(b.line - 1, 0)), true, b.condition, b.hitCondition, b.logMessage)),
    ...(script.functionBreakpoints || []).map((f) => new vscode.FunctionBreakpoint(f.name, true, f.condition)),
  ]);

  if (script.server) {
    const env = { ...process.env, ...pythonEnv, PROBE_TMP: fs.mkdtempSync(path.join(require("os").tmpdir(), "probe-")) };
    if (process.env.PROBE_DLV) env.PATH = env.PATH + path.delimiter + path.dirname(process.env.PROBE_DLV);
    const server = cp.spawn("/bin/sh", ["-c", script.server.command], {
      cwd: folder.uri.fsPath, detached: true, stdio: ["ignore", "pipe", "pipe"], env,
    });
    const kill = () => { try { process.kill(-server.pid, "SIGKILL"); } catch (e) {} };
    disposeOnFinish.push(kill);
    process.on("exit", kill);
    await new Promise((resolve) => {
      let output = "";
      const watch = (d) => { output += d; if (new RegExp(script.server.ready).test(output)) resolve(); };
      server.stdout.on("data", watch);
      server.stderr.on("data", watch);
      setTimeout(resolve, 60000);
    });
  }

  const configs = script.configs || [{ ...script.config, ...(script.vscode || {}) }];
  try {
    for (const c of configs) {
      const config = { ...c, type: types[c.type] || c.type };
      if (!(await vscode.debug.startDebugging(folder, config))) finish("startDebugging returned false");
    }
  } catch (e) {
    finish(e);
  }
  setTimeout(() => finish("timed out"), 90000);
}

module.exports = { activate, deactivate() {} };
