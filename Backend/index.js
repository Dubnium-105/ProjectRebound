const http = require('http');

const PORT = 3000;

// In-memory database of active servers
const servers = {};

// Hardcoded external port overrides (Docker mappings)
const portOverrides = {
    "Server_2": 35765
};

// Helper: parse JSON safely
function safeJsonParse(str) {
    try {
        return JSON.parse(str);
    } catch {
        return null;
    }
}

const server = http.createServer((req, res) => {

    // ============================
    //  POST /server/status
    // ============================
    if (req.method === 'POST' && req.url === '/server/status') {
        let body = '';

        req.on('data', chunk => {
            body += chunk.toString();
        });

        req.on('end', () => {
            const ip = req.socket.remoteAddress;
            const data = safeJsonParse(body);

            console.log("====================================");
            console.log("[DLL] Received POST /server/status");
            console.log("[DLL] From IP:", ip);
            console.log("[DLL] Raw Body:", body);

            if (!data) {
                console.log("[ERROR] Invalid JSON received");
                res.writeHead(400, { 'Content-Type': 'text/plain' });
                return res.end("Invalid JSON");
            }

            const {
                name,
                region,
                mode,
                map,
                port,
                playerCount,
                serverState,
                serverId
            } = data;

            // Clean mode
            let cleanMode = "Unknown";

            if (typeof mode === "string") {
                const parts = mode.split('/');
                const modeName = parts[parts.length - 1] || mode;
                const m = modeName.toUpperCase();

                if (m.includes("PVE")) cleanMode = "PVE";
                else if (m.includes("PVP")) cleanMode = "PVP";
                else cleanMode = "PVP"; // default assumption
            }

            // Apply port override if exists
            const finalPort = portOverrides[name] || port;

            // Update server entry
            servers[name] = {
                name,
                region,
                mode: cleanMode,
                map,
                port: finalPort,
                playerCount,
                serverState,
                serverId: serverId || "",
                ip,
                lastHeartbeat: Date.now()
            };

            console.log("[DB] Updated server entry:");
            console.log(servers[name]);
            console.log("====================================");

            res.writeHead(200, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify({ ok: true }));
        });

        return;
    }

    // ============================
    //  GET /servers
    // ============================
    if (req.method === 'GET' && req.url === '/servers') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        return res.end(JSON.stringify(Object.values(servers), null, 2));
    }
if (req.method === 'GET' && req.url === '/') {
    res.writeHead(200, { 'Content-Type': 'text/html' });
    return res.end(`
        <html>
        <head>
            <title>Server Status</title>
            <meta name="viewport" content="width=device-width, initial-scale=1.0">
            <style>
                body {
                    background: #1e1e1e;
                    color: #e0e0e0;
                    font-family: Arial, sans-serif;
                    padding: 20px;
                    margin: 0;
                }
                h1 {
                    text-align: center;
                    margin-bottom: 20px;
                    font-size: 1.8em;
                }
                table {
                    width: 100%;
                    border-collapse: collapse;
                    margin-top: 10px;
                    font-size: 1.1em;
                }
                th, td {
                    padding: 12px 10px;
                    border-bottom: 1px solid #333;
                    text-align: left;
                }
                th {
                    background: #333;
                    position: sticky;
                    top: 0;
                }
                tr:hover {
                    background: #2a2a2a;
                }
                @media (max-width: 600px) {
                    table, thead, tbody, th, td, tr {
                        display: block;
                    }
                    tr {
                        margin-bottom: 15px;
                        background: #252525;
                        padding: 10px;
                        border-radius: 8px;
                    }
                    th {
                        display: none;
                    }
                    td {
                        border: none;
                        padding: 6px 0;
                    }
                    td::before {
                        content: attr(data-label);
                        font-weight: bold;
                        display: block;
                        margin-bottom: 2px;
                        color: #aaa;
                    }
                }
            </style>
        </head>
        <body>
            <h1>Active Servers</h1>
            <table>
                <thead>
                    <tr>
                        <th>Name</th>
                        <th>Region</th>
                        <th>Mode</th>
                        <th>Map</th>
                        <th>Players</th>
                        <th>Port</th>
                    </tr>
                </thead>
                <tbody id="servers">
                    <tr><td colspan="6">Loading...</td></tr>
                </tbody>
            </table>

            <script>
                function load() {
                    fetch('/servers')
                        .then(r => r.json())
                        .then(list => {
                            let html = '';
                            list.forEach(s => {
                                html += '<tr>' +
                                    '<td data-label="Name">' + s.name + '</td>' +
                                    '<td data-label="Region">' + s.region + '</td>' +
                                    '<td data-label="Mode">' + s.mode + '</td>' +
                                    '<td data-label="Map">' + s.map + '</td>' +
                                    '<td data-label="Players">' + s.playerCount + '/10</td>' +
                                    '<td data-label="Port">' + s.port + '</td>' +
                                '</tr>';
                            });
                            document.getElementById('servers').innerHTML = html;
                        })
                        .catch(() => {
                            document.getElementById('servers').innerHTML =
                                '<tr><td colspan="6">Failed to load servers</td></tr>';
                        });
                }

                load();
                setInterval(load, 5000);
            </script>
        </body>
        </html>
    `);
}


    // Unknown route
    res.writeHead(404);
    res.end();
});

// ============================
//  Cleanup job: remove dead servers
// ============================
setInterval(() => {
    const now = Date.now();
    const timeout = 15000; // 15 seconds

    for (const name in servers) {
        if (now - servers[name].lastHeartbeat > timeout) {
            console.log("[CLEANUP] Removing dead server:", name);
            delete servers[name];
        }
    }
}, 5000);

server.listen(PORT, "0.0.0.0", () => {
    console.log(`Backend listening on port ${PORT}`);
});
