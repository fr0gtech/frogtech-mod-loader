# Contributing

Issues and pull requests are welcome. This page is for working on the code.

## Layout

| path | what |
|---|---|
| `cmd/frogloader` | `main`: flags, logging, wiring the packages together |
| `internal/app` | the loader: state, every operation, background loops (gameinfo watcher, update checks), self-update, `frogmod://` handling, repositories |
| `internal/server` | the local HTTP API on 127.0.0.1 (token + Host check) and the page, `web/index.html` (plain HTML/CSS/JS, embedded, no build step) |
| `internal/winui` | Windows UI: frameless WebView2 window with the page's title bar, tray icon, notifications |
| `internal/platform` | OS-specific bits: game process check, opening links, registry (Run key, `frogmod://`), message boxes |
| `internal/mods` | `state.json`, the mod store, archive extraction, writing enabled mods as `pakNN_dir.vpk` |
| `internal/catalog` | the Frogtech catalog and the third-party repository format |
| `internal/gamebanana` | GameBanana API v11: items, files, browsing |
| `internal/steam`, `internal/gameinfo` | finding Deadlock; keeping `citadel/addons` in `gameinfo.gi` |
| `internal/fetch`, `internal/secrets` | HTTP and downloads (repository keys only go to their own origin); DPAPI key storage |
| `internal/links`, `internal/fsutil`, `internal/icon` | link parsing, file helpers, the frog icon |
| `internal/testutil`, `internal/testdata` | shared test helpers and fixtures |
| `tools/winres`, `tools/pack` | build tools: Windows resources (icon, manifest, version info), the release zip |

## Develop and test

You need Go 1.23.12 (`.go-version`); everything is pure Go (no cgo). Windows-only code lives in `*_windows.go` files with stubs elsewhere, so it all builds and tests on Linux or macOS:

```
go vet ./... && GOOS=windows go vet ./...
go test ./...            # add -race for the race detector
go run ./cmd/frogloader --background   # Linux/macOS: logs the page URL; open it in a browser
```

Ground rules for changes:
- Keep it dependency-light and pure Go.
- Anything that touches the game folder keeps its safety rules: only `pakNN_dir.vpk` files are written or removed, files the loader didn't write are never deleted silently, and every download is checksummed.
- UI copy is sentence case, written from the player's point of view.

## Build and release

`./build.sh [version]` vets, tests and writes `dist/FrogLoader.exe`, `dist/FrogLoader_v<version>.zip`, `dist/THIRD_PARTY_NOTICES.txt` and `dist/SHA256SUMS`.
- The build is reproducible: `-trimpath`, no VCS stamp or build id, fixed resource text, and a zip with fixed timestamps.
- If you change dependencies, copy `dist/THIRD_PARTY_NOTICES.txt` to the repository root. The release workflow checks that the two match.

To release:
1. Set `VERSION` and the default `version` in `cmd/frogloader/main.go`.
2. Add `notes/<version>.txt`, one line per change. It becomes the release text and the in-app "What's new".
3. Commit, tag `v<version>` and push the tag. `.github/workflows/release.yml` builds, attests and publishes the release.
4. Point `loader` in the Frogtech catalog at the new zip and its SHA-256.
