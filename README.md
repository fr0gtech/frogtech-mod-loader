# Frogtech Mod Loader

A mod loader and auto-updater for [Deadlock](https://store.steampowered.com/app/1422450/Deadlock/), for Windows. One small app that:

![Frogtech Mod Loader: Browse > Frogtech](docs/screenshot.png)

**Download:** [mods.frogtech.dev/mod-loader](https://mods.frogtech.dev/mod-loader). The same files are attached to this repository's [Releases](https://github.com/fr0gtech/frogtech-mod-loader/releases).

## Is it safe?

- **It's open source:** this repository is all of it. MIT license.
- **GitHub builds every release** from this code with [`.github/workflows/release.yml`](.github/workflows/release.yml). You can see the build log on the Actions tab.
- **Every release has a signed provenance attestation**, which proves the file was built by that workflow from this repository. Check a download with the [GitHub CLI](https://cli.github.com/):

  ```
  gh attestation verify FrogLoader_v0.5.0.zip --repo fr0gtech/frogtech-mod-loader
  ```

  Or compare its SHA-256 with `SHA256SUMS` on the release page. In PowerShell: `Get-FileHash FrogLoader_v0.5.0.zip`.
- **Builds are reproducible:** building the same tag yourself with `./build.sh` gives byte-identical files (see below).
- **The app only reaches the internet for** mods.frogtech.dev, GameBanana's public API, and repositories you add yourself. Repository keys stay with the repository's own server.

### Why Windows may warn you

The exe isn't code-signed yet (a certificate costs money every year), so SmartScreen may say "Windows protected your PC" for a new release until enough people have run it: click **More info → Run anyway**. Defender's machine-learning checks can also be wary of brand-new unsigned programs. If you'd rather not trust a pre-built file, the attestation above shows exactly which commit it came from, or you can build it yourself.

## What it does

- **Keeps mods enabled.** Game updates reset `game/citadel/gameinfo.gi`, which turns mods off. The loader checks the file every 5 seconds and adds the `citadel/addons` search path back when it's missing.
- **Frogtech mods** come from `https://mods.frogtech.dev/loader/catalog.json`, and every download's sha256 is checked.
- **Browse GameBanana** (Mods and Sounds for Deadlock) inside the app: search, sort, categories, thumbnails, details and one-click install. GameBanana installs auto-update.
- **Local files and links:** drop a `.zip`, `.rar`, `.7z` or `.vpk` on the window, or paste a GameBanana link.
- **Owns the pak numbering** in `game/citadel/addons`. Enabled mods are written as `pak01_dir.vpk`, `pak02_dir.vpk`, … in your order (lower number wins).
- **Auto-updates** mods and the loader every hour, only while Deadlock is closed.
- **Play button:** fixes gameinfo, applies pending changes, then opens `steam://rungameid/1422450`.
- **`frogmod://` links** from the website install Frogtech mods or open GameBanana mods in the app.

## frogmod:// links

On every start the loader registers the URL protocol for the current user, rewriting it if the exe has moved:

| key (under `HKCU\Software\Classes\frogmod`) | value |
|---|---|
| (default) | `URL:Frogtech Mod Loader` |
| `URL Protocol` | `""` |
| `DefaultIcon` | `"<exe>",0` |
| `shell\open\command` | `"<exe>" --url "%1"` |

Links:

| link | what happens |
|---|---|
| `frogmod://install/<catalog id>` (for example `uikit`, `aspect_4x3`, `steam_profiles`, `hero_select_plus`) | installs or updates that Frogtech mod and enables it |
| `frogmod://gamebanana/<mod id>`, `frogmod://gamebanana/sound/<id>` | opens the item's details in Browse, with Install |

- If the loader is already running, the new process forwards the link to it (`POST /api/url` with the instance token) and exits.
- The window comes to the front and shows the result as a toast, for example "UI Kit 0.8.1 installed."
- While Deadlock is running, the mod goes into the store and the toast says it will be applied when the game closes.
- Bad or unknown links show an error toast.
- Parsing is in `url.go` and tested in `url_test.go`.

## The window

Launching `FrogLoader.exe` opens a native window ("Frogtech Mod Loader", frog icon, about 1100x800, resizable, size and position remembered).

The window is a Win32 window that hosts **WebView2**, the Edge engine that comes with Windows 10/11. It shows the embedded page (`web/index.html`), which the exe serves on `127.0.0.1:47621`, or a free port if that one is taken. There's no browser and no URL to remember.
- External links (GameBanana pages, mod pages) open in the default browser.
- Dev tools, the browser context menu and browser shortcuts (F5, Ctrl+P, …) are turned off.

Closing the window:
- If "Start with Windows" is on (the default), or the loader was started with `--background`, the loader keeps running in the tray. The WebView2 is torn down (its Edge processes exit) and **Open** in the tray builds a new window.
- Otherwise closing the window quits the loader.

Launching the exe again while it's running brings the existing window to the front, or reopens it. If the WebView2 Runtime is missing, a message box offers its download page (`https://developer.microsoft.com/microsoft-edge/webview2/`).

WebView2 binding: `github.com/wailsapp/go-webview2` (`pkg/edge` plus its **pure-Go** WebView2 loader), with our own small Win32 window in `window_windows.go`. We deliberately don't use `github.com/jchv/go-webview2`: it embeds `WebView2Loader.dll` and maps it from memory (go-winloader), which looks like a classic dropper technique to antivirus heuristics. The exe contains no embedded DLL.

Start with Windows: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` gets `FrogLoader` = `"<exe>" --background` (a toggle, on by default). The tray menu has Open, Check for updates and Quit.

Data lives in `%LOCALAPPDATA%\FrogLoader\`:

| path | what |
|---|---|
| `state.json` | settings, game folder, window rect, mods (id, source, name, version, enabled, order, files, paks) and the paks last written |
| `mods\<id>\*.vpk` | stored copy of every mod's VPK(s) |
| `WebView2\` | the window's WebView2 profile |
| `cache\` | preview images of installed and Frogtech mods (newest 400 kept) |
| `loader.log` | log (rotated at 1 MB) |
| `token`, `instance.json` | local API token and the running instance's port and pid |

### Local API security

- The server listens only on 127.0.0.1, and every `/api/` call needs a random token. The window loads `/?t=<token>` once, which sets an HttpOnly, SameSite=Strict cookie.
- Requests whose `Host` is not `127.0.0.1`/`localhost` are refused, and so are cross-origin POSTs.
- The page's CSP only allows our own scripts and GameBanana image and audio hosts.
- GameBanana descriptions are converted to plain text in Go, so no third-party HTML or script ever runs in the window.

## Windows Defender / SmartScreen

0.1.0 was deleted by Defender on a user's PC. Unsigned, freshly built Go GUI executables that download and replace files often trip Defender's machine-learning detections (for example `Trojan:Win32/Wacatac`), even though signature scans are clean. Rebuilding 0.1.0 and scanning it locally also found nothing. What 0.2.0 does about it:

- **Proper PE metadata** (`tools/winres`, pure Go via `github.com/tc-hib/winres`), written into `rsrc_windows_amd64.syso` at build time:
  - Version info: CompanyName "Frogtech", ProductName "Frogtech Mod Loader", FileDescription, File/ProductVersion, OriginalFilename "FrogLoader.exe", InternalName, LegalCopyright.
  - Application manifest: `asInvoker`, per-monitor-v2 DPI aware, supportedOS Windows 10/11, common controls v6, long-path aware.
  - Frog icon resource.
- **Not stripped:** no `-s -w`, so the symbol table and DWARF are kept. **Not packed:** no UPX. Builds use `-trimpath -buildid=` so they're reproducible.
- **No in-memory DLL loading and no embedded DLLs** (see the WebView2 section).
- **Self-update downloads a zip, not a bare exe.** The zip is written next to the exe, never to %TEMP%, its sha256 is checked, and `FrogLoader.exe` is extracted from it. The new exe is renamed into place and started normally, with no hidden-window tricks. A `.exe.old` is left behind and deleted on the next start.
- Helper processes: `rundll32 url.dll,FileProtocolHandler <url>` opens links and `explorer.exe` opens folders. Both run visibly, without hidden-window flags.

Local check: `MpCmdRun.exe -Scan -ScanType 3 -File <FrogLoader.exe>` (Defender's signature scan from the host) found no threats for the 0.2.0 exe and zip. That doesn't prove the cloud/ML protection that reacts on first download or run will accept it.

Remaining options, from most to least effective:

1. **Code signing.**
   - **Azure Trusted Signing** costs about $10/month (Basic). It gives an Authenticode signature Microsoft trusts quickly, and SmartScreen reputation builds up per certificate instead of per file hash.
   - An OV/EV certificate from a CA also works, but costs more and takes more paperwork.
   - Signing works from Linux too: Azure provides a `jsign`-compatible endpoint, and `osslsigncode` handles PFX certificates.
2. **Submit each release to Microsoft** as a false positive at https://www.microsoft.com/en-us/wdsi/filesubmission ("Software developer", choose "Incorrectly detected"). It's free and usually takes a few hours to a few days, but it has to be redone for every build because the hash changes.
3. Keep builds reproducible and stable: don't rebuild an unchanged version, so the hash Microsoft has seen stays the same.

## Look and window

- **One design with mods.frogtech.dev.** The site's design document is the source of truth. The page copies its tokens:
  - neutral greys (ink, coal, slate, line, smoke, chalk);
  - jade only for the brand and "yes" states;
  - brass for versions and key facts;
  - coral for danger;
  - tag colours per kind of mod (catalog `tags`, with built-in defaults for our four mods; GameBanana categories are mapped onto them).
- **Font:** Jost only, with small sentence-case headings: 16px section titles, 15px mod names, 20px for the details title. It's bundled in the exe (`web/fonts`, latin subset, SIL OFL 1.1, license text alongside) and served from `/fonts/`, so the window works offline. The site keeps Big Shoulders Display for its big page titles; the loader has none.
- **Tabs:**
  - Mods shows only your installed mods (update badges included) and the add-a-file/link box.
  - Browse starts on "Frogtech": our catalog as cards with Install, or Installed plus a toggle, and an in-app details view.
  - Browse also has "Mods" and "Sounds" from GameBanana.
  - A first start with nothing installed opens Browse > Frogtech.
- **Frameless window:**
  - The window has no native caption. `WM_NCCALCSIZE` returns 0, and the overlapped styles are kept, so Aero Snap, Win+arrows, the taskbar button and the system menu still work. A maximized window is pulled in by the frame size.
  - The page draws a 44px title bar: the frog (its tooltip shows the name and version), the tabs, the Play button and minimise / maximise / close (close hover is coral). The native window title stays "Frogtech Mod Loader" for the taskbar and Alt+Tab; the name and version also sit at the bottom of Settings. It sends short WebView2 messages (`frog:drag`, `frog:resize:<HT code>`, `frog:max`, `frog:sysmenu`, …).
  - The native side answers drag and resize with `ReleaseCapture` + `WM_NCLBUTTONDOWN` (Windows' own move and size loop) and right-click with the real system menu. It pushes the maximized state back to the page.
  - Edge handles of 5–8px resize the window. `WM_NCHITTEST` covers anything outside the web view.
  - DWM settings: dark mode, caption colour ink, border colour line, rounded corners on Windows 11, and a 1px frame extension for the shadow.
  - The pure parts (hit-testing, message parsing, the page's edge codes) are in `chrome.go` and tested in `chrome_test.go`.
- **Scrollbars:** 8px, thumb #3a3a40 (#4a4a52 on hover), transparent track. The content scrolls under the fixed title bar.
- **Minimum window size:** 800x600. The layout is checked at 1100x800 and 800x600.

## Updates and notifications

- **Tray:** our own Shell_NotifyIcon code in `tray_windows.go` (no third-party tray library). Left click opens the window. Right click shows Open, Check for updates and Quit. The icon is added again if Explorer restarts.
- **Tray "Check for updates":**
  - It installs if auto-update is on and always answers with a Windows notification: "You're up to date…", "Updates found: Loader 0.4.3, UI Kit 0.8.2…", "Mods updated: Updated: …", or "Couldn't check for updates: <reason>".
  - Clicking a notification opens the window on Settings > Updates.
- **Background checks** (hourly) stay quiet unless they installed mod updates. A new loader version is announced once per version when auto-update is off (`state.json` `notifiedLoader`).
- **In-app check** (Settings > Updates, or the Mods tab):
  - "Everything is up to date" toast.
  - A toast listing mod updates with "Update all".
  - For a new loader, a dialog: "Frogtech Mod Loader X is available (you have Y). Update now? The loader restarts.", with the catalog's `loader.notes` as "What's new". The dialog's "Always update the loader automatically" box turns auto-update on.
  - Errors show as error toasts.
- **Settings > Updates:** the current version, the status ("You're up to date · checked 14:05" or "Version X is ready"), Update to X and Check for updates, plus the auto-update toggle.
- **Other indicators:** the Settings tab gets a jade dot while a loader update is waiting, and the Mods tab shows "loader ready" and "N mod updates ready" banners.
- **Failed self-update:** an error toast with the reason. The old exe is restored and keeps running.

## Tags

Tag chips are buttons ("Find more HUD mods") that open Browse on similar mods:
- A GameBanana category, including hero subcategories under Skins, selects that category in Browse > Mods or Sounds.
- Our tags map to the closest GameBanana category: HUD/Editor → HUD, Theme/Add-on → Skins, Social → Sounds, and Menus/New players/Video/Settings/Tool → Quality of Life/Fixes.
- Tags without a match, such as Retired, filter Browse > Frogtech by that tag.
- Local and imported mods have no tags.

## UI notes

- **Dialogs:** there are no native `confirm()`/`alert()` dialogs or message boxes for in-app questions. Questions are styled modals (Esc cancels, Enter confirms).
  - Each kind of question has a "Don't ask again" box, stored in `settings.dontAsk`. The keys are `remove`, `removeSelected`, `import`, `deleteForeign`, `playAnyway` and `quit`.
  - Settings has a button to reset these choices.
  - The only native message boxes left come before a window exists: WebView2 missing, or a fatal start-up error.
- **Installed list:**
  - Each row has a thumbnail, name, source badge, version, enable toggle and a ⋯ menu (update, details, web page, move, remove).
  - Multi-select checkboxes show a bulk bar: enable, disable, remove.
  - The list's ⋯ menu has: enable all, disable all, remove imported mods, remove all local mods, delete foreign pak files.
- **Thumbnails:**
  - Catalog mods use the catalog's `image`; GameBanana installs save the item's first preview image in `state.json` (`image`). Installs from before 0.3.0 are back-filled during the next update check.
  - Local and imported mods get a placeholder tile.
  - Images go through `/img?u=…` (GameBanana and Frogtech hosts only) and are cached on disk.
- **Catalog loading:** the Frogtech catalog loads at start and whenever the window opens (if it's older than 5 minutes). If it fails, as it often does at Windows startup before the network is up, it retries after 1, 2, 4 … 15 minutes instead of waiting an hour.

## How the addons folder is managed

- "Apply" writes every enabled mod's VPK(s) to `addons` as `pakNN_dir.vpk` in list order, and removes paks the loader wrote earlier that are no longer needed. Disabled mods are not copied.
- Every change applies right away. While Deadlock is running, changes stay pending and are applied when the game closes.
- The loader only touches files matching `^pak\d\d_dir\.vpk$`. It records the sha256 of each pak it writes. A pak it didn't write, or one changed since, counts as foreign: applying stops, and the page offers to **import** those files as local mods (moved into the store, never deleted).
- If `addons/.dmm.json` exists, the page warns that Deadlock Mod Manager also manages the folder.
- While `deadlock.exe` is running (checked with a Toolhelp snapshot), applying is refused with "Close Deadlock first".

## The gameinfo.gi fix

Vanilla `gameinfo.gi` contains:

```
		SearchPaths
		{
			...
			Game_UILanguage		citadel_*LANGUAGE*
			Game_LowViolence	citadel_lv

			Game				citadel
			Game				core
		}
```

The loader parses the `SearchPaths { }` block (it ignores comments and tracks brace depth) and inserts `Game citadel/addons` before the first entry that isn't a `Game_*` path. The new line copies the indentation, key/value spacing and line ending (CRLF) of the line it's inserted before.

- If the block already mounts `citadel/addons`, the file is left alone. Deadlock Mod Manager's marked block counts.
- The original is backed up once to `gameinfo.gi.bak`, and the new file is written to a temp file and then renamed.

`gameinfo_test.go` tests this against the real vanilla and DMM-modified files in `testdata/`.

## Finding the game

1. Read the Steam root from `HKLM\SOFTWARE\WOW6432Node\Valve\Steam\InstallPath` and `HKCU\Software\Valve\Steam\SteamPath`.
2. Read every library from `steamapps\libraryfolders.vdf`.
3. Find the library with `appmanifest_1422450.acf`; the game is `<lib>\steamapps\common\<installdir>`.

The build id comes from the manifest's `buildid` key, and `ClientVersion` from `game\citadel\steam.inf`. You can also set the folder by hand in Settings.

## Catalog schema

`GET https://mods.frogtech.dev/loader/catalog.json`:

```json
{"loader": {"version": "0.2.0", "url": "https://mods.frogtech.dev/downloads/FrogLoader_v0.2.0.zip", "sha256": "<sha256 of the zip>",
            "notes": ["What's new, one line each (optional)"]},
 "mods": [{"id": "uikit", "name": "UI Kit", "version": "0.8.0", "game": "6779",
           "url": "https://mods.frogtech.dev/downloads/uikit_v0.8.0.zip", "sha256": "...", "size": 414451,
           "description": "...", "page": "https://mods.frogtech.dev/ui-kit", "conflicts": ["hud_fit"],
           "image": "https://mods.frogtech.dev/img/uikit.png"}]}
```

- **`loader`:** `url` points to a **zip containing `FrogLoader.exe`** at any depth (`build.sh` produces `dist/FrogLoader_v<version>.zip`), and `sha256` is the zip's hash. 0.2.0 also accepts a bare exe at `url`.
  - **Warning for 0.1.0 installs:** 0.1.0 treats whatever `loader.url` serves as the new exe. If the catalog points it at a zip, 0.1.0 replaces itself with a zip and stops starting. Keep `loader` out of the catalog until 0.1.0 users have updated by hand (it was out for a day), or point `loader.url` at the bare exe until then.
- **Mods:**
  - `id`: `[A-Za-z0-9._-]`, at most 64 characters.
  - `version`: dotted numbers, compared numerically.
  - `game`: the ClientVersion the build targets. The UI shows "built for N" when it differs from the installed game.
  - `sha256`: required.
  - `conflicts`: ids that clash with this mod.
  - `image`: an absolute URL of a preview image.
  - The zip holds `*.vpk` files (usually `pak01_dir.vpk`) and an optional readme. Each VPK becomes one pak slot.

## Repositories

Other modders, including ones who sell mods, can ship updates through the loader. Frogtech's catalog is the built-in default repository and can't be removed (it can be disabled); users can add more in Settings > Repositories. Disabling Frogtech's repository hides its mods from Browse and stops their updates; loader updates still come from it.

**Repository format** (also published at <https://mods.frogtech.dev/docs/repositories>):
- A repository is one JSON document at an https URL. http is allowed only for localhost and 127.0.0.1, for modders testing.
- It uses the same schema as https://mods.frogtech.dev/loader/catalog.json, with these extra top-level fields:

```json
{
  "format": 1,
  "name": "Example Mods",
  "description": "One line about this repository",
  "homepage": "https://example.com",
  "icon": "https://example.com/icon.png",
  "mods": [ { "id": "cool_hud", "name": "Cool HUD", "version": "1.2.0", "url": "https://example.com/dl/cool_hud_1.2.0.zip",
              "sha256": "…", "size": 12345, "description": "…", "image": "https://…", "tags": ["HUD"], "page": "https://…",
              "conflicts": [], "game": "6779", "notes": ["what changed in this version"] } ]
}
```

- `format`, `name` and `mods` are required. Each mod needs `id`, `name`, `version`, `url` and `sha256`; everything else is optional.
- `id` is unique within the repository and matches `^[a-z0-9_-]{1,64}$`.
- `version` is compared like ours (numeric dot parts).
- `url` points to a .zip (with *_dir.vpk inside, or several) or a bare .vpk.
- The `loader` key is only honoured from the built-in Frogtech repository. It is ignored elsewhere.
- Unknown fields are ignored.

The loader checks all of this when it fetches a repository (`ParseRepo` in `repos.go`). A file that breaks a rule shows "Invalid file" with the reason, and the loader also requires https (or localhost http) for mod URLs.

**Keys** (optional, for paid or private repositories):
- When adding a repository the user can enter a key. The loader then sends `Authorization: Bearer <key>` on the catalog request, and on mod downloads whose URL has the same origin (scheme, host and port) as the repository URL.
- The key is never sent to any other origin. Signed download URLs on other hosts (S3 and so on) work without the key.
  - The check is repeated on every redirect: a redirect to another origin drops the header.
- A 401 or 403 shows "Key not accepted" on the repository. The server may answer differently per key, for example listing only the mods that key owns.
- Keys are stored with Windows DPAPI (CryptProtectData, current user) in state.json. They're never shown in plain text again after saving: the page shows "•••• last 4", with Change and Remove.
  - On other systems (development and tests) a stand-in obfuscates them.

**In the loader:**
- **Settings > Repositories** lists each repository with:
  - its name, URL, mod count and last fetch;
  - a status: Working, Key not accepted, Can't reach it, or Invalid file with the reason;
  - an enable toggle, remove, and the key (add, change or remove).
- **Add repository** asks for the URL and an optional key, then shows a preview (name, description, mod count) before the user confirms. A warning reads "Only add repositories from people you trust: their mods run in your game."
- **Installed ids** are `<repoId>/<modId>`. The repoId comes from the repository URL: host (with port) plus a hash of the path, e.g. `mods.example.com-1a2b3c4d`, and it stays stable. Frogtech's mods keep their plain ids.
- **Browse:** each enabled repository is a source after Frogtech, with its name and icon, and uses the same cards and details view (including the mod's `notes` as "What's new"). Cards from third-party repositories say "from <repo name>".
- **Updates:** update checks and auto-update cover every enabled repository. A failing repository never blocks the others, and the tray check names the repositories it couldn't reach.
- **Deep links:**
  - `frogmod://repo/add?url=<urlencoded>` opens the Add repository dialog, prefilled and already checked. It is never added silently.
  - `frogmod://install/<repoId>/<modId>` installs from a repository the user has already added. For an unknown repository it explains that the repository needs adding first.
- **Downloads:** sha256 is required and verified. The same VPK validation, path checks and size cap apply as for every other download.

## GameBanana

Deadlock is game **20948** on GameBanana: `GET /apiv11/Game/20948` returns `"_sName": "Deadlock"`, and Deadlock Mod Manager uses the same id. Mods and Sounds are separate "models" with their own ids and endpoints. All calls go straight to GameBanana's public API v11 with the User-Agent `FrogLoader/<version>`. List and detail replies are cached in memory for 5 minutes, and categories for 1 hour.

| use | endpoint (base `https://gamebanana.com/apiv11`) |
|---|---|
| browse | `/<Mod or Sound>/Index?_aFilters[Generic_Game]=20948&_aFilters[Generic_Category]=<cat>&_sSort=<sort>&_nPage=<n>&_nPerpage=24`, where `<sort>` is one of `Generic_MostLiked`, `Generic_MostDownloaded`, `Generic_MostViewed`, `Generic_Newest`, `Generic_LatestUpdated` |
| search | `/Util/Search/Results?_sModelName=<Mod or Sound>&_idGameRow=20948&_sSearchString=<q>&_sOrder=<best_match, popularity, date or udate>&_nPage=<n>` (15 per page; it can't be combined with a category) |
| categories | `/<Model>/Categories?_idGameRow=20948&_sSort=a_to_z&_bShowEmpty=true`, then `?_idCategoryRow=<id>` for subcategories (Skins has one per hero) |
| details | `/<Model>/<id>/ProfilePage`: images, `_sText` (HTML, shown as plain text), `_aFiles`, `_nDownloadCount`, `_nLikeCount`, `_nViewCount`, `_aContentRatings`, `_sInitialVisibility`; Sounds also have `_aPreviewMedia._aMetadata._sAudioUrl` |
| install / update check | `/<Model>/<id>?_csvProperties=_sName,_aFiles,_tsDateModified,_tsDateUpdated,_aGame,_aSubmitter,_sProfileUrl` (plus `_sVersion` for Mods; Sounds reject that field) |

List records carry `_idRow`, `_sName`, `_aSubmitter._sName`, `_aPreviewMedia._aImages[]` (`_sBaseUrl` + `_sFile220/530/800`), `_aRootCategory`, `_aSubCategory`, `_nLikeCount`, `_nViewCount`, `_tsDateAdded`, `_tsDateUpdated`, `_sVersion`, `_sInitialVisibility` and `_bHasContentRatings`. They don't include download counts, so the details view shows those.

- **Mature content:** items GameBanana marks `_sInitialVisibility` = `hide` or `warn` (the same rule DMM uses) are blurred unless Settings → "Show mature GameBanana content" is on.
- **Downloads:**
  - The default file is the newest non-archived file. Picking an older file in the details view pins the mod, so it won't auto-update.
  - Downloads are checked against `_sMd5Checksum`. Files whose `_sAvResult` isn't `clean` are refused.
  - Updates are detected when the newest file's `_idRow` changes.
- **Archives:**
  - `.zip`, `.rar` (`github.com/nwaples/rardecode/v2`) and `.7z` (`github.com/bodgit/sevenzip`) are all read in pure Go. Every `*.vpk` inside is installed. Multi-chunk VPKs (`*_000.vpk`) and encrypted archives are rejected.
  - Of the 40 newest Deadlock mods, 41 files were zip, 6 rar and 5 7z.

For comparison, Deadlock Mod Manager (github.com/deadlock-mod-manager/deadlock-mod-manager) browses through its own backend, `https://api.deadlockmods.app`. That backend mirrors GameBanana by polling the same `apiv11/Mod/Index`, `ProfilePage` and `DownloadPage` endpoints, with `DEADLOCK_GAME_ID = 20948`. We don't use their service.

## Build it yourself

You need [Go](https://go.dev/dl/) **1.23.12**, the version in `.go-version` (any OS; the exe is cross-compiled). Nothing else is needed.

```
git clone https://github.com/fr0gtech/frogtech-mod-loader
cd frogtech-mod-loader
./build.sh            # vet (Linux + Windows), tests, then dist/FrogLoader.exe, dist/FrogLoader_v<VERSION>.zip,
                      # dist/THIRD_PARTY_NOTICES.txt and dist/SHA256SUMS
./build.sh 0.5.0      # explicit version (the release workflow passes the tag)
GO=/path/to/go ./build.sh  # a specific go binary
```

What `build.sh` does:
1. `go run ./tools/winres` writes `rsrc_windows_amd64.syso`: icon, manifest and version info. It's pure Go and never committed.
2. It builds with `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-H windowsgui -buildid= -X main.version=$V"`.

**Reproducible:** with the same Go version, building a tag gives the same SHA-256 as the release.
- `-trimpath` removes the local paths and `-buildvcs=false` / `-buildid=` remove the per-checkout stamps.
- The resource file uses fixed text (no build date), and `tools/pack` writes the zip with fixed timestamps and Go's own deflate.
- A different Go version changes the hash, so `build.sh` warns if yours differs from `.go-version`.

Tests run anywhere with `go test ./...`. Windows-only code is behind build tags (`*_windows.go`; `platform_other.go` and `secrets_other.go` stand in elsewhere).

**Releasing:**
1. Bump `VERSION` and `main.version`'s default.
2. Write `notes/<version>.txt`, one line per change. It becomes the release body and the in-app "What's new".
3. Tag `v<version>` and push. The workflow builds, attests and publishes.
4. Then update `loader` in the Frogtech catalog to point at the release zip and its sha256.

## Contributing

Issues and pull requests are welcome.
- Please run `go vet ./...`, `GOOS=windows go vet ./...` and `go test ./...` before sending a change.
- Keep the app dependency-light and pure Go (no cgo).
- Anything that touches the game folder must keep its safety rules: only `pakNN_dir.vpk` files, never delete files the loader didn't write, checksums on every download.
- UI changes go in `web/index.html`, which is plain HTML, CSS and JS with no build step.

## License

MIT, see [LICENSE](LICENSE). Copyright (c) 2026 Frogtech.

Third-party code compiled into the exe; the full texts ship in `THIRD_PARTY_NOTICES.txt` with every release:

| module | license |
|---|---|
| github.com/wailsapp/go-webview2 | MIT |
| golang.org/x/sys, golang.org/x/text | BSD-3-Clause |
| github.com/nwaples/rardecode/v2 | BSD-2-Clause |
| github.com/bodgit/sevenzip, bodgit/plumbing, bodgit/windows | BSD-3-Clause |
| github.com/klauspost/compress | BSD-3-Clause (parts Apache-2.0, MIT) |
| github.com/ulikunitz/xz, github.com/pierrec/lz4/v4 | BSD-3-Clause |
| github.com/andybalholm/brotli | MIT |
| github.com/spf13/afero, go4.org | Apache-2.0 |
| github.com/hashicorp/golang-lru/v2 | MPL-2.0 (file-level copyleft: unmodified, source at its upstream) |

- **Build tool only, not shipped:** github.com/tc-hib/winres (0BSD-style ISC terms), plus golang.org/x/image and github.com/nfnt/resize (BSD-3-Clause and ISC).
- **Font:** Jost (SIL Open Font License 1.1, `web/fonts/OFL-Jost.txt`).
- All of these licenses are compatible with releasing the loader under MIT.

## Files

| file | contents |
|---|---|
| `build.sh`, `.go-version`, `.github/workflows/release.yml` | reproducible build and the GitHub release (with provenance) |
| `main.go` | flags, single instance, logging, startup |
| `app.go` | operations: install, add, toggle, order, remove, import, apply, play, settings, window state |
| `steam.go` | VDF parser, Steam library and game discovery, steam.inf |
| `gameinfo.go` | SearchPaths parser and fix |
| `mods.go` | state.json, VPK extraction (zip, rar, 7z), pak apply and import |
| `catalog.go` | catalog, version compare, verified downloads |
| `gamebanana.go` | GameBanana items, files and updates (Mods and Sounds) |
| `url.go` | frogmod:// parsing and dispatch, page events, catalog refresh |
| `imgcache.go` | image proxy and on-disk cache |
| `repos.go`, `secrets*.go` | repositories: format checks, keys (DPAPI), fetching, installs |
| `browse.go` | GameBanana browse, search, categories, details and the response cache |
| `update.go` | background loops, self-update from a zip |
| `server.go` | local HTTP API, token and host guard, status |
| `window_windows.go` | native window plus WebView2 |
| `platform_*.go`, `tray_windows.go` | registry, process check, links, tray icon and notifications |
| `icon.go`, `internal/icon` | the frog icon (exe resource, window, tray, favicon) |
| `tools/winres` | generates the .syso (icon, manifest, version info) |
| `web/index.html` | the UI (embedded) |
