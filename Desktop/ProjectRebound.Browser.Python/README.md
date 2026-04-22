# ProjectRebound Python Browser Prototype

This is a dependency-free Python 3.11 + tkinter prototype for the room browser.

## Run

```powershell
python Desktop\ProjectRebound.Browser.Python\project_rebound_browser.py
```

or use launch scripts:

```text
Desktop\ProjectRebound.Browser.Python\run_browser.bat
Desktop\ProjectRebound.Browser.Python\run_browser_debug.bat
```

- `run_browser.bat` defaults to quiet mode (no extra console windows when possible).
- `run_browser_debug.bat` keeps the legacy debug-friendly behavior (visible consoles and batch launch flow).
- In GUI settings, `Launch` accepts `quiet` or `debug` and is saved to config.

## Notes

- Start the backend first: `dotnet run --project Backend\ProjectRebound.MatchServer\ProjectRebound.MatchServer.csproj`
- Config is saved to `%APPDATA%\ProjectReboundBrowser\config-python.json`.
- Create Room and Quick Match require the selected UDP port to be reachable from the backend.
- Join launches the game with `-LogicServerURL=http://127.0.0.1:8000 -match=ip:port`; `-debuglog` is enabled in `debug` launch mode. `Logic URL` can be changed in the GUI.
- Client `-debuglog` writes Payload client logs under the game working directory's `clientlogs/` folder.
- Before launching the client or host wrapper, the GUI mirrors the working batch launcher: it starts `BoundaryMetaServer-main/index.js` with `nodejs/node.exe` when `Logic URL` points to local `127.0.0.1:8000`, then waits until the fake login server is reachable.
- In debug mode, client launch and UDP Proxy host launch are written to `%APPDATA%\ProjectReboundBrowser\launchers\launch-client.bat` / `launch-host.bat` and executed through `cmd.exe`, matching the known-good batch launcher style. The host batch keeps the console open with `pause`, starts fake login, UDP proxy, server wrapper, waits, then starts a normal game client with `-match=127.0.0.1:<game-port>` so the host joins the room it just created.
- In quiet mode, launch paths avoid `cmd.exe` batch windows and start child processes directly with hidden-window creation flags.
- Before launching the game, the GUI copies the built `dxgi.dll` and `Payload.dll` into the game exe directory when needed. These files are required for `-match` and `-debuglog` to do anything.
- Client game launch intentionally does not use `CREATE_NEW_CONSOLE`; proxy and wrapper still open their own consoles for logs.
- Create Room launches `ProjectReboundServerWrapper.exe` when it can find it under the configured game directory.
- This Python prototype is the current recommended GUI path; the WPF prototype remains in the repo but is not the active target.

## Experimental UDP Proxy

`project_rebound_udp_proxy.py` is an experimental local UDP punch proxy.

When `Use UDP Proxy` is checked in the GUI:

- Host side keeps the public room port for the proxy, for example `7777`.
- Host game/server wrapper is launched on `Port + 1`, for example `7778`.
- Client side starts a local proxy on `Client Proxy`, for example `17777`.
- Client game is launched with `-match=127.0.0.1:17777`.
- The backend only exchanges NAT binding and punch ticket metadata; it does not relay game packets.

The backend must expose UDP rendezvous port `5001/udp` when this mode is used.

This mode is a prototype. It can help with cone / port-restricted NAT cases, but it does not guarantee symmetric NAT or strict CGNAT traversal.
