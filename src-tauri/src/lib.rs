use std::{
    error::Error,
    ffi::OsString,
    fs,
    io::{Read, Write},
    net::{TcpListener, TcpStream, ToSocketAddrs},
    path::{Path, PathBuf},
    process::Command as StdCommand,
    sync::Mutex,
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use tauri::{path::BaseDirectory, Manager, RunEvent, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_shell::{
    process::{CommandChild, CommandEvent},
    ShellExt,
};

const BACKEND_HOST: &str = "127.0.0.1";
const BACKEND_PORT: u16 = 8787;
const APP_URL: &str = "http://localhost:8787/";
const HEALTH_PATH: &str = "/api/health";
const SIDECAR_NAME: &str = "ai-analyst-server";
const STARTUP_TIMEOUT: Duration = Duration::from_secs(30);
const HEALTH_CONNECT_TIMEOUT: Duration = Duration::from_millis(500);
const HEALTH_READ_TIMEOUT: Duration = Duration::from_millis(1000);
const LEGACY_APP_IDENTIFIER: &str = "com.balancedscorpion.aianalyst";

struct BackendProcess(Mutex<Option<CommandChild>>);

impl BackendProcess {
    fn stop(&self) {
        let Ok(mut child) = self.0.lock() else {
            return;
        };
        if let Some(child) = child.take() {
            let _ = child.kill();
        }
    }
}

impl Drop for BackendProcess {
    fn drop(&mut self) {
        self.stop();
    }
}

pub fn run() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_shell::init())
        .setup(|app| {
            terminate_stale_backend_listeners();
            ensure_desktop_port_available()?;

            let config_dir = desktop_config_dir(app)?;
            migrate_legacy_config_dir(app, &config_dir)?;
            fs::create_dir_all(&config_dir)?;

            let desktop_token = generate_desktop_token()?;
            let backend = start_backend(app, &config_dir, &desktop_token)?;
            app.manage(backend);
            wait_for_backend_health(&desktop_token)?;
            open_main_webview(app, &desktop_token)?;

            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("failed to build AI Analyst desktop app");

    app.run(|app_handle, event| {
        if matches!(event, RunEvent::ExitRequested { .. }) {
            stop_backend(app_handle);
        }
    });
}

fn start_backend(
    app: &tauri::App,
    config_dir: &Path,
    desktop_token: &str,
) -> Result<BackendProcess, Box<dyn Error>> {
    let mut command = app
        .shell()
        .sidecar(SIDECAR_NAME)
        .map_err(|err| startup_error(format!("failed to resolve backend sidecar: {err}")))?
        .args(sidecar_args(config_dir, desktop_token));

    if let Some(runtime_dir) = bundled_runtime_dir(app) {
        command = command.current_dir(runtime_dir);
    }

    let (mut rx, child) = command
        .spawn()
        .map_err(|err| startup_error(format!("failed to launch backend sidecar: {err}")))?;
    let pid = child.pid();

    tauri::async_runtime::spawn(async move {
        while let Some(event) = rx.recv().await {
            match event {
                CommandEvent::Stdout(bytes) => log_sidecar_line("stdout", &bytes),
                CommandEvent::Stderr(bytes) => log_sidecar_line("stderr", &bytes),
                CommandEvent::Error(err) => eprintln!("[ai-analyst-server:error] {err}"),
                CommandEvent::Terminated(payload) => eprintln!(
                    "[ai-analyst-server] pid {pid} terminated with code {:?}, signal {:?}",
                    payload.code, payload.signal
                ),
                _ => {}
            }
        }
    });

    Ok(BackendProcess(Mutex::new(Some(child))))
}

fn sidecar_args(config_dir: &Path, desktop_token: &str) -> Vec<OsString> {
    vec![
        "--host".into(),
        BACKEND_HOST.into(),
        "--port".into(),
        BACKEND_PORT.to_string().into(),
        "--config-dir".into(),
        config_dir.as_os_str().to_owned(),
        "--desktop-token".into(),
        desktop_token.into(),
    ]
}

fn bundled_runtime_dir(app: &tauri::App) -> Option<PathBuf> {
    ["resources/runtime", "runtime"]
        .into_iter()
        .filter_map(|path| app.path().resolve(path, BaseDirectory::Resource).ok())
        .find(|path| path.is_dir())
}

fn desktop_config_dir(app: &tauri::App) -> Result<PathBuf, Box<dyn Error>> {
    app.path()
        .app_local_data_dir()
        .or_else(|_| app.path().app_data_dir())
        .map_err(|err| startup_error(format!("failed to resolve app data directory: {err}")))
}

fn migrate_legacy_config_dir(app: &tauri::App, config_dir: &Path) -> Result<(), Box<dyn Error>> {
    if config_dir.exists() {
        if !config_dir.is_dir() || dir_has_entries(config_dir)? {
            return Ok(());
        }
    }
    let Some(legacy_dir) = legacy_config_dirs(app, config_dir)
        .into_iter()
        .find(|path| path.is_dir() && path != config_dir)
    else {
        return Ok(());
    };
    let Some(parent) = config_dir.parent() else {
        return Ok(());
    };
    if config_dir.exists() {
        fs::remove_dir(config_dir)?;
    }
    fs::create_dir_all(parent)?;
    match fs::rename(&legacy_dir, config_dir) {
        Ok(()) => Ok(()),
        Err(rename_err) => {
            copy_dir_recursive(&legacy_dir, config_dir).map_err(|copy_err| {
                startup_error(format!(
                    "failed to migrate app data from {} to {}: rename failed with {rename_err}; copy failed with {copy_err}",
                    legacy_dir.display(),
                    config_dir.display()
                ))
            })?;
            if let Err(remove_err) = fs::remove_dir_all(&legacy_dir) {
                eprintln!(
                    "[ai-analyst-desktop] migrated app data to {}; failed to remove legacy directory {}: {remove_err}",
                    config_dir.display(),
                    legacy_dir.display()
                );
            }
            Ok(())
        }
    }
}

fn legacy_config_dirs(app: &tauri::App, config_dir: &Path) -> Vec<PathBuf> {
    let mut dirs = Vec::new();
    if let Ok(roaming_dir) = app.path().app_data_dir() {
        push_unique_path(&mut dirs, roaming_dir.clone());
        if let Some(parent) = roaming_dir.parent() {
            push_unique_path(&mut dirs, parent.join(LEGACY_APP_IDENTIFIER));
        }
    }
    if let Some(parent) = config_dir.parent() {
        push_unique_path(&mut dirs, parent.join(LEGACY_APP_IDENTIFIER));
    }
    dirs
}

fn push_unique_path(paths: &mut Vec<PathBuf>, path: PathBuf) {
    if !paths.iter().any(|existing| existing == &path) {
        paths.push(path);
    }
}

fn dir_has_entries(path: &Path) -> std::io::Result<bool> {
    let mut entries = match fs::read_dir(path) {
        Ok(entries) => entries,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(err) => return Err(err),
    };
    Ok(entries.next().transpose()?.is_some())
}

fn copy_dir_recursive(src: &Path, dst: &Path) -> std::io::Result<()> {
    fs::create_dir_all(dst)?;
    for entry in fs::read_dir(src)? {
        let entry = entry?;
        let source_path = entry.path();
        let target_path = dst.join(entry.file_name());
        let file_type = entry.file_type()?;
        if file_type.is_dir() {
            copy_dir_recursive(&source_path, &target_path)?;
        } else {
            fs::copy(&source_path, &target_path)?;
        }
    }
    Ok(())
}

fn open_main_webview(app: &tauri::App, desktop_token: &str) -> Result<(), Box<dyn Error>> {
    let url = main_webview_url(desktop_token, &desktop_launch_nonce())?;

    WebviewWindowBuilder::new(app, "main", WebviewUrl::External(url))
        .title("AI Analyst")
        .inner_size(1280.0, 860.0)
        .min_inner_size(960.0, 640.0)
        .center()
        .build()?;

    Ok(())
}

fn desktop_launch_nonce() -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_nanos())
        .unwrap_or_default();
    format!("{}-{nanos}", std::process::id())
}

fn main_webview_url(desktop_token: &str, launch_nonce: &str) -> Result<tauri::Url, Box<dyn Error>> {
    let mut url = tauri::Url::parse(APP_URL)?;
    url.query_pairs_mut()
        .append_pair("desktop_build", env!("CARGO_PKG_VERSION"))
        .append_pair("launch", launch_nonce);
    url.set_fragment(Some(&format!("desktop_token={desktop_token}")));
    Ok(url)
}

fn stop_backend(app: &tauri::AppHandle) {
    if let Some(backend) = app.try_state::<BackendProcess>() {
        backend.stop();
    }
}

#[cfg(not(target_os = "macos"))]
fn terminate_stale_backend_listeners() {}

#[cfg(target_os = "macos")]
fn terminate_stale_backend_listeners() {
    for pid in backend_listener_pids() {
        if !is_stale_ai_analyst_backend(pid) {
            continue;
        }
        terminate_process(pid, "TERM");
        wait_for_backend_listener_exit(pid, Duration::from_secs(2));
        if backend_listener_pids().contains(&pid) {
            terminate_process(pid, "KILL");
            wait_for_backend_listener_exit(pid, Duration::from_secs(1));
        }
    }
}

#[cfg(target_os = "macos")]
fn backend_listener_pids() -> Vec<u32> {
    let Ok(output) = StdCommand::new("/usr/sbin/lsof")
        .args(["-nP", "-tiTCP:8787", "-sTCP:LISTEN"])
        .output()
    else {
        return Vec::new();
    };
    if !output.status.success() {
        return Vec::new();
    }
    String::from_utf8_lossy(&output.stdout)
        .lines()
        .filter_map(|line| line.trim().parse::<u32>().ok())
        .filter(|pid| *pid != std::process::id())
        .collect()
}

#[cfg(target_os = "macos")]
fn is_stale_ai_analyst_backend(pid: u32) -> bool {
    let command = process_field(pid, "command=");
    if !command.contains("ai-analyst-server") {
        return false;
    }
    let parent_pid = process_field(pid, "ppid=").trim().parse::<u32>().ok();
    !command.contains("--desktop-token") || parent_pid == Some(1)
}

#[cfg(target_os = "macos")]
fn process_field(pid: u32, field: &str) -> String {
    let Ok(output) = StdCommand::new("/bin/ps")
        .args(["-p", &pid.to_string(), "-o", field])
        .output()
    else {
        return String::new();
    };
    if !output.status.success() {
        return String::new();
    }
    String::from_utf8_lossy(&output.stdout).trim().to_string()
}

#[cfg(target_os = "macos")]
fn terminate_process(pid: u32, signal: &str) {
    let _ = StdCommand::new("/bin/kill")
        .args([format!("-{signal}"), pid.to_string()])
        .status();
}

#[cfg(target_os = "macos")]
fn wait_for_backend_listener_exit(pid: u32, timeout: Duration) {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if !backend_listener_pids().contains(&pid) {
            return;
        }
        thread::sleep(Duration::from_millis(100));
    }
}

fn ensure_desktop_port_available() -> Result<(), Box<dyn Error>> {
    for addr in ["127.0.0.1:8787", "[::1]:8787"] {
        match TcpListener::bind(addr) {
            Ok(listener) => drop(listener),
            Err(err) if err.kind() == std::io::ErrorKind::AddrNotAvailable => continue,
            Err(err) => {
                return Err(startup_error(format!(
                    "desktop backend port {BACKEND_PORT} is already in use on {addr}; \
                     stop the process using it before starting AI Analyst Desktop ({err})"
                )));
            }
        }
    }
    Ok(())
}

fn wait_for_backend_health(desktop_token: &str) -> Result<(), Box<dyn Error>> {
    let deadline = Instant::now() + STARTUP_TIMEOUT;
    let health_url = format!("http://localhost:{BACKEND_PORT}{HEALTH_PATH}");
    let mut last_error = String::from("backend has not accepted a connection yet");

    while Instant::now() < deadline {
        match request_health(desktop_token) {
            Ok(()) => return Ok(()),
            Err(err) => last_error = err,
        }
        thread::sleep(Duration::from_millis(250));
    }

    Err(startup_error(format!(
        "backend did not become healthy at {health_url} within {} seconds; \
         last check failed with: {last_error}",
        STARTUP_TIMEOUT.as_secs()
    )))
}

fn request_health(desktop_token: &str) -> Result<(), String> {
    let addresses = ("localhost", BACKEND_PORT)
        .to_socket_addrs()
        .map_err(|err| format!("failed to resolve localhost:{BACKEND_PORT}: {err}"))?;
    let request = format!(
        "GET {HEALTH_PATH} HTTP/1.1\r\nHost: localhost:{BACKEND_PORT}\r\nX-AI-Desktop-Token: {desktop_token}\r\nConnection: close\r\n\r\n"
    );
    let mut errors = Vec::new();

    for addr in addresses {
        let mut stream = match TcpStream::connect_timeout(&addr, HEALTH_CONNECT_TIMEOUT) {
            Ok(stream) => stream,
            Err(err) => {
                errors.push(format!("{addr}: {err}"));
                continue;
            }
        };

        let _ = stream.set_read_timeout(Some(HEALTH_READ_TIMEOUT));
        let _ = stream.set_write_timeout(Some(HEALTH_READ_TIMEOUT));

        if let Err(err) = stream.write_all(request.as_bytes()) {
            errors.push(format!("{addr}: write failed: {err}"));
            continue;
        }

        let mut response = String::new();
        if let Err(err) = stream.read_to_string(&mut response) {
            errors.push(format!("{addr}: read failed: {err}"));
            continue;
        }

        if (response.starts_with("HTTP/1.1 200") || response.starts_with("HTTP/1.0 200"))
            && response.contains("\"ok\":true")
        {
            return Ok(());
        }

        let status = response.lines().next().unwrap_or("<empty response>");
        errors.push(format!(
            "{addr}: health returned unexpected response {status}"
        ));
    }

    Err(errors.join("; "))
}

fn log_sidecar_line(stream: &str, bytes: &[u8]) {
    let line = String::from_utf8_lossy(bytes);
    eprintln!("[ai-analyst-server:{stream}] {}", line.trim_end());
}

fn generate_desktop_token() -> Result<String, Box<dyn Error>> {
    let mut bytes = [0u8; 32];
    getrandom::getrandom(&mut bytes)
        .map_err(|err| startup_error(format!("failed to generate desktop token: {err}")))?;
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        out.push(hex_nibble(byte >> 4));
        out.push(hex_nibble(byte & 0x0f));
    }
    Ok(out)
}

fn hex_nibble(value: u8) -> char {
    match value {
        0..=9 => (b'0' + value) as char,
        _ => (b'a' + (value - 10)) as char,
    }
}

fn startup_error(message: String) -> Box<dyn Error> {
    std::io::Error::new(std::io::ErrorKind::Other, message).into()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn main_webview_url_busts_cached_shell_without_exposing_the_desktop_token() {
        let url = main_webview_url("secret-token", "launch-123").expect("desktop URL");
        let query = url
            .query_pairs()
            .collect::<std::collections::HashMap<_, _>>();
        assert_eq!(
            query.get("desktop_build").map(|value| value.as_ref()),
            Some(env!("CARGO_PKG_VERSION"))
        );
        assert_eq!(
            query.get("launch").map(|value| value.as_ref()),
            Some("launch-123")
        );
        assert!(!url.query().unwrap_or_default().contains("secret-token"));
        assert_eq!(url.fragment(), Some("desktop_token=secret-token"));
    }
}
