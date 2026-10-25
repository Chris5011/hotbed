# SBX Desktop — the Electron shell

This folder is the *thin* desktop wrapper. All logic lives in the Go daemon
(`sbxd`); the shell only opens a window on `http://127.0.0.1:7770` and
bootstraps the daemon inside WSL if it is not running (like Docker Desktop
does with its backend).

## Run (Windows side — native, no WSLg)

1. Copy this folder to an NTFS path (npm on `\\wsl$` shares is flaky):

   ```powershell
   robocopy "\\wsl$\kali-linux\home\<you>\OpenCode-SBX\sbxd\desktop" "$env:USERPROFILE\sbx-desktop" /E
   ```

2. Install + start (Node ≥ 18 required):

   ```powershell
   cd ~\sbx-desktop
   npm install
   npm start
   ```

The window shows the identical dashboard the browser would (`/api/*`,
`/static/*`, SSE) — it stays a plain webapp, so it also runs in any browser
or inside WSLg if you ever want it there.

## Behavior notes

- The daemon persists in WSL after the window closes (`sbxd serve` stays up,
  `/tmp/sbxd.log`). Menu → *SBX Desktop → Stop sbxd daemon in WSL* stops it.
- Starting the app twice focuses the existing window (single-instance).
- Env overrides: `SBXD_PORT`, `SBXD_WSL_DISTRO`, `SBXD_BIN` — see `main.js`.
- `F12` toggles devtools (View menu too).

## Why not wails/tauri/native?

Kept out of the P1 scope deliberately: the Go binary must stay dependency-free
and single-file. The Electron shell is ~200 disposable lines that can be
replaced later without touching any product logic.
