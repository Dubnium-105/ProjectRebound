#!/bin/bash
# Cold Launch — bash version, logs to file
# Usage: bash tools/cold_launch.sh

METADIR="$(dirname "$(dirname "$0")")"
LOG="$METADIR/logs/cold_scan_$(date +%Y%m%d_%H%M%S).log"
SCRIPT="$METADIR/tools/cold_start_scan.js"
GAMEDIR="D:/steam/steamapps/common/Boundary/ProjectBoundary/Binaries/Win64"
GAMEEXE="$GAMEDIR/ProjectBoundarySteam-Win64-Shipping.exe"

echo "=== Cold Launch ===" | tee "$LOG"
echo "Log: $LOG" | tee -a "$LOG"

# Kill old processes
taskkill //f //im node.exe 2>/dev/null
taskkill //f //im ProjectBoundarySteam-Win64-Shipping.exe 2>/dev/null
sleep 2

# Start metaserver + proxy
cd "$METADIR"
echo "[1] Starting MetaServer..." | tee -a "$LOG"
node index.js > logs/metaserver.log 2>&1 &
sleep 1
echo "[2] Starting Proxy..." | tee -a "$LOG"
node proxy.js > logs/proxy.log 2>&1 &
sleep 2

# Start game
echo "[3] Starting game..." | tee -a "$LOG"
start "" "$GAMEEXE" -LogicServerURL=http://127.0.0.1:8000 &
sleep 3

# Wait for game process
for i in $(seq 1 60); do
    PID=$(frida-ps 2>/dev/null | grep ProjectBoundary | awk '{print $1}' | head -1)
    if [ -n "$PID" ]; then
        echo "  Game PID: $PID" | tee -a "$LOG"
        break
    fi
    sleep 2
done

if [ -z "$PID" ]; then
    echo "ERROR: Game not found" | tee -a "$LOG"
    exit 1
fi

# Wait for modules to load
echo "  Waiting for modules..." | tee -a "$LOG"
sleep 15

# Attach Frida with output to log
echo "[4] Attaching Frida..." | tee -a "$LOG"
echo "  Log file: $LOG" | tee -a "$LOG"
frida -p "$PID" -l "$SCRIPT" 2>&1 | tee -a "$LOG"
