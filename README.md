<p align="center"><img src="icon/icon.svg" width="96" alt="War Table shield icon"></p>

# War Table

A World of Warcraft companion overlay for **Midnight** (patch 12.1.5). It sits on top of the game or on a second monitor, tracks your week automatically through a small addon, and has an AI assistant you can ask mid-run.

Built for Protection Warrior, Restoration Shaman and Fire Mage, each with its own theme.

## Features

- **Week tab:** Great Vault progress for Raid, Dungeons and World, your keystone, rating, Mythic+ runs and raid lockouts, all synced from the game. Plus a weekly checklist that resets at Tuesday reset.
- **Spec tab:** patch changes, core habits, tier set bonuses and your own notes per spec.
- **Ask tab:** an assistant that knows your characters' progress and can look up live hotfixes and patch notes.
- **Overlay:** stays on top of WoW (Windowed Fullscreen), adjustable opacity, opens on whichever screen you left it on. `Ctrl+Alt+W` hides or shows it.
- **Tray icon:** hover for the reset countdown; click to show or hide.
- **Class themes:** Warrior (Forge), Shaman (Tidecaller), Fire Mage (Pyre).
- **Auto-updates** from this repo's releases.

## Install

1. Download the latest `WarTable-vX.Y.Z.zip` from [Releases](https://github.com/ahoule710/war-table/releases/latest) and unzip it anywhere you like (Documents works well).
2. Run `WarTable.exe`. Windows SmartScreen may warn because the app isn't code-signed: **More info**, then **Run anyway**.
3. Click **Install addon** on the setup card, then log in or `/reload` in WoW.
4. Add your Anthropic API key in Settings to use the Ask tab.

After that, the app updates itself. When a new release is out you'll see **Update ready** under the tabs.

## How it works

| Piece | What it does |
|---|---|
| `addon/WarTableCompanion` | WoW addon. Saves Vault, Mythic+, lockouts, item level and spec to SavedVariables, out of combat only. |
| `WarTable.exe` | Small Go app. Reads the addon's data, serves the UI on `127.0.0.1`, opens it in a Chrome (or Edge) app window, and manages the overlay, tray and hotkey. |
| `ui/index.html` | The interface, embedded in the exe. |

WoW only writes addon data to disk on `/reload` or logout, so that's when the app sees new progress.

## Privacy and security

- Your characters, checklist and notes stay on your PC in `%APPDATA%\WarTable`. Nothing is sent to this repo.
- The API key is encrypted with Windows DPAPI under your login and is only sent to `api.anthropic.com`.
- The local server only listens on `127.0.0.1`, checks the Host header, and requires a random per-launch token, so other websites can't call it.
- Updates are downloaded only from GitHub and installed only if their SHA-256 matches the release's `SHA256SUMS.txt`.

## Build from source

Requires Go 1.24+.

```sh
go test ./...
GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui -s -w" -o WarTable.exe .
```

The icon and version info come from `rsrc_windows_amd64.syso`, generated from `icon/wartable.rc`:

```sh
llvm-rc /FO wartable.res icon/wartable.rc
llvm-cvtres /machine:x64 /out:rsrc_windows_amd64.syso wartable.res
```

## Releasing

1. Set the new version in `VERSION` (for example `1.2.1`) and add a matching section to `CHANGELOG.md`.
2. Push to `main`.
3. The Release workflow sees a version that hasn't been released yet, runs the tests, builds `WarTable.exe`, zips it with the addon, publishes checksums, and creates the tag and GitHub release. Running apps pick it up within a few hours, or right away with **Check now**.
