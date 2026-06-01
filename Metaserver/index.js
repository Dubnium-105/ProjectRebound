const express = require('express');
const bodyParser = require('body-parser');
const app = express();

// ---- Definition index and loadout storage ----
const { getDefinitionIndex } = require('./game/definitionIndex');
const { getLoadoutStore } = require('./game/loadoutStore');

app.use(bodyParser.json({ limit: '2mb' }));
app.use(bodyParser.urlencoded({ extended: true }));

app.use((req, res, next) => {
    // Avoid logging full /api/ bodies because loadout JSON can be large.
    if (req.originalUrl.startsWith('/api/')) {
        console.log(`\n=== API REQUEST ===`);
        console.log(`Time: ${new Date().toISOString()}`);
        console.log(`Method: ${req.method}`);
        console.log(`URL: ${req.originalUrl}`);
        console.log('====================\n');
    } else {
        console.log('\n=== RECEIVED REQUEST ===');
        console.log(`Time: ${new Date().toISOString()}`);
        console.log(`Method: ${req.method}`);
        console.log(`URL: ${req.originalUrl}`);
        console.log(`Headers:`, JSON.stringify(req.headers, null, 2));
        console.log(`Body:`, JSON.stringify(req.body, null, 2));
        console.log('========================\n');
    }
    next();
});

const MatchmakingHost = "204.12.195.98";
const MatchmakingPort = 9000;

// MatchServer management API base URL (HTTP API on port 9001)
const MATCHSERVER_API = `http://${MatchmakingHost}:9001`;

const matchmakingUDPServerDiscoveryPayload = {"servers":[{"location_id":6,"region_id":"336d1f3e-3ecb-11eb-a7dc-3b7705f20f56","ipv4":MatchmakingHost,"ipv6":"","port":MatchmakingPort}]}

app.get("/", (req, res) => {
  res.status(200).json(matchmakingUDPServerDiscoveryPayload);
});

app.post("/recordClientStatus", (req, res) => {
    res.status(200).json({}); 
});

const TEMP_USER_ID = "76561198211631084";
const LEGACY_GATE_TOKEN = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiYWRtaW4iOnRydWUsImlhdCI6MTUxNjIzOTAyMn0.KMUFsIDTnFmyG3nMiGM6H9FNFUROf3wh7SmqJp-QV30";
const gateSessions = new Map([[LEGACY_GATE_TOKEN, TEMP_USER_ID]]);

function normalizePlayerId(playerId) {
    if (typeof playerId !== "string") return TEMP_USER_ID;
    const trimmed = playerId.trim();
    return trimmed || TEMP_USER_ID;
}

function issueGateToken(playerId) {
    const normalizedPlayerId = normalizePlayerId(playerId);
    const gateToken = `prb.${Buffer.from(normalizedPlayerId, "utf8").toString("base64url")}.${crypto.randomUUID()}`;
    gateSessions.set(gateToken, normalizedPlayerId);
    return { gateToken, playerId: normalizedPlayerId };
}

function handleConnectServer(req, res) {
    const loginToken = req.body.loginToken;
    const platform = req.body.platform;
    const version = req.body.version;
    const { gateToken, playerId } = issueGateToken(req.body.playerId);

    console.log("Connection Request:", {
        platform,
        playerId,
        version,
        loginTokenPresent: Boolean(loginToken),
    });

    res.status(200).json({
        "error": 0,
        "userId": playerId,
        "aceId": "test",
        "gateToken": gateToken,
        "endpoint": "127.0.0.1:6969",
    });
}

app.post("//connectServer", handleConnectServer);
app.post("/connectServer", handleConnectServer);

// =====================================================================
//  REST API for loadouts and definitions, used by Payload / Browser.
// =====================================================================

// ---- Definition queries ----

// GET /api/definitions/roles - all role IDs
app.get("/api/definitions/roles", (req, res) => {
    const index = getDefinitionIndex();
    res.status(200).json({ roles: index.getAllRoleIds() });
});

// GET /api/definitions/roles/:roleId - role definition
app.get("/api/definitions/roles/:roleId", (req, res) => {
    const index = getDefinitionIndex();
    const role = index.getRole(req.params.roleId);
    if (!role) {
        return res.status(404).json({ error: "Role not found", roleId: req.params.roleId });
    }
    res.status(200).json({
        roleId: req.params.roleId,
        weaponScope: Array.from(role.weaponScope),
        podScope: Array.from(role.podScope),
        meleeWeaponScope: Array.from(role.meleeWeaponScope),
        mobilityScope: Array.from(role.mobilityScope),
        radarId: role.radarId,
        vehicleId: role.vehicleId,
        spaceSuitSkinScope: Array.from(role.spaceSuitSkinScope),
        armBadgeScope: Array.from(role.armBadgeScope),
        headAccessoryScope: Array.from(role.headAccessoryScope),
    });
});

// GET /api/definitions/weapons - all weapon IDs
app.get("/api/definitions/weapons", (req, res) => {
    const index = getDefinitionIndex();
    res.status(200).json({ weapons: index.getAllWeaponIds() });
});

// GET /api/definitions/weapons/:weaponId - weapon definition
app.get("/api/definitions/weapons/:weaponId", (req, res) => {
    const index = getDefinitionIndex();
    const weapon = index.getWeapon(req.params.weaponId);
    if (!weapon) {
        return res.status(404).json({ error: "Weapon not found", weaponId: req.params.weaponId });
    }
    // Convert Set values to arrays for JSON serialization.
    const slotScopes = {};
    for (const [slotName, partSet] of Object.entries(weapon.slotScopes)) {
        slotScopes[slotName] = Array.from(partSet);
    }
    res.status(200).json({
        weaponId: req.params.weaponId,
        slotScopes,
        receiverMain: weapon.receiverMain,
    });
});

// GET /api/definitions/resolve-weapon/:roleId/:baseWeaponId - weapon redirect
app.get("/api/definitions/resolve-weapon/:roleId/:baseWeaponId", (req, res) => {
    const index = getDefinitionIndex();
    const result = index.resolveRoleWeaponId(req.params.roleId, req.params.baseWeaponId);
    res.status(200).json({
        roleId: req.params.roleId,
        baseWeaponId: req.params.baseWeaponId,
        roleWeaponId: result,
        found: result !== null,
    });
});

// GET /api/definitions/items/:itemId/type - item type
app.get("/api/definitions/items/:itemId/type", (req, res) => {
    const index = getDefinitionIndex();
    const itemType = index.getItemType(req.params.itemId);
    res.status(200).json({
        itemId: req.params.itemId,
        type: itemType || "Unknown",
        found: itemType !== null,
    });
});

// ---- Loadout queries / updates ----

// GET /api/loadout/:playerId - full player loadout
app.get("/api/loadout/:playerId", (req, res) => {
    const store = getLoadoutStore();
    const data = store.getFullLoadout(req.params.playerId);
    if (!data) {
        return res.status(404).json({ error: "Player loadout not found", playerId: req.params.playerId });
    }
    res.status(200).json(data);
});

// PUT /api/loadout/:playerId - update a player's full loadout.
app.put("/api/loadout/:playerId", (req, res) => {
    const store = getLoadoutStore();
    const index = getDefinitionIndex();

    if (!req.body || !req.body.roles || typeof req.body.roles !== "object") {
        return res.status(400).json({ error: "Request body must contain a roles object or array" });
    }

    const normalizedLoadout = store.normalizeFullLoadout(req.body);
    const validationLoadout = {
        ...normalizedLoadout,
        roles: Object.values(normalizedLoadout.roles || {}),
    };
    const validation = index.validateLoadout(validationLoadout);
    store.setFullLoadout(req.params.playerId, normalizedLoadout);

    res.status(200).json({
        playerId: req.params.playerId,
        updatedAt: new Date().toISOString(),
        validation,
    });
});

// GET /api/loadout/:playerId/:roleId - single role loadout snapshot
app.get("/api/loadout/:playerId/:roleId", (req, res) => {
    const store = getLoadoutStore();
    const snapshot = store.getRoleLoadoutSnapshot(req.params.playerId, req.params.roleId);
    if (!snapshot) {
        return res.status(404).json({
            error: "Loadout snapshot not found",
            playerId: req.params.playerId,
            roleId: req.params.roleId,
        });
    }
    res.status(200).json(snapshot);
});

// ---- Loadout validation / filtering ----

// POST /api/loadout/validate - validate loadout JSON
app.post("/api/loadout/validate", (req, res) => {
    const index = getDefinitionIndex();
    const loadout = req.body && req.body.loadout ? req.body.loadout : req.body;
    const result = index.validateLoadout(loadout);
    res.status(200).json(result);
});

// POST /api/loadout/filter - remove incompatible loadout items
app.post("/api/loadout/filter", (req, res) => {
    const index = getDefinitionIndex();
    const loadout = req.body && req.body.loadout ? req.body.loadout : req.body;
    const filtered = index.filterLoadout(loadout);
    const removedCount = (filtered._filtered && filtered._filtered.removedItemCount) || 0;
    res.status(200).json({
        loadout: filtered,
        removedItemCount: removedCount,
    });
});

// ---- Health check ----
app.get("/api/health", (req, res) => {
    const index = getDefinitionIndex();
    const store = getLoadoutStore();
    res.status(200).json({
        status: "ok",
        definitions: {
            roles: index.roles.size,
            weapons: index.weapons.size,
            parts: index.parts.size,
            itemTypes: index.itemTypes.size,
        },
    });
});

const net = require('net');

const protobuf = require("protobufjs");

const crypto = require("crypto");

function WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes){
  let Root = protobuf.loadSync("./game/proto/Response/ResponseWrapper.proto");

  let ResponseWrapperType = Root.lookupType("ProjectBoundary.ResponseWrapper");

  let ResponseWrapper = ResponseWrapperType.create({MessageId: MessageId, RPCPath: RPCPath, ErrorCode: 0, Message: ResponseBytes});
  let ResponsePayload = ResponseWrapperType.encode(ResponseWrapper).finish();
  let ResponseLengthHeader = Buffer.alloc(4);
  ResponseLengthHeader.writeUint32BE(ResponsePayload.length);

  return Buffer.concat([ResponseLengthHeader, ResponsePayload]);
}

function WrapJSONMessageAndSerialize(MessageId, RPCPath, ResponseJSON){
  let Root = protobuf.loadSync("./game/proto/Response/JSONResponseWrapper.proto");

  let ResponseWrapperType = Root.lookupType("ProjectBoundary.JSONResponseWrapper");

  let ResponseWrapper = ResponseWrapperType.create({MessageId: MessageId, RPCPath: RPCPath, ErrorCode: 0, JSONMessage: JSON.stringify(ResponseJSON)});
  let ResponsePayload = ResponseWrapperType.encode(ResponseWrapper).finish();
  let ResponseLengthHeader = Buffer.alloc(4);
  ResponseLengthHeader.writeUint32BE(ResponsePayload.length);

  return Buffer.concat([ResponseLengthHeader, ResponsePayload]);
}

const ObjectOptions = {
  Enums: String,  // enums as string names
  longs: String,  // longs as strings (requires long.js)
  defaults: true, // includes default values
  arrays: true,   // populates empty arrays (repeated fields) even if defaults=false
  objects: true,  // populates empty objects (map fields) even if defaults=false
  oneofs: true    // includes virtual oneof fields set to the present field's name);
};

function BuildNotification(Title, Content, Background, LanguageCode, Platform, Timezone){
  return {
    Id: crypto.randomUUID().toString(),
    Title: Title,
    Content: Content,
    Background: Background,
    LanguageCode: LanguageCode,
    Platform: Platform,
    Unknown1: 1,
    Timezone: Timezone,
    Unknown2: 1
  }
}

const PATCHNOTES_4012026_TEXT = "Welcome to the second round of patchnotes! Most of this is bugfix-focused, and those fixes might not even work yet! Fun!\n\nNew Features:\n- PvE Match Support! This should (theoretically) allow you to take on Hard bots, either solo or CoOp! This still has to be hosted, but we might run some at some point!\n- Randomized map selection, from all available Boundary maps!\n- Proper TDM mode setup, first to 75 kills wins, should last 10 min!\n\nBugfixes:\n- HOPEFULLY fixed the 999/999 spawn bug, though we're gonna have to confirm this in a second to see if I actually fixed it or not!\n- Fixed up the logic server a little bit to make it somewhat more reliable, shouldn't crash as often now\n\nThat's it for today, hope y'all enjoy!"

const PATCHNOTES_3312026_TEXT = "Welcome to the first round of patches for Project Rebound!\nNew Features:\n- Basic emulation of the Logic Server. This allows you to see the news (hi), adjust settings, and not have to reboot the game for every match\n- In-Game Medals & Scoring! Go for those headshots :)\nBugfixes:\n- Fixed several bugs causing respawning early to softlock the game. There is still one more bug I'm working out here, but there should already be improvement here.\n- Upgraded to 128 tick servers! This might get reverted if horrific things happen, but for now enjoy 128tick Boundary!\n- Various optimizations to backend tech, should make your matches significantly more stable!"

const ALPHA_TEXT = "Welcome to the Project Rebound Alpha. Please be patient and respectful to me & your fellow playtesters. Matchmaking will prioritize short queues over full matches, so feel free to coordinate in the discord to get games going."

const PLAYLISTS_JSON = { "PVP": [{ "Name": "Playtest", "Title": [{ "en": "Playtest" }], "Description": [, { "en": "Playtest a very early version of Project Rebound" }], "SecondaryDescription": [{ "en": "Please report any bugs to @systemdev in the Boundary discord" }], "BigTitle": [{ "en": "Playtest" }], "BigDescription": [{ "en": "Playtest a very early version of Project Rebound" }], "PlotImage": [{ "zh": "Capture" }, { "en": "Capture" }], "LargePlotImage": [{ "zh": "Capture" }, { "en": "Capture" }], "GameModeList": ["Purge"], "bHasFilter": false, "bIsLive": true, "Priority": 1, "StartTime": 0, "StopTime": 0 }] };

let PartyPresence = "InMatching";

function BuildRegionList(){
  //[{RegionId: "336d1f3e-3ecb-11eb-a7dc-3b7705f20f56", RegionName: "us-east1"}]

  let RegionList = [];

  for(let Region in matchmakingUDPServerDiscoveryPayload.servers){
    RegionList.push({RegionId: Region.regionid, RegionName: "us-east1"});
  }

  return RegionList;
}

// ---- MatchServer HTTP Client ----

const http = require('http');

function matchServerRequest(method, path, body) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, MATCHSERVER_API);
    const bodyStr = body ? JSON.stringify(body) : '';
    const options = {
      hostname: url.hostname,
      port: url.port,
      path: url.pathname,
      method,
      headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(bodyStr) },
      timeout: 5000,
    };
    const req = http.request(options, (res) => {
      let data = '';
      res.on('data', chunk => data += chunk);
      res.on('end', () => {
        try { resolve(JSON.parse(data)); }
        catch(_) { resolve({ _raw: data }); }
      });
    });
    req.on('error', (e) => reject(e));
    req.on('timeout', () => { req.destroy(); reject(new Error('timeout')); });
    if (bodyStr) req.write(bodyStr);
    req.end();
  });
}

// In-memory matchmaking ticket store (persists across connections)
const matchTickets = new Map(); // ticketId -> { userId, gameMode, regionIds, status, serverIp, serverPort, createdAt }

function generateTicketId() {
  return crypto.randomUUID().toString();
}

// ---- MatchServer Integration ----

let fs = require("fs");

const server = net.createServer((socket) => {
  console.log('\n=== Client connected ===');
  console.log(`From: ${socket.remoteAddress}:${socket.remotePort}\n`);

  socket.on('data', async (rawdata) => {
    if(rawdata.length == 6 && rawdata.toString("hex") === "000000022f2f"){
      //console.log("[RECV] Keepalive");

      socket.write(rawdata);
      return;
    }

    while(rawdata.length > 0){
      let Length = rawdata.readUint32BE(0);

      let data = rawdata.subarray(0, Length + 4);

      rawdata = rawdata.subarray(4 + Length);

      let Root = protobuf.loadSync("./game/proto/Request/RequestWrapper.proto");

      let RequestWrapperType = Root.lookupType("ProjectBoundary.RequestWrapper");

      let RequestWrapper;
      
      try{
        RequestWrapper = RequestWrapperType.decode(data.subarray(4));
      }
      catch(e){

      }
      
      if(RequestWrapper != undefined){
      let RequestObj = RequestWrapperType.toObject(RequestWrapper, ObjectOptions);

      const MessageId = RequestObj.MessageId;
      const RPCPath = RequestObj.RPCPath;
      const MessageBytes = RequestObj.Message;

      if(gateSessions.has(RPCPath)){
        socket.playerId = gateSessions.get(RPCPath);
        console.log(`[SESSION] Bound protobuf socket to player ${socket.playerId}`);
        socket.write(data);
      }
      else if(RPCPath === "/assets.Assets/UpdateRoleArchiveV2"){
        console.log("[RECV] Update Role Archive V2!");

        // Decode UpdateRoleArchiveV2Request: field1=Operation, field2=RoleId, field3=ItemId, field4=SkinData
        let op = 1, roleId = '', itemId = '', skinData = null;
        if (MessageBytes && MessageBytes.length > 0) {
          try {
            Root = protobuf.loadSync("./game/proto/Request/UpdateRoleArchiveV2Request.proto");
            let ReqType = Root.lookupType("ProjectBoundary.UpdateRoleArchiveV2Request");
            let req = ReqType.toObject(ReqType.decode(MessageBytes), ObjectOptions);
            op = req.Operation ?? 1;
            roleId = req.RoleId || '';
            itemId = req.ItemId || '';
            skinData = req.SkinData || null;
          } catch(e) {
            console.log("[ARCHIVE] Failed to decode UpdateRoleArchiveV2:", e.message);
          }
        }
        if (skinData && skinData.length > 0) {
          try {
            // skinData is a Buffer from protobufjs decode
            const SkinType = protobuf.loadSync("./game/proto/Request/UpdateRoleArchiveV2Request.proto")
              .lookupType("ProjectBoundary.SkinPayload");
            const skinObj = SkinType.toObject(SkinType.decode(skinData), ObjectOptions);
            console.log(`[ARCHIVE] Update: op=${op} role=${roleId} item=${itemId} skinToken=${skinObj.TokenId || ''} ornament=${skinObj.OrnamentId || ''}`);
          } catch(_) {
            console.log(`[ARCHIVE] Update: op=${op} role=${roleId} item=${itemId} skinData=${skinData.length}b (raw)`);
          }
        } else {
          console.log(`[ARCHIVE] Update: op=${op} role=${roleId} item=${itemId}`);
        }

        if (roleId) {
          const store = getLoadoutStore();
          const index = getDefinitionIndex();
          const playerId = socket.playerId || TEMP_USER_ID;
          const data = store.load(playerId) || { playerId, roles: {} };
          if (!data.roles[roleId]) data.roles[roleId] = {};
          const role = data.roles[roleId];

          // Operation is not a pure slot index in captured traffic. Route by item
          // type first, then use observed ops only to choose between sibling slots.
          const SLOT_MAP = {
            2: 'leftPylon', 3: 'rightPylon', 4: 'mobilityModule',
            5: 'meleeWeapon', 6: 'primaryWeapon', 7: 'secondaryWeapon',
          };
          const setPrimaryWeapon = (nextWeapon) => {
            role.primaryWeapon = nextWeapon;
          };
          const setSecondaryWeapon = (nextWeapon) => {
            role.secondaryWeapon = nextWeapon;
          };
          const ensureWeaponArchive = (weaponId) => {
            if (!weaponId || weaponId === 'None') return;
            if (!role._weaponArchives || typeof role._weaponArchives !== 'object' || Array.isArray(role._weaponArchives)) {
              role._weaponArchives = {};
            }
            if (!role._weaponArchives[weaponId]) {
              const defaultRaw = store.buildDefaultWeaponArchiveRaw(roleId, weaponId);
              if (defaultRaw) role._weaponArchives[weaponId] = defaultRaw;
            }
          };

          if (itemId) {
            const itemType = index.getItemType(itemId);
            const roleDef = index.getRole(roleId) || index.getRole(String(roleId).toUpperCase());
            const allowed = (scope) => !roleDef || (roleDef[scope] && roleDef[scope].has(itemId));

            if (itemType === 'EPBItemType::Weapon' && allowed('weaponScope')) {
              if (op === 2 || op === 7) setSecondaryWeapon(itemId);
              else setPrimaryWeapon(itemId);
              ensureWeaponArchive(itemId);
            } else if (itemType === 'EPBItemType::Pod' && allowed('podScope')) {
              if (op === 6 || op === 3 || op === 7) role.rightPylon = itemId;
              else if (op === 5 || op === 2 || op === 1) role.leftPylon = itemId;
              else if (!role.leftPylon || role.leftPylon === 'None') role.leftPylon = itemId;
              else role.rightPylon = itemId;
            } else if (itemType === 'EPBItemType::Mobility' && allowed('mobilityScope')) {
              role.mobilityModule = itemId;
            } else if (itemType === 'EPBItemType::MeleeWeapon' && allowed('meleeWeaponScope')) {
              role.meleeWeapon = itemId;
            } else {
              console.log(`[ARCHIVE] Ignored slot update role=${roleId} op=${op} item=${itemId} type=${itemType || 'Unknown'}`);
            }
          } else if (skinData && skinData.length > 0) {
            // Skin-only update (no item change)
            try {
              const SkinType = protobuf.loadSync("./game/proto/Request/UpdateRoleArchiveV2Request.proto")
                .lookupType("ProjectBoundary.SkinPayload");
              const skinObj = SkinType.toObject(SkinType.decode(skinData), ObjectOptions);
              if (skinObj.TokenId) role._skinToken = skinObj.TokenId;
              if (skinObj.OrnamentId) role._ornamentId = skinObj.OrnamentId;
            } catch(_) {}
          } else {
            // Empty item = unequip
            if (SLOT_MAP[op]) {
              role[SLOT_MAP[op]] = 'None';
            } else if (op === 1) {
              // op=1 with empty item: unequip primary weapon so auto-detect
              // can place the next equipped weapon into the primary slot
              role.primaryWeapon = 'None';
            }
          }
          // Save skin data if present
          if (skinData && skinData.length > 0) {
            role._skinData = skinData.toString('hex');
            try {
              const SkinType = protobuf.loadSync("./game/proto/Request/UpdateRoleArchiveV2Request.proto")
                .lookupType("ProjectBoundary.SkinPayload");
              const skinObj = SkinType.toObject(SkinType.decode(skinData), ObjectOptions);
              if (skinObj.TokenId) role._skinToken = skinObj.TokenId;
              if (skinObj.OrnamentId) role._ornamentId = skinObj.OrnamentId;
            } catch(_) {}
          }
          store.save(playerId, data);
        }

        // Return success; the request body is decoded above only for the fields we persist.

        Root = protobuf.loadSync("./game/proto/Response/UpdateRoleArchiveV2.proto");

        let UpdateRoleArchiveV2Type = Root.lookupType("ProjectBoundary.UpdateRoleArchiveV2Response");

        let UpdateRoleArchiveV2 = UpdateRoleArchiveV2Type.create({StatusCode: 0});

        let ResponseBytes = UpdateRoleArchiveV2Type.encode(UpdateRoleArchiveV2).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/assets.Assets/GetPlayerArchiveV2"){
        console.log("[RECV] Player Archive V2!");

        Root = protobuf.loadSync("./game/proto/Request/GetPlayerArchiveV2Request.proto");

        let PlayerArchiveV2RequestType = Root.lookupType("ProjectBoundary.GetPlayerArchiveV2Request");

        let PlayerArchiveV2Request = PlayerArchiveV2RequestType.decode(MessageBytes);

        let PlayerArchiveV2RequestObj = PlayerArchiveV2RequestType.toObject(PlayerArchiveV2Request, ObjectOptions);

        const store = getLoadoutStore();
        const playerId = socket.playerId || TEMP_USER_ID;
        const roleIds = PlayerArchiveV2RequestObj.RoleIDs || [];
        const playerRoleDatas = store.getRoleArchive(playerId, roleIds);
        const fullData = store.load(playerId);
        const roles = (fullData && fullData.roles) || {};

        // Attach weapon archive and skin data
        for (const roleData of playerRoleDatas) {
          const savedRole = roles[roleData.RoleID] || {};
          const defaultSkin = store.getDefaultRoleSkinMetadata(roleData.RoleID);
          // Put primary last so singular field-3 decoders keep the legacy primary archive.
          roleData.WeaponArchiveRaw = store.getWeaponArchiveRawBundleForRole(
            savedRole,
            [roleData.SecondWeapon, roleData.PrimaryWeapon],
            roleData.RoleID
          );
          roleData.SkinToken = savedRole._skinToken || defaultSkin.skinToken || '';
          roleData.OrnamentId = savedRole._ornamentId || defaultSkin.ornamentId || '';
          if (savedRole._skinData) {
            try {
              const SkinType = protobuf.loadSync("./game/proto/Request/UpdateRoleArchiveV2Request.proto")
                .lookupType("ProjectBoundary.SkinPayload");
              const skinObj = SkinType.toObject(SkinType.decode(Buffer.from(savedRole._skinData, 'hex')), ObjectOptions);
              roleData.SkinToken = skinObj.TokenId || roleData.SkinToken;
              roleData.OrnamentId = skinObj.OrnamentId || roleData.OrnamentId;
            } catch(_) {}
          }
        }

        let ResponseObj = {PlayerRoleDatas: playerRoleDatas, PlayerLevel: 0};
        console.log("[ARCHIVE] Returning loadout data:", JSON.stringify(ResponseObj));

        Root = protobuf.loadSync("./game/proto/Response/GetPlayerArchiveV2Response.proto");

        let PlayerArchiveV2ResponseType = Root.lookupType("ProjectBoundary.GetPlayerArchiveV2Response");

        let PlayerArchiveV2Response = PlayerArchiveV2ResponseType.create(ResponseObj);

        let ResponseBytes = PlayerArchiveV2ResponseType.encode(PlayerArchiveV2Response).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/assets.Assets/QueryAssets"){
        console.log("[RECV] Query Assets!");

        Root = protobuf.loadSync("./game/proto/Response/QueryAssetsResponse.proto");

        let QueryAssetsResponseType = Root.lookupType("ProjectBoundary.QueryAssetsResponse");

        let ResponseObj = {ItemDatas: [], ItemCount: 0};
        const index = getDefinitionIndex();

        for(let itemId of index.itemTypes.keys()){
          ResponseObj.ItemDatas.push({
            ItemId: itemId,
            Unknown1: 1,
            Unknown2: 1,
            Unknown3: 1
          });
        }

        ResponseObj.ItemCount = ResponseObj.ItemDatas.length;
        console.log(`[ASSETS] Returning ${ResponseObj.ItemCount} items`);

        let QueryAssetsResponse = QueryAssetsResponseType.create(ResponseObj);

        let ResponseBytes = QueryAssetsResponseType.encode(QueryAssetsResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/notification.Notification/QueryNotification"){
        //console.log("[RECV] Query Notification!");

        Root = protobuf.loadSync("./game/proto/Request/QueryNotificationRequest.proto");

        let QueryNotificationRequestType = Root.lookupType("ProjectBoundary.QueryNotificationRequest");

        let QueryNotificationRequest = QueryNotificationRequestType.decode(MessageBytes);

        let QueryNotificationRequestObj = QueryNotificationRequestType.toObject(QueryNotificationRequest, ObjectOptions);

        const Platform = QueryNotificationRequestObj.Platform;

        const LanguageCode = QueryNotificationRequestObj.LanguageCode;

        // translate it or smth idfk

        Root = protobuf.loadSync("./game/proto/Response/QueryNotificationResponse.proto");

        let QueryNotificationResponseType = Root.lookupType("ProjectBoundary.QueryNotificationResponse");

        let QueryNotificationResponse = QueryNotificationResponseType.create({Unknown: 0, Notifications: [BuildNotification("4/01/2026 Patchnotes", PATCHNOTES_4012026_TEXT, "", LanguageCode, Platform, "America/New_York"), BuildNotification("3/31/2026 Patchnotes", PATCHNOTES_3312026_TEXT, "", LanguageCode, Platform, "America/New_York"), BuildNotification("Project Rebound Alpha", ALPHA_TEXT, "", LanguageCode, Platform, "America/New_York")]});

        let ResponseBytes = QueryNotificationResponseType.encode(QueryNotificationResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/party.party/Create"){
        //console.log("[RECV] Party Create!");
        
        Root = protobuf.loadSync("./game/proto/Response/CreatePartyResponse.proto");

        let CreatePartyResponseType = Root.lookupType("ProjectBoundary.CreatePartyResponse");

        let CreatePartyResponse = CreatePartyResponseType.create({StatusCode: 0, PartyId: crypto.randomUUID().toString(), PartyMembers: [TEMP_USER_ID]});

        let ResponseBytes = CreatePartyResponseType.encode(CreatePartyResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/party.party/Ready"){
        //console.log("[RECV] Party Ready!");

        Root = protobuf.loadSync("./game/proto/Request/PartyReadyRequest.proto");

        let PartyReadyRequestType = Root.lookupType("ProjectBoundary.PartyReadyRequest");

        let PartyReadyRequest = PartyReadyRequestType.decode(MessageBytes);

        let PartyReadyRequestObj = PartyReadyRequestType.toObject(PartyReadyRequest, ObjectOptions);

        const PartyId = PartyReadyRequestObj.PartyId;
        
        Root = protobuf.loadSync("./game/proto/Response/PartyReadyResponse.proto");

        let PartyReadyResponseType = Root.lookupType("ProjectBoundary.PartyReadyResponse");

        let PartyReadyResponse = PartyReadyResponseType.create({StatusCode: 0});

        let ResponseBytes = PartyReadyResponseType.encode(PartyReadyResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/party.party/Get"){
        //console.log("[RECV] Get Party!");
      }
      else if(RPCPath === "/chat.chat/TextFilter"){
        //console.log("[RECV] Text Filter!");
      }
      else if(RPCPath === "/party.party/SetPresence"){
        //console.log("[RECV] Set Party Presence!");
        
        Root = protobuf.loadSync("./game/proto/Request/SetPartyPresenceRequest.proto");

        let SetPartyPresenceRequestType = Root.lookupType("ProjectBoundary.SetPartyPresenceRequest");

        let SetPartyPresenceRequest = SetPartyPresenceRequestType.decode(MessageBytes);

        let SetPartyPresenceRequestObj = SetPartyPresenceRequestType.toObject(SetPartyPresenceRequest, ObjectOptions);

        const DecodedPartyPresence = SetPartyPresenceRequestObj.Presence;

        console.log(`[PARTY] Presence ${PartyPresence} => ${DecodedPartyPresence}`);

        PartyPresence = DecodedPartyPresence;

        Root = protobuf.loadSync("./game/proto/Response/SetPartyPresenceResponse.proto");

        let SetPartyPresenceResponseType = Root.lookupType("ProjectBoundary.SetPartyPresenceResponse");

        let SetPartyPresenceResponse = SetPartyPresenceResponseType.create({StatusCode: 0});

        let ResponseBytes = SetPartyPresenceResponseType.encode(SetPartyPresenceResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/party.party/QueryPresence"){
        //console.log("[RECV] Query Party Presence!");

        Root = protobuf.loadSync("./game/proto/Response/QueryPartyPresenceResponse.proto");

        let QueryPartyPresenceResponseType = Root.lookupType("ProjectBoundary.QueryPartyPresenceResponse");

        let QueryPartyPresenceResponse = QueryPartyPresenceResponseType.create({StatusCode: 0, PartyMembers: [{
          UserId: TEMP_USER_ID,
          Status: PartyPresence
        }]});

        let ResponseBytes = QueryPartyPresenceResponseType.encode(QueryPartyPresenceResponse).finish();

        //console.log(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes).toString("hex"));

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/matchmaking.Matchmaking/QueryUnityMatchmakingRegion"){
        console.log("[RECV] Query Matchmaking Region!");

        Root = protobuf.loadSync("./game/proto/Response/QueryMatchmakingRegionResponse.proto");

        let QueryMatchmakingRegionResponseType = Root.lookupType("ProjectBoundary.QueryMatchmakingRegionResponse");

        let QueryMatchmakingRegionResponse = QueryMatchmakingRegionResponseType.create({StatusCode: 0, Regions: BuildRegionList()});

        let ResponseBytes = QueryMatchmakingRegionResponseType.encode(QueryMatchmakingRegionResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/matchmaking.Matchmaking/StartUnityMatchmaking"){
        console.log("[RECV] Start Matchmaking!");

        let userId = '';
        let gameMode = 'Purge';
        let regionIds = [];
        try {
          Root = protobuf.loadSync("./game/proto/Request/StartMatchmakingRequest.proto");
          let ReqType = Root.lookupType("ProjectBoundary.StartMatchmakingRequest");
          let req = ReqType.toObject(ReqType.decode(MessageBytes), ObjectOptions);
          userId = req.Payload.MatchmakingRequestorUserId || TEMP_USER_ID;
          gameMode = req.GameMode || 'Purge';
          regionIds = (req.Payload.UnknownMessage || []).map(m => m.RegionId).filter(Boolean);
        } catch(e) {
          console.log("[MATCH] Failed to decode StartMatchmaking:", e.message);
        }

        // Try MatchServer API; fall back to in-memory ticket
        let ticketId = generateTicketId();
        let matchFound = false;
        try {
          const result = await matchServerRequest('POST', '/matchmaking/enqueue', {
            userId, regionIds, gameMode, ticketId,
          });
          ticketId = result.ticketId || ticketId;
          matchFound = result.status === 'found';
          if (matchFound) {
            matchTickets.set(ticketId, {
              userId, gameMode, regionIds,
              status: 'found',
              serverIp: result.serverIp,
              serverPort: result.serverPort,
              createdAt: Date.now(),
            });
          }
          console.log(`[MATCH] MatchServer: ticket=${ticketId} status=${result.status}`);
        } catch(_) {
          // MatchServer unavailable - queue locally
          matchTickets.set(ticketId, {
            userId, gameMode, regionIds,
            status: 'queued',
            serverIp: null, serverPort: null,
            createdAt: Date.now(),
          });
          console.log(`[MATCH] MatchServer unavailable, queued locally: ticket=${ticketId}`);
        }

        Root = protobuf.loadSync("./game/proto/Response/StartMatchmakingResponse.proto");
        let RespType = Root.lookupType("ProjectBoundary.StartMatchmakingResponse");
        let Resp = RespType.create({StatusCode: 0});
        let ResponseBytes = RespType.encode(Resp).finish();
        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/matchmaking.Matchmaking/QueryUnityMatchmaking"){
        // Parse ticketId from request
        let ticketId = '';
        try {
          Root = protobuf.loadSync("./game/proto/Response/matchmaking_ext.proto");
          let ReqType = Root.lookupType("ProjectBoundary.QueryUnityMatchmakingReq");
          let req = ReqType.toObject(ReqType.decode(MessageBytes), ObjectOptions);
          ticketId = req.ticketId || '';
        } catch(_) {}

        let status = 'queued', serverIp = '', serverPort = 0;
        if (ticketId && matchTickets.has(ticketId)) {
          const ticket = matchTickets.get(ticketId);
          status = ticket.status;
          serverIp = ticket.serverIp || '';
          serverPort = ticket.serverPort || 0;
        }

        // Poll MatchServer if available
        if (ticketId && status === 'queued') {
          try {
            const result = await matchServerRequest('GET', `/matchmaking/status/${ticketId}`);
            if (result.status === 'found') {
              const ticket = matchTickets.get(ticketId);
              if (ticket) {
                ticket.status = 'found';
                ticket.serverIp = result.serverIp;
                ticket.serverPort = result.serverPort;
              }
              status = 'found';
              serverIp = result.serverIp;
              serverPort = result.serverPort;
            }
          } catch(_) {}
        }

        // Return empty response (QueryUnityMatchmakingRes has no known fields)
        Root = protobuf.loadSync("./game/proto/Response/matchmaking_ext.proto");
        let RespType = Root.lookupType("ProjectBoundary.QueryUnityMatchmakingRes");
        let Resp = RespType.create({});
        let ResponseBytes = RespType.encode(Resp).finish();
        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/matchmaking.Matchmaking/StopUnityMatchmaking"){
        let ticketId = '';
        try {
          Root = protobuf.loadSync("./game/proto/Response/matchmaking_ext.proto");
          let ReqType = Root.lookupType("ProjectBoundary.StopUnityMatchmakingReq");
          let req = ReqType.toObject(ReqType.decode(MessageBytes), ObjectOptions);
          ticketId = req.ticketId || '';
        } catch(_) {}

        console.log(`[MATCH] Stop matchmaking: ticket=${ticketId}`);
        if (ticketId) {
          matchTickets.delete(ticketId);
          try {
            await matchServerRequest('POST', `/matchmaking/cancel/${ticketId}`);
          } catch(_) {}
        }

        Root = protobuf.loadSync("./game/proto/Response/matchmaking_ext.proto");
        let RespType = Root.lookupType("ProjectBoundary.StopUnityMatchmakingRes");
        let Resp = RespType.create({});
        let ResponseBytes = RespType.encode(Resp).finish();
        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/playerdata.PlayerDataClient/GetDataStatisticsInfo"){
        //console.log("[RECV] Get Data Statistics!");

        Root = protobuf.loadSync("./game/proto/Response/GetDataStatisticsInfoResponse.proto");

        let GetDataStatisticsInfoResponseType = Root.lookupType("ProjectBoundary.GetDataStatisticsInfoResponse");

        let GetDataStatisticsInfoResponse = GetDataStatisticsInfoResponseType.create({StatusCode: 0, Datapoints: []});

        let ResponseBytes = GetDataStatisticsInfoResponseType.encode(GetDataStatisticsInfoResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/matchmaking.Matchmaking/QueryPlayList"){
        console.log("[RECV] Query Playlists!");

        Root = protobuf.loadSync("./game/proto/Response/QueryPlaylistResponse.proto");

        let QueryPlaylistResponseType = Root.lookupType("ProjectBoundary.QueryPlaylistResponse");

        let QueryPlaylistResponse = QueryPlaylistResponseType.create({StatusCode: 0, PlaylistsJSON: JSON.stringify(PLAYLISTS_JSON)});

        let ResponseBytes = QueryPlaylistResponseType.encode(QueryPlaylistResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/profile.Profile/QueryCurrency"){
        //console.log("[RECV] Query Currency!");

        Root = protobuf.loadSync("./game/proto/Response/QueryCurrencyResponse.proto");

        let QueryCurrencyResponseType = Root.lookupType("ProjectBoundary.QueryCurrencyResponse");

        let QueryCurrencyResponse = QueryCurrencyResponseType.create({CurrencyA: 0, CurrencyB: 0, CurrencyC: 0, CurrencyD: 0, CurrencyE: 0});

        let ResponseBytes = QueryCurrencyResponseType.encode(QueryCurrencyResponse).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/assets.Assets/UpdateWeaponArchiveV2"){
        console.log(`[RECV] Update Weapon Archive V2! (${MessageBytes ? MessageBytes.length : 0} bytes)`);

        let weaponRoleId = '';
        let waParsed = null;
        if (MessageBytes && MessageBytes.length > 0) {
          try {
            const WARoot = protobuf.loadSync("./game/proto/Request/UpdateWeaponArchiveV2Request.proto");
            const WAReqType = WARoot.lookupType("ProjectBoundary.UpdateWeaponArchiveV2Request");
            waParsed = WAReqType.toObject(WAReqType.decode(MessageBytes), ObjectOptions);
            weaponRoleId = waParsed.RoleId || '';
            if (waParsed.WeaponArchive) {
              console.log(`[WEAPON] role=${weaponRoleId} weapon=${waParsed.WeaponArchive.WeaponId} slots=${(waParsed.WeaponArchive.Parts || []).length}`);
            }
          } catch(e) {
            console.log(`[WEAPON] Failed to decode: ${e.message}`);
            // Fallback: parse roleId from raw bytes
            if (MessageBytes[0] === 0x0a) {
              const len = MessageBytes[1];
              if (len < 128) weaponRoleId = MessageBytes.subarray(2, 2 + len).toString('utf-8');
            }
          }
        }

        if (weaponRoleId) {
          const store = getLoadoutStore();
          const playerId = socket.playerId || TEMP_USER_ID;
          const data = store.load(playerId) || { playerId, roles: {} };
          if (!data.roles[weaponRoleId]) data.roles[weaponRoleId] = {};

          const role = data.roles[weaponRoleId];
          const rawHex = MessageBytes ? MessageBytes.toString('hex') : '';
          const archiveWeaponId = waParsed && waParsed.WeaponArchive && waParsed.WeaponArchive.WeaponId
            ? waParsed.WeaponArchive.WeaponId
            : '';
          if (!role._weaponArchives || typeof role._weaponArchives !== 'object' || Array.isArray(role._weaponArchives)) {
            role._weaponArchives = {};
          }
          if (archiveWeaponId) {
            role._weaponArchives[archiveWeaponId] = rawHex;
          }
          // Backward-compatible field for old clients; the per-weapon map remains authoritative.
          role._weaponArchiveRaw = rawHex;

          // Weapon skin/ornament are part of the per-weapon raw archive. Do not
          // copy them into role skin fields used by PlayerRoleData.
          store.save(playerId, data);
        }

        Root = protobuf.loadSync("./game/proto/Response/UpdateRoleArchiveV2.proto");
        let RespType = Root.lookupType("ProjectBoundary.UpdateRoleArchiveV2Response");
        let Resp = RespType.create({StatusCode: 0});
        let ResponseBytes = RespType.encode(Resp).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/mission.Mission/QueryProgress"){
        Root = protobuf.loadSync("./game/proto/Response/mission.proto");

        let QueryProgressRespType = Root.lookupType("ProjectBoundary.QueryProgressResp");

        let QueryProgressResp = QueryProgressRespType.create({});

        let ResponseBytes = QueryProgressRespType.encode(QueryProgressResp).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/event.Event/QueryOperatingEvent"){
        Root = protobuf.loadSync("./game/proto/Response/event.proto");

        let QueryOperatingEventRespType = Root.lookupType("ProjectBoundary.QueryOperatingEventResp");

        let QueryOperatingEventResp = QueryOperatingEventRespType.create({});

        let ResponseBytes = QueryOperatingEventRespType.encode(QueryOperatingEventResp).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/eventtracking.EventTracking/Record"){
        Root = protobuf.loadSync("./game/proto/Response/eventtracking.proto");

        let RecordRespType = Root.lookupType("ProjectBoundary.RecordResp");

        let RecordResp = RecordRespType.create({});

        let ResponseBytes = RecordRespType.encode(RecordResp).finish();

        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/mission.Mission/QueryLoginRecord"){
        Root = protobuf.loadSync("./game/proto/Response/mission.proto");
        let RespType = Root.lookupType("ProjectBoundary.QueryLoginRecordResp");
        let Resp = RespType.create({});
        let ResponseBytes = RespType.encode(Resp).finish();
        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/mission.Mission/QueryActivitiesInfo"){
        Root = protobuf.loadSync("./game/proto/Response/mission.proto");
        let RespType = Root.lookupType("ProjectBoundary.QuestActivitiesInfoResp");
        let Resp = RespType.create({});
        let ResponseBytes = RespType.encode(Resp).finish();
        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else if(RPCPath === "/mission.Mission/QueryUserEvents"){
        Root = protobuf.loadSync("./game/proto/Response/mission.proto");
        let RespType = Root.lookupType("ProjectBoundary.QueryUserEventsResp");
        let Resp = RespType.create({});
        let ResponseBytes = RespType.encode(Resp).finish();
        socket.write(WrapMessageAndSerialize(MessageId, RPCPath, ResponseBytes));
      }
      else{
        console.log("[RECV] Undefined Message:\n", {
          path: RequestObj.RPCPath,
          MessageId: RequestObj.MessageId
        });

        //socket.write(data);
      }
    }
    }
    


  });

  socket.on('end', () => {
    console.log('\n=== Client disconnected ===\n');
  });

  socket.on('error', (err) => {
    console.error('Socket error:', err);
  });
});

let udp = require("dgram");
const { serialize } = require('v8');

const matchmakingUDPServer = udp.createSocket('udp4');

matchmakingUDPServer.on("error", (error) => {
  console.log("[MM] Server blew up!");
  console.log(error.toString());
  matchmakingUDPServer.close();
});

matchmakingUDPServer.on("close", () => {
  console.log("[MM] Shutdown!");
});

matchmakingUDPServer.on("message", (message, info) => {
  if(message[0] == 0x59){
    console.log("[MM] Recieved a new QoS message, echoing!");
    
    let header = Buffer.alloc(3);

    header[0] = 0x95;
    header[1] = 0x00;

    const resp = Buffer.concat([header, message.subarray(11)]);

    matchmakingUDPServer.send(resp, info.port, info.address, (error, bytesSend) => {
      console.log("Sent Info\n", {
        error: error,
        bytesSent: bytesSend,
        addr: info.address,
        port: info.port,
        req: message.toString("hex"),
        resp: resp.toString("hex")
      });
    });
  }
  else{
    console.log("[MM] Recv'd an unknown message!");
    console.log(message);
  }
});

matchmakingUDPServer.on("listening", () => {
  console.log(`mrooooow >.< - ${9000}`);
});

const matchmakingTCPServer = net.createServer((socket) => {
  console.log('\n=== Client connected ===');
  console.log(`From: ${socket.remoteAddress}:${socket.remotePort}\n`);

  socket.on('data', (rawdata) => {
    console.log("MOGGEDDDDDDDDD");
  });
});

app.listen(process.env.PORT || 8000, () => {
    console.log(`mrow :3 - ${process.env.PORT || 8000}`);

    server.listen(6968, () => {
      console.log(`miau >:3 - ${6968}`);

      matchmakingUDPServer.bind(9000);

      matchmakingTCPServer.listen(9000);
    })
});
