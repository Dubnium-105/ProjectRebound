// ======================================================
//  GUI MODE – SLINT FRONTEND FOR SERVER WRAPPER
// ======================================================
#include "app-window.h"
#include "headers/wrapper.h"
#include <thread>
#include <slint.h>
#include <deque>
#include <mutex>
#include <string>
#include <sstream>
#include <atomic>
#include <windows.h>
#include "headers/json.hpp"
using json = nlohmann::json;

// ---------- UI log queue (thread‑safe) ----------
slint::ComponentWeakHandle<AppWindow> g_ui;

constexpr size_t MAX_LOG_LINES = 100;
std::deque<std::string> g_log_lines;
std::mutex g_log_mutex;
std::atomic<int> g_total_log_length{0};

// 从 wrapper 全局变量更新到 UI（通过 AppWindow 属性）
void SyncConfigToUI(AppWindow &ui) {
    std::lock_guard<std::mutex> lock(g_ServerMutex);
    ui.set_cfg_map(slint::SharedString(CurrentMap));
    ui.set_cfg_mode(slint::SharedString(CurrentMode));
    ui.set_cfg_difficulty(slint::SharedString(CurrentDifficulty));
    ui.set_cfg_name(slint::SharedString(ServerName));
    ui.set_cfg_region(slint::SharedString(ServerRegion));
    ui.set_cfg_port(slint::SharedString(std::to_string(g_ServerPort)));
    ui.set_cfg_external_port(slint::SharedString(std::to_string(g_ExternalPort)));
    ui.set_cfg_backend(slint::SharedString(OnlineBackend));
    ui.set_cfg_offline(OfflineMode);
    ui.set_cfg_dx11(UseDX11);
}

// 从 UI 读取配置并写回 wrapper 全局变量
void SyncWrapperFromUI(AppWindow &ui) {
    std::lock_guard<std::mutex> lock(g_ServerMutex);
    CurrentMap = ui.get_cfg_map().data();
    CurrentMode = ui.get_cfg_mode().data();
    CurrentDifficulty = ui.get_cfg_difficulty().data();
    ServerName = ui.get_cfg_name().data();
    ServerRegion = ui.get_cfg_region().data();
    try { g_ServerPort = std::stoi(ui.get_cfg_port().data()); } catch (...) {}
    try { g_ExternalPort = std::stoi(ui.get_cfg_external_port().data()); } catch (...) {}
    OnlineBackend = ui.get_cfg_backend().data();
    OfflineMode = ui.get_cfg_offline();
    UseDX11 = ui.get_cfg_dx11();
}

void SyncStatusToUI(AppWindow& app,
                    const std::string& map, const std::string& mode,
                    int players, const std::string& state,
                    int port, const std::string& name, const std::string& region)
{
    app.set_status_map(slint::SharedString(map));
    app.set_status_mode(slint::SharedString(mode));
    app.set_status_players(players);
    app.set_status_state(slint::SharedString(state));
    app.set_status_port(port);
    app.set_status_name(slint::SharedString(name));
    app.set_status_region(slint::SharedString(region));
}

void PushToUILog(const std::string& text)
{
    std::lock_guard<std::mutex> lock(g_log_mutex);
    g_log_lines.push_back(text);
    if (g_log_lines.size() > MAX_LOG_LINES)
        g_log_lines.pop_front();

    int total = 0;
    for (const auto& line : g_log_lines)
        total += line.size();
    g_total_log_length.store(total);
}

std::string get_recent_logs()
{
    std::lock_guard<std::mutex> lock(g_log_mutex);
    std::ostringstream oss;
    for (const auto& line : g_log_lines)
        oss << line;
    return oss.str();
}

int get_log_length()
{
    return g_total_log_length.load();
}

std::string CleanModeString(const std::string& raw) {
    if (raw.empty()) return "Unknown";
    // 取最后一个 '/' 之后的部分
    size_t pos = raw.rfind('/');
    std::string modeName = (pos != std::string::npos) ? raw.substr(pos + 1) : raw;
    // 转为大写
    for (auto& ch : modeName) ch = static_cast<char>(std::toupper(static_cast<unsigned char>(ch)));
    if (modeName.find("PVE") != std::string::npos) return "PVE";
    return "PVP";   // 默认视为 PvP
}

// callback that feeds wrapper logs into the UI queue
// Process callback server's actuall status pack
void WrapperLogCallback(const std::string& msg)
{
    PushToUILog(msg);

    const std::string marker = "[ONLINE] Sent /server/status: ";
    size_t pos = msg.find(marker);
    if (pos != std::string::npos) {
        std::string jsonStr = msg.substr(pos + marker.size());
        while (!jsonStr.empty() && (jsonStr.back() == '\n' || jsonStr.back() == '\r'))
            jsonStr.pop_back();

        try {
            auto j = nlohmann::json::parse(jsonStr);
            std::string map = j.value("map", "");
            std::string rawMode = j.value("mode", "");
            std::string mode = CleanModeString(rawMode);
            int players = j.value("playerCount", 0);
            std::string state = j.value("serverState", "Unknown");
            int port = j.value("port", 0);
            std::string name = j.value("name", "");
            std::string region = j.value("region", "");

slint::invoke_from_event_loop([map, mode, players, state, port, name, region]() {
    auto ui_opt = g_ui.lock();                     // optional<ComponentHandle<AppWindow>>
    if (ui_opt) {
        AppWindow& app = *(*ui_opt);               // 第一次解 optional，第二次解 ComponentHandle
        SyncStatusToUI(app, map, mode, players, state, port, name, region);
    }
});
        } catch (...) { }
    }
}
// ---------- GUI entry ----------
void InitGUI()
{
    // prevent dxgi proxy hijacking
    SetDllDirectoryW(L"");

    auto ui = AppWindow::create();
    g_ui = ui;

    //SyncConfig
    LoadConfigFile();
    SyncConfigToUI(*ui);

    // 绑定 load-config 回调
    ui->on_load_config([&]() {
        LoadConfigFile();
        SyncConfigToUI(*ui);
        LauncherLog("Configuration loaded from file.");
    });

    // 绑定 save-config 回调
    ui->on_save_config([&]() {
        SyncWrapperFromUI(*ui);
        SaveConfigFile();
        LauncherLog("Configuration saved.");
    });

    ui->on_get_new_log([&]() -> slint::SharedString {
        return slint::SharedString(get_recent_logs());
    });
    ui->on_get_log_length([&]() -> int {
        return get_log_length();
    });
    ui->on_send_command([&](slint::SharedString cmd) {
        ExecuteConsoleCommand(std::string(cmd));
    });

    // button that starts the server
    ui->on_start_server([&]() {
        static bool started = false;
        if (started) return;
        started = true;

        SetExternalLogCallback(WrapperLogCallback);

        std::thread([]() {
            InitWrapperCore();    // load config, init commands, log file
            // note: we deliberately do NOT start console input thread
            RunWrapperLoop();     // launch server, wait for shutdown
        }).detach();
    });
    ui->on_restart_server([&]() {
    // RestartServer 直接调用 wrapper 中的函数
    RestartServer();
});
    ui->run();

    // window closed → trigger graceful shutdown
    g_WrapperShuttingDown.store(true);
    std::this_thread::sleep_for(std::chrono::seconds(2));
}