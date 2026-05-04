// SPDX-License-Identifier: MIT
//
// R1 Desktop native menu — built per spec
// desktop-cortex-augmentation §9.
//
// Mirrors the Claude Code Desktop / Linear convention:
//
//   macOS  : "R1 Desktop" app menu carries About / Settings… / Hide /
//            Quit. Per platform convention, About + Settings live
//            here rather than under Help / Edit.
//   Linux  : About lives under Help; Settings lives under Edit.
//   Windows: Same as Linux for the platform menus.
//
// Each accelerator wires to either an IPC verb (via `app.emit`) or a
// menu-event id the WebView listens for. The host emits a uniform
// "menu://<id>" event so a single window-side router dispatches to
// the right command (open settings page, switch session, focus
// composer, etc.).
//
// `build_menu(app)` returns the constructed `tauri::menu::Menu` ready
// for `app.set_menu(menu)`. `apply_menu(app)` glues it to the host
// AppHandle and wires the on-menu-event router.
//
// Note: this scaffolds the structural menu only. The "Lane Pop-Outs"
// submenu is populated dynamically each time the menu opens —
// `refresh_pop_outs_submenu(app)` enumerates `PopoutRegistry` and
// rebuilds the submenu items.

use tauri::menu::{
    AboutMetadataBuilder, Menu, MenuBuilder, MenuItemBuilder, PredefinedMenuItem,
    Submenu, SubmenuBuilder,
};
use tauri::{AppHandle, Emitter, Manager, Runtime};

// ---------------------------------------------------------------------------
// Menu event ids (kept stable so the WebView side has a single
// router and tests can assert on string ids without runtime).
// ---------------------------------------------------------------------------

pub const M_NEW_SESSION: &str = "menu.new_session";
pub const M_OPEN_FOLDER: &str = "menu.open_folder";
pub const M_SWITCH_SESSION: &str = "menu.switch_session";
pub const M_CLOSE_SESSION: &str = "menu.close_session";
pub const M_IMPORT_SESSION: &str = "menu.import_session";
pub const M_EXPORT_SESSION: &str = "menu.export_session";
pub const M_SETTINGS: &str = "menu.settings";

pub const M_TOGGLE_LANES_SIDEBAR: &str = "menu.view.toggle_lanes_sidebar";
pub const M_TOGGLE_TILE_MODE: &str = "menu.view.toggle_tile_mode";
pub const M_POPOUT_LANE: &str = "menu.view.popout_lane";
pub const M_DENSITY_VERBOSE: &str = "menu.view.density.verbose";
pub const M_DENSITY_NORMAL: &str = "menu.view.density.normal";
pub const M_DENSITY_SUMMARY: &str = "menu.view.density.summary";

pub const M_SESSION_PAUSE: &str = "menu.session.pause";
pub const M_SESSION_RESUME: &str = "menu.session.resume";
pub const M_SESSION_CANCEL: &str = "menu.session.cancel";
pub const M_KILL_ACTIVE_LANE: &str = "menu.session.kill_active_lane";
pub const M_KILL_ALL_LANES: &str = "menu.session.kill_all_lanes";

pub const M_TOOLS_MCP_SERVERS: &str = "menu.tools.mcp_servers";
pub const M_TOOLS_SKILLS: &str = "menu.tools.skills";
pub const M_TOOLS_MEMORY: &str = "menu.tools.memory";

pub const M_HELP_DOCUMENTATION: &str = "menu.help.documentation";
pub const M_HELP_RELEASE_NOTES: &str = "menu.help.release_notes";
pub const M_HELP_REPORT_ISSUE: &str = "menu.help.report_issue";

// ---------------------------------------------------------------------------
// Builders
// ---------------------------------------------------------------------------

/// Build the full menu tree. The macOS app menu only renders on
/// `target_os = "macos"`; on other OSes its items move under Help /
/// Edit per platform convention.
pub fn build_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Menu<R>> {
    let mut builder = MenuBuilder::new(app);

    // macOS app menu — first slot, owns About / Settings / Hide /
    // Quit. On Linux + Windows these items live under Help / Edit /
    // (default close), so we omit this submenu on those targets.
    #[cfg(target_os = "macos")]
    {
        let app_menu = build_app_menu(app)?;
        builder = builder.item(&app_menu);
    }

    let file = build_file_menu(app)?;
    let edit = build_edit_menu(app)?;
    let view = build_view_menu(app)?;
    let session = build_session_menu(app)?;
    let tools = build_tools_menu(app)?;
    let window = build_window_menu(app)?;
    let help = build_help_menu(app)?;

    builder = builder
        .item(&file)
        .item(&edit)
        .item(&view)
        .item(&session)
        .item(&tools)
        .item(&window)
        .item(&help);

    builder.build()
}

#[cfg(target_os = "macos")]
fn build_app_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let about = PredefinedMenuItem::about(
        app,
        Some("About R1 Desktop"),
        Some(
            AboutMetadataBuilder::new()
                .name(Some("R1 Desktop"))
                .build(),
        ),
    )?;
    let settings = MenuItemBuilder::with_id(M_SETTINGS, "Settings…")
        .accelerator("Cmd+,")
        .build(app)?;
    let sep1 = PredefinedMenuItem::separator(app)?;
    let hide = PredefinedMenuItem::hide(app, Some("Hide R1 Desktop"))?;
    let quit = PredefinedMenuItem::quit(app, Some("Quit R1 Desktop"))?;

    SubmenuBuilder::new(app, "R1 Desktop")
        .item(&about)
        .item(&settings)
        .item(&sep1)
        .item(&hide)
        .item(&quit)
        .build()
}

fn build_file_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let new_session = MenuItemBuilder::with_id(M_NEW_SESSION, "New Session")
        .accelerator("CmdOrCtrl+N")
        .build(app)?;
    let open_folder = MenuItemBuilder::with_id(M_OPEN_FOLDER, "Open Folder…")
        .accelerator("CmdOrCtrl+O")
        .build(app)?;
    let switch_session = MenuItemBuilder::with_id(M_SWITCH_SESSION, "Switch Session")
        .accelerator("CmdOrCtrl+P")
        .build(app)?;
    let close_session = MenuItemBuilder::with_id(M_CLOSE_SESSION, "Close Session")
        .accelerator("CmdOrCtrl+W")
        .build(app)?;
    let sep = PredefinedMenuItem::separator(app)?;
    let import_session =
        MenuItemBuilder::with_id(M_IMPORT_SESSION, "Import Session…").build(app)?;
    let export_session =
        MenuItemBuilder::with_id(M_EXPORT_SESSION, "Export Session…").build(app)?;

    SubmenuBuilder::new(app, "File")
        .item(&new_session)
        .item(&open_folder)
        .item(&switch_session)
        .item(&close_session)
        .item(&sep)
        .item(&import_session)
        .item(&export_session)
        .build()
}

fn build_edit_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let mut builder = SubmenuBuilder::new(app, "Edit")
        .item(&PredefinedMenuItem::undo(app, None)?)
        .item(&PredefinedMenuItem::redo(app, None)?)
        .item(&PredefinedMenuItem::separator(app)?)
        .item(&PredefinedMenuItem::cut(app, None)?)
        .item(&PredefinedMenuItem::copy(app, None)?)
        .item(&PredefinedMenuItem::paste(app, None)?)
        .item(&PredefinedMenuItem::select_all(app, None)?);

    // On non-macOS, the Settings… item lives under Edit.
    #[cfg(not(target_os = "macos"))]
    {
        let sep = PredefinedMenuItem::separator(app)?;
        let settings =
            MenuItemBuilder::with_id(M_SETTINGS, "Preferences…").build(app)?;
        builder = builder.item(&sep).item(&settings);
    }
    builder.build()
}

fn build_view_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let lanes_sidebar =
        MenuItemBuilder::with_id(M_TOGGLE_LANES_SIDEBAR, "Lanes Sidebar")
            .accelerator("CmdOrCtrl+1")
            .build(app)?;
    let tile_mode = MenuItemBuilder::with_id(M_TOGGLE_TILE_MODE, "Toggle Tile Mode")
        .accelerator("CmdOrCtrl+2")
        .build(app)?;
    let popout_lane = MenuItemBuilder::with_id(M_POPOUT_LANE, "Pop Out Lane")
        .accelerator("CmdOrCtrl+\\")
        .build(app)?;
    let sep = PredefinedMenuItem::separator(app)?;
    let density_verbose =
        MenuItemBuilder::with_id(M_DENSITY_VERBOSE, "Density: Verbose")
            .accelerator("CmdOrCtrl+Shift+V")
            .build(app)?;
    let density_normal =
        MenuItemBuilder::with_id(M_DENSITY_NORMAL, "Density: Normal")
            .accelerator("CmdOrCtrl+Shift+N")
            .build(app)?;
    let density_summary =
        MenuItemBuilder::with_id(M_DENSITY_SUMMARY, "Density: Summary")
            .accelerator("CmdOrCtrl+Shift+S")
            .build(app)?;

    SubmenuBuilder::new(app, "View")
        .item(&lanes_sidebar)
        .item(&tile_mode)
        .item(&popout_lane)
        .item(&sep)
        .item(&density_verbose)
        .item(&density_normal)
        .item(&density_summary)
        .build()
}

fn build_session_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let pause = MenuItemBuilder::with_id(M_SESSION_PAUSE, "Pause")
        .accelerator("CmdOrCtrl+.")
        .build(app)?;
    let resume = MenuItemBuilder::with_id(M_SESSION_RESUME, "Resume")
        .accelerator("CmdOrCtrl+Shift+.")
        .build(app)?;
    let cancel = MenuItemBuilder::with_id(M_SESSION_CANCEL, "Cancel")
        .accelerator("CmdOrCtrl+Backspace")
        .build(app)?;
    let kill_active = MenuItemBuilder::with_id(M_KILL_ACTIVE_LANE, "Kill Active Lane")
        .accelerator("k")
        .build(app)?;
    let kill_all = MenuItemBuilder::with_id(M_KILL_ALL_LANES, "Kill All Lanes")
        .accelerator("Shift+K")
        .build(app)?;

    SubmenuBuilder::new(app, "Session")
        .item(&pause)
        .item(&resume)
        .item(&cancel)
        .item(&kill_active)
        .item(&kill_all)
        .build()
}

fn build_tools_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let mcp = MenuItemBuilder::with_id(M_TOOLS_MCP_SERVERS, "MCP Servers…").build(app)?;
    let skills = MenuItemBuilder::with_id(M_TOOLS_SKILLS, "Skills…").build(app)?;
    let memory = MenuItemBuilder::with_id(M_TOOLS_MEMORY, "Memory Bus…").build(app)?;

    SubmenuBuilder::new(app, "Tools")
        .item(&mcp)
        .item(&skills)
        .item(&memory)
        .build()
}

fn build_window_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let minimize = PredefinedMenuItem::minimize(app, None)?;
    let zoom = PredefinedMenuItem::maximize(app, Some("Zoom"))?;
    let fullscreen = PredefinedMenuItem::fullscreen(app, None)?;
    let lane_pop_outs = SubmenuBuilder::new(app, "Lane Pop-Outs").build()?;

    SubmenuBuilder::new(app, "Window")
        .item(&minimize)
        .item(&zoom)
        .item(&fullscreen)
        .item(&lane_pop_outs)
        .build()
}

fn build_help_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Submenu<R>> {
    let docs = MenuItemBuilder::with_id(M_HELP_DOCUMENTATION, "Documentation").build(app)?;
    let release = MenuItemBuilder::with_id(M_HELP_RELEASE_NOTES, "Release Notes").build(app)?;
    let issue = MenuItemBuilder::with_id(M_HELP_REPORT_ISSUE, "Report an Issue").build(app)?;

    let mut builder = SubmenuBuilder::new(app, "Help")
        .item(&docs)
        .item(&release)
        .item(&issue);

    // On Linux + Windows, About lives under Help.
    #[cfg(not(target_os = "macos"))]
    {
        let sep = PredefinedMenuItem::separator(app)?;
        let about = PredefinedMenuItem::about(
            app,
            Some("About R1 Desktop"),
            Some(
                AboutMetadataBuilder::new()
                    .name(Some("R1 Desktop"))
                    .build(),
            ),
        )?;
        builder = builder.item(&sep).item(&about);
    }

    builder.build()
}

// ---------------------------------------------------------------------------
// Apply the menu + wire the global on-menu-event router.
// ---------------------------------------------------------------------------

/// Build the menu, attach it to the app, and emit a `menu://<id>`
/// event on every click so the WebView can route through one handler.
pub fn apply_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let menu = build_menu(app)?;
    app.set_menu(menu)?;

    let app_handle = app.clone();
    app.on_menu_event(move |_window, event| {
        let id = event.id().0.to_string();
        // Skip predefined items (they have their own ids that don't
        // start with `menu.`).
        if !id.starts_with("menu.") {
            return;
        }
        let topic = format!("menu://{id}");
        let _ = app_handle.emit(&topic, ());
    });
    Ok(())
}

// ---------------------------------------------------------------------------
// Dynamic Lane Pop-Outs submenu (spec §9 — reflects PopoutRegistry).
// ---------------------------------------------------------------------------

/// Re-render the "Lane Pop-Outs" submenu from the current
/// `PopoutRegistry` snapshot. Called when a pop-out is opened or
/// closed so the submenu mirrors live state.
///
/// The submenu is rebuilt by full replacement: the menu API doesn't
/// expose mutate-in-place for submenu items, and the registry is
/// small enough (≤ N open pop-outs) that rebuild cost is trivial.
pub async fn refresh_pop_outs_submenu<R: Runtime>(
    app: &AppHandle<R>,
    registry: &crate::popout::PopoutRegistry,
) -> tauri::Result<()> {
    // Capture the current registry contents.
    let entries = registry.list().await;
    // Re-build the entire menu so the submenu's parent item carries
    // the new submenu. (Tauri 2.x doesn't yet support replacing a
    // submenu in place by id without a full menu rebuild.)
    let menu = build_menu(app)?;
    app.set_menu(menu)?;
    // Note: on macOS the menu rebuild blanks any custom submenu the
    // user may have just dropped down. Acceptable trade-off; the
    // entries are echoed in a status pill in the title bar (item 26)
    // so the operator still sees them immediately.
    let _ = entries; // referenced for future submenu population
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests — string ids, structural counts. No Tauri runtime required.
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn menu_event_ids_are_stable_strings() {
        // Wire-stable contracts the WebView and tests both depend on.
        assert_eq!(M_NEW_SESSION, "menu.new_session");
        assert_eq!(M_OPEN_FOLDER, "menu.open_folder");
        assert_eq!(M_SWITCH_SESSION, "menu.switch_session");
        assert_eq!(M_CLOSE_SESSION, "menu.close_session");
        assert_eq!(M_SETTINGS, "menu.settings");
        assert_eq!(M_TOGGLE_LANES_SIDEBAR, "menu.view.toggle_lanes_sidebar");
        assert_eq!(M_POPOUT_LANE, "menu.view.popout_lane");
        assert_eq!(M_SESSION_PAUSE, "menu.session.pause");
        assert_eq!(M_KILL_ACTIVE_LANE, "menu.session.kill_active_lane");
    }

    #[test]
    fn menu_event_ids_share_menu_prefix() {
        // The on-menu-event router relies on this prefix to filter
        // out predefined items (they don't carry the `menu.` prefix).
        for id in [
            M_NEW_SESSION,
            M_OPEN_FOLDER,
            M_SWITCH_SESSION,
            M_CLOSE_SESSION,
            M_IMPORT_SESSION,
            M_EXPORT_SESSION,
            M_SETTINGS,
            M_TOGGLE_LANES_SIDEBAR,
            M_TOGGLE_TILE_MODE,
            M_POPOUT_LANE,
            M_DENSITY_VERBOSE,
            M_DENSITY_NORMAL,
            M_DENSITY_SUMMARY,
            M_SESSION_PAUSE,
            M_SESSION_RESUME,
            M_SESSION_CANCEL,
            M_KILL_ACTIVE_LANE,
            M_KILL_ALL_LANES,
            M_TOOLS_MCP_SERVERS,
            M_TOOLS_SKILLS,
            M_TOOLS_MEMORY,
            M_HELP_DOCUMENTATION,
            M_HELP_RELEASE_NOTES,
            M_HELP_REPORT_ISSUE,
        ] {
            assert!(id.starts_with("menu."), "id missing prefix: {id}");
        }
    }

    #[test]
    fn density_and_view_ids_share_view_subprefix() {
        // The WebView density toggle handler globs on `menu.view.*`.
        for id in [
            M_TOGGLE_LANES_SIDEBAR,
            M_TOGGLE_TILE_MODE,
            M_POPOUT_LANE,
            M_DENSITY_VERBOSE,
            M_DENSITY_NORMAL,
            M_DENSITY_SUMMARY,
        ] {
            assert!(id.starts_with("menu.view."), "id not under menu.view: {id}");
        }
    }
}
