// SBX Desktop — thin Electron shell.
//
// All product logic lives in the Go daemon (sbxd); this window is just a
// desktop-grade frame around http://127.0.0.1:<port>. If the daemon is not
// reachable it is booted inside WSL via wsl.exe (forever-running, like
// Docker Desktop's backend), then the window opens.
//
// Env overrides:
//   SBXD_PORT         dashboard port            (default 7770)
//   SBXD_WSL_DISTRO   wsl.exe distro name       (default: auto-detect)
//   SBXD_BIN          path to sbxd inside WSL   (default ~/OpenCode-SBX/sbxd/bin/sbxd)
'use strict';

const { app, BrowserWindow, Menu, dialog, shell } = require('electron');
const http = require('http');
const { spawn, execFile } = require('child_process');

const PORT = process.env.SBXD_PORT || '7770';
const DASH = 'http://127.0.0.1:' + PORT;
const SBXD_IN_WSL = process.env.SBXD_BIN || '~/OpenCode-SBX/sbxd/bin/sbxd';

let win = null;

/* ---------- tiny helpers ---------- */

function httpOK(url, ms) {
  return new Promise((resolve) => {
    let done = false;
    const settle = (ok) => { if (!done) { done = true; resolve(ok); } };
    const req = http.get(url, { timeout: ms }, (res) => {
      res.resume();
      settle(res.statusCode === 200);
    });
    req.on('timeout', () => { req.destroy(); settle(false); });
    req.on('error', () => settle(false));
  });
}

function waitFor(url, seconds) {
  return new Promise(async (resolve) => {
    const deadline = Date.now() + seconds * 1000;
    for (;;) {
      if (await httpOK(url + '/api/health', 1500)) return resolve(true);
      if (Date.now() > deadline) return resolve(false);
      await new Promise((r) => setTimeout(r, 500));
    }
  });
}

/* ---------- WSL plumbing ---------- */

function wslDistros() {
  return new Promise((resolve) => {
    execFile('wsl.exe', ['--list', '--quiet'], { timeout: 8000, windowsHide: true },
      (err, stdout) => {
        if (err) return resolve([]);
        // wsl.exe prints UTF-16LE; strip NULs and BOM artifacts
        let s = String(stdout).replace(/\u0000/g, '').trim();
        if (s.startsWith('\uFEFF') || s.startsWith('\uFFFE')) s = s.slice(1);
        resolve(s.split(/\r?\n/).map((l) => l.trim()).filter(Boolean));
      });
  });
}

function startDaemon(dist) {
  const cmd =
    'nohup ' + SBXD_IN_WSL + ' serve --port ' + PORT +
    ' >/tmp/sbxd.log 2>&1 & disown; echo sbx-started';
  return new Promise((resolve) => {
    const child = spawn('wsl.exe', ['-d', dist, '--exec', '/bin/bash', '-lc', cmd],
      { stdio: 'ignore', windowsHide: true });
    child.on('error', () => resolve(false));
    child.on('exit', (code) => resolve(code === 0));
  });
}

function stopDaemon(dist) {
  spawn('wsl.exe', ['-d', dist, '--exec', '/bin/bash', '-lc',
    "pkill -f 'sbxd serve' || true"], { windowsHide: true }).unref();
}

/* ---------- app lifecycle ---------- */

const gotLock = app.requestSingleInstanceLock();
if (!gotLock) {
  app.quit();
} else {
  app.on('second-instance', () => {
    if (win) { win.isMinimized() && win.restore(); win.focus(); }
  });

  app.whenReady().then(async () => {
    let ok = await waitFor(DASH, 2);
    if (!ok) {
      const dists = await wslDistros();
      if (dists.length === 0) {
        dialog.showErrorBox('SBX Desktop',
          'wsl.exe not reachable — start the daemon manually:\n\n  sbxd serve\n\nthen reopen SBX Desktop.');
        return app.quit();
      }
      const dist = process.env.SBXD_WSL_DISTRO || dists[0];
      await startDaemon(dist);
      ok = await waitFor(DASH, 20);
    }
    if (!ok) {
      dialog.showErrorBox('SBX Desktop',
        'The sbxd daemon did not come up on ' + DASH + '.\n\nInspect it inside WSL:\n  tail /tmp/sbxd.log\n  sbxd doctor\n\nThen reopen SBX Desktop.');
      return app.quit();
    }
    openWindow();
  });
}

function openWindow() {
  win = new BrowserWindow({
    width: 1240,
    height: 820,
    minWidth: 980,
    minHeight: 640,
    backgroundColor: '#14161a',
    title: 'SBX Desktop',
    autoHideMenuBar: true,
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true },
  });
  win.loadURL(DASH);
  let retries = 0;
  win.webContents.on('did-fail-load', () => {
    if (retries++ < 5) setTimeout(() => win.loadURL(DASH), 2000);
  });
  win.on('closed', () => { win = null; });
}

function buildMenu() {
  const menu = Menu.buildFromTemplate([
    { label: 'SBX Desktop',
      submenu: [
        { label: 'Stop sbxd daemon in WSL', click: async () => {
            const dist = process.env.SBXD_WSL_DISTRO || (await wslDistros())[0] || 'kali-linux';
            stopDaemon(dist);
            setTimeout(() => win && win.loadURL(DASH).catch(() => {}), 400);
          } },
        { type: 'separator' },
        { role: 'quit', label: 'Quit' },
      ] },
    { label: 'View',
      submenu: [
        { role: 'reload' }, { role: 'forceReload' },
        { type: 'separator' },
        { role: 'toggleDevTools', label: 'Toggle developer tools' },
        { type: 'separator' },
        { role: 'zoomIn' }, { role: 'zoomOut' }, { role: 'resetZoom' },
      ] },
    { label: 'Help',
      submenu: [
        { label: 'Open dashboard in browser', click: () => shell.openExternal(DASH) },
        { label: 'CLI help (sbxd --help)', click: () =>
            shell.openExternal('https://github.com/chris5011/OpenCode-SBX#sbxd--desktop-ui') },
        { label: 'Daemon log (/tmp/sbxd.log in WSL)', click: () =>
            dialog.showMessageBox(win, { message: 'Daemon stdout/err lives in WSL:\n  tail -f /tmp/sbxd.log',
              detail: 'Also useful: sbxd doctor' }) },
      ] },
  ]);
  Menu.setApplicationMenu(menu);
}

app.on('window-all-closed', () => app.quit());
