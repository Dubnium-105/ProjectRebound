// Boundary MetaServer TCP Proxy
// Sits transparently between game client (port 6969) and metaserver (port 6968).
// Decodes and logs all length-prefixed protobuf traffic in both directions.
// Flags unknown RPCPaths and undecodeable messages for reverse engineering.

const net = require('net');
const protobuf = require('protobufjs');
const fs = require('fs');
const path = require('path');

// =====================================================================
// Configuration
// =====================================================================

const PROXY_PORT = 6969;
const TARGET_PORT = 6968;
const TARGET_HOST = '127.0.0.1';
const PROTO_BASE = path.join(__dirname, 'game', 'proto');
const LOG_DIR = path.join(__dirname, 'logs');
const BINARY_DIR = path.join(LOG_DIR, 'binary');

const MAX_HEX_DUMP = 512; // max bytes to hex-dump per message (longer gets truncated)

// =====================================================================
// Init
// =====================================================================

if (!fs.existsSync(LOG_DIR)) {
    fs.mkdirSync(LOG_DIR, { recursive: true });
}
if (!fs.existsSync(BINARY_DIR)) {
    fs.mkdirSync(BINARY_DIR, { recursive: true });
}

const sessionLogFile = path.join(
    LOG_DIR,
    `proxy-${new Date().toISOString().replace(/[:.]/g, '-').substring(0, 19)}.log`
);
let logStream = fs.createWriteStream(sessionLogFile, { flags: 'a' });

function writeLog(text) {
    console.log(text);
    logStream.write(text + '\n');
}

// =====================================================================
// Proto Registry — preload all .proto files, index by message name
// =====================================================================

const protoRegistry = {};   // messageName → { root, type }

function loadAllProtos() {
    for (const sub of ['Request', 'Response']) {
        const dir = path.join(PROTO_BASE, sub);
        if (!fs.existsSync(dir)) continue;
        for (const file of fs.readdirSync(dir)) {
            if (!file.endsWith('.proto')) continue;
            const filePath = path.join(dir, file);
            let root;
            try {
                root = protobuf.loadSync(filePath);
            } catch (_) {
                writeLog(`[PROXY] WARN: Failed to load ${filePath}`);
                continue;
            }
            // Types are inside the ProjectBoundary namespace: root → ProjectBoundary → MessageType
            try {
                const ns = root.lookup('ProjectBoundary');
                for (const [name, nested] of Object.entries(ns.nested || {})) {
                    if (nested.fields) {
                        protoRegistry[name] = { root, type: nested };
                    }
                }
            } catch (_) { /* skip */ }
        }
    }
    const names = Object.keys(protoRegistry).join(', ');
    writeLog(`[PROXY] Loaded ${Object.keys(protoRegistry).length} proto types: ${names}`);
}

function lookupType(messageName) {
    const entry = protoRegistry[messageName];
    return entry ? entry.type : null;
}

// =====================================================================
// RPCPath → Request/Response type mapping
// =====================================================================

const RPC_MAP = {
    // ---- assets ----
    '/assets.Assets/GetPlayerArchiveV2': {
        reqInner: 'GetPlayerArchiveV2Request',
        resWrapper: 'ResponseWrapper',
        resInner: 'GetPlayerArchiveV2Response',
    },
    '/assets.Assets/UpdateRoleArchiveV2': {
        reqInner: 'UpdateRoleArchiveV2Request',
        resWrapper: 'ResponseWrapper',
        resInner: 'UpdateRoleArchiveV2Response',
    },
    '/assets.Assets/UpdateWeaponArchiveV2': {
        reqInner: 'UpdateWeaponArchiveV2Request',
        resWrapper: 'ResponseWrapper',
        resInner: 'UpdateRoleArchiveV2Response',
    },
    '/assets.Assets/QueryAssets': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryAssetsResponse',
    },
    '/assets.Assets/QueryAssetsInMatch': {
        reqInner: 'QueryAssetsInMatchReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryAssetsInMatchResp',
    },

    // ---- notification ----
    '/notification.Notification/QueryNotification': {
        reqInner: 'QueryNotificationRequest',
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryNotificationResponse',
    },

    // ---- party ----
    '/party.party/Create': {
        resWrapper: 'ResponseWrapper',
        resInner: 'CreatePartyResponse',
    },
    '/party.party/Ready': {
        reqInner: 'PartyReadyRequest',
        resWrapper: 'ResponseWrapper',
        resInner: 'PartyReadyResponse',
    },
    '/party.party/Get': {
        // no response expected
    },
    '/party.party/SetPresence': {
        reqInner: 'SetPartyPresenceRequest',
        resWrapper: 'ResponseWrapper',
        resInner: 'SetPartyPresenceResponse',
    },
    '/party.party/QueryPresence': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryPartyPresenceResponse',
    },
    '/party.party/AcceptInvitation': {
        reqInner: 'AcceptInvitationReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'AcceptInvitationResp',
    },
    '/party.party/SendInvitation': {
        reqInner: 'SendInvitationReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'SendInvitationResp',
    },
    '/party.party/RejectInvitation': {
        reqInner: 'RejectInvitationReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'RejectInvitationResp',
    },
    '/party.party/Join': {
        reqInner: 'JoinReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'JoinResp',
    },
    '/party.party/Leave': {
        reqInner: 'LeaveReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'LeaveResp',
    },
    '/party.party/Kick': {
        reqInner: 'KickReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'KickResp',
    },
    '/party.party/Promote': {
        reqInner: 'PromoteReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'PromoteResp',
    },

    // ---- chat (wire format uses lowercase "chat.chat") ----
    '/chat.chat/TextFilter': {
        reqInner: 'TextFilterReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'TextFilterRes',
    },
    '/chat.chat/Create': {
        reqInner: 'CreateReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'CreateResp',
    },
    '/chat.chat/Join': {
        reqInner: 'JoinReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'JoinResp',
    },
    '/chat.chat/Leave': {
        reqInner: 'LeaveReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'LeaveResp',
    },
    '/chat.chat/AddMember': {
        reqInner: 'AddMemberReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'AddMemberResp',
    },
    '/chat.chat/RemoveMember': {
        reqInner: 'RemoveMemberReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'RemoveMemberResp',
    },
    '/chat.chat/SendMessage': {
        reqInner: 'SendChatMessageReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'SendChatMessageResp',
    },

    // ---- matchmaking ----
    '/matchmaking.Matchmaking/QueryUnityMatchmakingRegion': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryMatchmakingRegionResponse',
    },
    '/matchmaking.Matchmaking/StartUnityMatchmaking': {
        reqInner: 'StartMatchmakingRequest',
        resWrapper: 'ResponseWrapper',
        resInner: 'StartMatchmakingResponse',
    },
    '/matchmaking.Matchmaking/QueryPlayList': {
        resWrapper: 'JSONResponseWrapper',
        resInner: 'QueryPlaylistResponse',
    },
    '/matchmaking.Matchmaking/QueryUnityMatchmaking': {
        reqInner: 'QueryUnityMatchmakingReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryUnityMatchmakingRes',
    },
    '/matchmaking.Matchmaking/StopUnityMatchmaking': {
        reqInner: 'StopUnityMatchmakingReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'StopUnityMatchmakingRes',
    },

    // ---- playerdata ----
    '/playerdata.PlayerDataClient/GetDataStatisticsInfo': {
        resWrapper: 'ResponseWrapper',
        resInner: 'GetDataStatisticsInfoResponse',
    },
    '/playerdata.PlayerDataClient/AddDataStatisticsInfo': {
        reqInner: 'AddDataStatisticsInfoReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'AddDataStatisticsInfoResp',
    },

    // ---- profile ----
    '/profile.Profile/QueryCurrency': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryCurrencyResponse',
    },

    // ---- account ----
    '/account.Account/GetAceId': {
        reqInner: 'GetAceIdReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'GetAceIdResp',
    },
    '/account.Account/GetUserId': {
        reqInner: 'GetUserIdReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'GetUserIdResp',
    },

    // ---- event ----
    '/event.Event/QueryOperatingEvent': {
        reqInner: 'QueryOperatingEventReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryOperatingEventResp',
    },

    // ---- eventtracking (wire format: "eventtracking.EventTracking") ----
    '/eventtracking.EventTracking/Record': {
        reqInner: 'RecordReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'RecordResp',
    },

    // ---- feedback ----
    '/feedback.Feedback/CreateReport': {
        reqInner: 'CreateReportReq',
        resWrapper: 'ResponseWrapper',
    },
    '/feedback.Feedback/Create': {
        reqInner: 'CreateReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'CreateResp',
    },

    // ---- gameplay ----
    '/gameplay.Gameplay/PlayerJoinMatch': {
        reqInner: 'PlayerJoinMatchReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'PlayerJoinMatchResp',
    },

    // ---- mission ----
    '/mission.Mission/QueryLoginRecord': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryLoginRecordResp',
    },
    '/mission.Mission/QueryProgress': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryProgressResp',
    },
    '/mission.Mission/QueryUserEvents': {
        reqInner: 'QueryUserEventsReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryUserEventsResp',
    },
    '/mission.Mission/QueryActivitiesInfo': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QuestActivitiesInfoResp',
    },
    '/mission.Mission/QueryLoginRecord': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryLoginRecordResp',
    },
    '/mission.Mission/RefreshDailyActivity': {
        reqInner: 'RefreshDailyActivityReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'RefreshDailyActivityResp',
    },
    '/mission.Mission/SignIn': {
        resWrapper: 'ResponseWrapper',
        resInner: 'SignInResp',
    },

    // ---- challenge ----
    '/challenge.Challenge/GetAllChallengeGroupsProgress': {
        resWrapper: 'ResponseWrapper',
        resInner: 'GetChallengeGroupsProgressResp',
    },
    '/challenge.Challenge/UpdateChallengeGroups': {
        reqInner: 'UpdateChallengeGroupsReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'UpdateChallengeGroupsResp',
    },

    // ---- store ----
    '/store.Store/QueryArmory': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryArmoryResponse',
    },
    '/store.Store/PurchaseInArmory': {
        reqInner: 'PurchaseInArmoryRequest',
        resWrapper: 'ResponseWrapper',
        resInner: 'PurchaseInArmoryResponse',
    },
    '/store.Store/QueryPersonalBlackMarket': {
        resWrapper: 'ResponseWrapper',
        resInner: 'QueryPersonalBlackMarketResponse',
    },
    '/store.Store/PurchaseInBlackMarket': {
        reqInner: 'PurchaseInBlackMarketRequest',
        resWrapper: 'ResponseWrapper',
        resInner: 'PurchaseInBlackMarketResponse',
    },

    // ---- redeem ----
    '/redeem.Redeem/CheckVCFromPSN': {
        resWrapper: 'ResponseWrapper',
        resInner: 'CheckVCFromPSNResponse',
    },
    '/redeem.Redeem/Use': {
        reqInner: 'UseReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'UseResp',
    },

    // ---- demo (dev test) ----
    '/demo.Demo/TestAllType': {
        reqInner: 'TestAllTypeReq',
        resWrapper: 'ResponseWrapper',
        resInner: 'TestAllTypeResp',
    },
    '/demo.Demo/Echo': {
        reqInner: 'echo',
        resWrapper: 'ResponseWrapper',
        resInner: 'echo',
    },
};

const NO_RESPONSE_RPCS = new Set([
    '/party.party/Get',
    '/chat.chat/TextFilter',
]);

const KNOWN_RPCS = new Set(Object.keys(RPC_MAP));

// =====================================================================
// Utilities
// =====================================================================

function hexDump(buf, maxLen = MAX_HEX_DUMP) {
    const display = buf.length > maxLen ? buf.subarray(0, maxLen) : buf;
    const lines = [];
    for (let i = 0; i < display.length; i += 16) {
        const chunk = display.subarray(i, Math.min(i + 16, display.length));
        const hex = Array.from(chunk, b => b.toString(16).padStart(2, '0')).join(' ');
        const ascii = Array.from(chunk, b => (b >= 0x20 && b <= 0x7e) ? String.fromCharCode(b) : '.')
            .join('');
        lines.push(`  ${i.toString(16).padStart(4, '0')}: ${hex.padEnd(47)} ${ascii}`);
    }
    if (buf.length > maxLen) {
        lines.push(`  ... (truncated, ${buf.length} bytes total)`);
    }
    return lines.join('\n');
}

const PROTO_DECODE_OPTS = {
    Enums: String,
    longs: String,
    defaults: true,
    arrays: true,
    objects: true,
    oneofs: true,
};

function tryDecodeMessage(buf, messageName) {
    if (!buf || buf.length === 0) return null;
    const type = lookupType(messageName);
    if (!type) return { _error: `type "${messageName}" not found in registry` };
    try {
        const decoded = type.decode(buf);
        return type.toObject(decoded, PROTO_DECODE_OPTS);
    } catch (e) {
        return { _error: e.message };
    }
}

function tryDecodeWrapper(buf, wrapperName) {
    if (!buf || buf.length === 0) return null;
    const type = lookupType(wrapperName);
    if (!type) return null;
    try {
        const decoded = type.decode(buf);
        return type.toObject(decoded, PROTO_DECODE_OPTS);
    } catch (_) {
        return null;
    }
}

function isHandshake(rpcPath) {
    return rpcPath && /^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(rpcPath);
}

function isKeepalive(buf) {
    return buf.length === 6 && buf.toString('hex') === '000000022f2f';
}

let msgSeq = 0;

function logTraffic(direction, wrapperObj, wrapperType, framedBuf, innerBuf, innerDecoded) {
    const seq = ++msgSeq;
    const arrow = direction === 'req' ? '→ REQ' : '← RES';
    const rpcPath = wrapperObj.RPCPath || '?';
    const msgId = wrapperObj.MessageId ?? '?';
    const isUnknown = direction === 'req' && !KNOWN_RPCS.has(rpcPath) && !isHandshake(rpcPath);
    const flag = isUnknown ? ' *** UNKNOWN RPC ***' : '';
    const sep = '='.repeat(80);

    // Save binary blobs for analysis tools
    const safeRpc = rpcPath.replace(/[^a-zA-Z0-9._-]/g, '_').substring(0, 60);
    const prefix = `${String(seq).padStart(4, '0')}_${direction}_${msgId}_${safeRpc}`;
    fs.writeFileSync(path.join(BINARY_DIR, `${prefix}_frame.bin`), framedBuf);
    if (innerBuf && innerBuf.length > 0) {
        fs.writeFileSync(path.join(BINARY_DIR, `${prefix}_inner.bin`), innerBuf);
    }
    fs.writeFileSync(path.join(BINARY_DIR, `${prefix}_meta.json`), JSON.stringify({
        seq, direction, msgId, rpcPath, wrapperType,
        frameLen: framedBuf.length,
        innerLen: innerBuf ? innerBuf.length : 0,
        decoded: innerDecoded || null,
        isUnknown,
        time: new Date().toISOString(),
    }, null, 2));

    let innerHex = '';
    if (innerBuf && innerBuf.length > 0) {
        innerHex = `\n-- Inner message (${innerBuf.length} bytes) --\n${hexDump(innerBuf)}\n`;
    }

    let decodedStr = '';
    if (innerDecoded) {
        if (innerDecoded._error) {
            decodedStr = `-- Decode error: ${innerDecoded._error} --\n`;
        } else {
            decodedStr = `-- Decoded --\n${JSON.stringify(innerDecoded, null, 2)}\n`;
        }
    }

    writeLog(
        `${sep}\n` +
        `[${arrow}] #${msgId} | ${rpcPath} | ${framedBuf.length} bytes | ${wrapperType}${flag}\n` +
        `[${arrow}] Time: ${new Date().toISOString()}\n` +
        `-- Raw frame (4-byte len + payload) --\n${hexDump(framedBuf)}\n` +
        innerHex +
        decodedStr +
        `${sep}\n`
    );
}

// =====================================================================
// Proxy Server
// =====================================================================

const proxyServer = net.createServer((clientSock) => {
    const clientAddr = `${clientSock.remoteAddress}:${clientSock.remotePort}`;
    writeLog(`\n[PROXY] Client connected: ${clientAddr}`);

    const targetSock = new net.Socket();
    let clientBuf = Buffer.alloc(0);
    let serverBuf = Buffer.alloc(0);
    const msgIdToRPC = new Map(); // track MessageId → RPCPath for response matching

    targetSock.connect(TARGET_PORT, TARGET_HOST, () => {
        // pipe is ready
    });

    // ---- helpers for the frame-parsing loop ----

    function processClientFrame(framedMsg) {
        targetSock.write(framedMsg); // forward

        const payload = framedMsg.subarray(4);
        const wrapper = tryDecodeWrapper(payload, 'RequestWrapper');
        if (!wrapper) {
            writeLog(`[→ REQ] Cannot decode as RequestWrapper\n${hexDump(framedMsg)}\n`);
            return;
        }

        const rpcPath = wrapper.RPCPath || '?';
        const msgId = wrapper.MessageId;

        if (msgId != null && rpcPath !== '?') {
            msgIdToRPC.set(msgId, rpcPath);
        }

        if (isHandshake(rpcPath)) {
            writeLog(`[→ HANDSHAKE] ${framedMsg.length} bytes, echo expected\n${hexDump(framedMsg)}\n`);
            return;
        }

        const rpcInfo = RPC_MAP[rpcPath];
        const innerBytes = wrapper.Message; // bytes → Buffer or undefined

        let innerDecoded = null;
        if (innerBytes && innerBytes.length > 0 && rpcInfo && rpcInfo.reqInner) {
            innerDecoded = tryDecodeMessage(innerBytes, rpcInfo.reqInner);
        }

        logTraffic('req', wrapper, 'RequestWrapper', framedMsg, innerBytes, innerDecoded);
    }

    function processServerFrame(framedMsg) {
        clientSock.write(framedMsg); // forward

        const payload = framedMsg.subarray(4);

        // Determine which wrapper type to use
        // Strategy: decode as RequestWrapper first to get MessageId,
        // then look up RPCPath to decide ResponseWrapper vs JSONResponseWrapper
        let wrapper = null;
        let wrapperType = null;
        let msgId = null;
        let rpcPath = null;

        // First try to extract MessageId from any wrapper-like structure
        // (all three wrappers share field 1 = int32 MessageId)
        for (const wname of ['ResponseWrapper', 'JSONResponseWrapper', 'RequestWrapper']) {
            const w = tryDecodeWrapper(payload, wname);
            if (w && w.MessageId != null) {
                msgId = w.MessageId;
                rpcPath = w.RPCPath || msgIdToRPC.get(msgId) || '?';
                break;
            }
        }

        if (msgId == null) {
            writeLog(`[← RES] Cannot decode any wrapper\n${hexDump(framedMsg)}\n`);
            return;
        }

        if (isHandshake(rpcPath)) {
            writeLog(`[← HANDSHAKE ECHO] ${framedMsg.length} bytes\n${hexDump(framedMsg)}\n`);
            return;
        }

        // Now decode with the correct wrapper
        const rpcInfo = RPC_MAP[rpcPath];
        const expectedWrapper = (rpcInfo && rpcInfo.resWrapper) || 'ResponseWrapper';

        wrapper = tryDecodeWrapper(payload, expectedWrapper);
        wrapperType = expectedWrapper || '?';

        if (!wrapper) {
            // fallback: try the other wrapper
            const fallback = expectedWrapper === 'ResponseWrapper' ? 'JSONResponseWrapper' : 'ResponseWrapper';
            wrapper = tryDecodeWrapper(payload, fallback);
            wrapperType = wrapper ? fallback : '?';
        }

        if (!wrapper) {
            writeLog(`[← RES] #${msgId} | ${rpcPath} | decode failed\n${hexDump(framedMsg)}\n`);
            return;
        }

        let innerBytes = null;
        let innerDecoded = null;

        if (expectedWrapper === 'JSONResponseWrapper' && wrapper.JSONMessage) {
            try {
                innerDecoded = JSON.parse(wrapper.JSONMessage);
            } catch (_) {
                innerDecoded = { _jsonRaw: wrapper.JSONMessage };
            }
        } else if (wrapper.Message && wrapper.Message.length > 0) {
            innerBytes = wrapper.Message;
            if (rpcInfo && rpcInfo.resInner) {
                innerDecoded = tryDecodeMessage(innerBytes, rpcInfo.resInner);
            }
        }

        logTraffic('res', wrapper, wrapperType, framedMsg, innerBytes, innerDecoded);
    }

    // ---- Client → MetaServer ----

    clientSock.on('data', (raw) => {
        if (isKeepalive(raw)) {
            targetSock.write(raw);
            return;
        }

        clientBuf = Buffer.concat([clientBuf, raw]);

        while (clientBuf.length >= 4) {
            const frameLen = clientBuf.readUint32BE(0);
            if (clientBuf.length < 4 + frameLen) break; // wait for complete frame

            const frame = clientBuf.subarray(0, 4 + frameLen);
            clientBuf = clientBuf.subarray(4 + frameLen);
            processClientFrame(frame);
        }
    });

    // ---- MetaServer → Client ----

    targetSock.on('data', (raw) => {
        if (isKeepalive(raw)) {
            clientSock.write(raw);
            return;
        }

        serverBuf = Buffer.concat([serverBuf, raw]);

        while (serverBuf.length >= 4) {
            const frameLen = serverBuf.readUint32BE(0);
            if (serverBuf.length < 4 + frameLen) break;

            const frame = serverBuf.subarray(0, 4 + frameLen);
            serverBuf = serverBuf.subarray(4 + frameLen);
            processServerFrame(frame);
        }
    });

    // ---- Cleanup ----

    function teardown() {
        if (!targetSock.destroyed) targetSock.destroy();
        if (!clientSock.destroyed) clientSock.destroy();
    }

    clientSock.on('close', () => {
        writeLog(`[PROXY] Client disconnected: ${clientAddr}\n`);
        teardown();
    });
    targetSock.on('close', () => {
        writeLog(`[PROXY] MetaServer connection closed for ${clientAddr}\n`);
        teardown();
    });
    clientSock.on('error', (e) => {
        writeLog(`[PROXY] Client error: ${e.message}`);
        teardown();
    });
    targetSock.on('error', (e) => {
        writeLog(`[PROXY] Target error: ${e.message}`);
        teardown();
    });
});

// =====================================================================
// Startup
// =====================================================================

loadAllProtos();

proxyServer.listen(PROXY_PORT, () => {
    writeLog(`[PROXY] Listening on :${PROXY_PORT} → ${TARGET_HOST}:${TARGET_PORT}`);
    writeLog(`[PROXY] Session log: ${sessionLogFile}`);
    writeLog(`[PROXY] Ready. Waiting for game client connections...\n`);
});

proxyServer.on('error', (err) => {
    console.error(`[PROXY] Fatal: ${err.message}`);
    process.exit(1);
});
