// ======================================================
//  loadoutStore.js - player loadout persistence
// ======================================================
//  Shared by REST endpoints and protobuf handlers.
//
//  Storage format (data/loadouts/{playerId}.json):
//    {
//      "playerId": "...",
//      "updatedAt": "2026-04-28T...",
//      "roles": {
//        "PEACE": {
//          "primaryWeapon": "PEACE_GSW-AR",
//          "secondaryWeapon": "PEACE_RU-APS",
//          "leftPylon": "PEACE_ATK-HE",
//          "rightPylon": "None",
//          "mobilityModule": "PEACE_FCM-BOOST",
//          "meleeWeapon": "MELEE-KNIFE",
//          "loadoutSnapshot": { ... }  // optional structured JSON for Payload
//        }
//      }
//    }

const fs = require("fs");
const path = require("path");
const protobuf = require("protobufjs");
const { getDefinitionIndex } = require("./definitionIndex");

const DATA_DIR = path.join(__dirname, "..", "data", "loadouts");
const SKIN_PROTO = path.join(__dirname, "proto", "Request", "UpdateRoleArchiveV2Request.proto");
const WEAPON_ARCHIVE_PROTO = path.join(__dirname, "proto", "Request", "UpdateWeaponArchiveV2Request.proto");
const ITEM_TYPE = {
    Weapon: "EPBItemType::Weapon",
    MeleeWeapon: "EPBItemType::MeleeWeapon",
    Mobility: "EPBItemType::Mobility",
    Pod: "EPBItemType::Pod",
};
const WEAPON_ARCHIVE_SLOT_SCOPES = [
    [1, "MuzzleScope"],
    [2, "BarrelScope"],
    [3, "HandGuardScope"],
    [4, "ReceiverUpperScope"],
    [5, "GripScope"],
    [6, "SightOpticalScope"],
    [7, "PointerScope"],
    [8, "SightIronScope"],
    [9, "AmmoStorageDeviceScope"],
    [10, "StockScope"],
];

class LoadoutStore {
    constructor() {
        this.cache = new Map();
    }

    // ---- Internal paths ----

    _playerPath(playerId) {
        const safeId = playerId.replace(/[^a-zA-Z0-9_-]/g, "_");
        return path.join(DATA_DIR, `${safeId}.json`);
    }

    _ensureDir() {
        if (!fs.existsSync(DATA_DIR)) {
            fs.mkdirSync(DATA_DIR, { recursive: true });
        }
    }

    // ---- Load / save ----

    /**
     * @param {string} playerId
     * @returns {object|null}
     */
    load(playerId) {
        if (this.cache.has(playerId)) {
            return this.cache.get(playerId);
        }

        const filePath = this._playerPath(playerId);
        if (!fs.existsSync(filePath)) {
            return null;
        }

        try {
            const raw = fs.readFileSync(filePath, "utf8");
            const data = JSON.parse(raw);
            this.attachDecodedMetadata(data);
            this.cache.set(playerId, data);
            return data;
        } catch (e) {
            console.error(`[LoadoutStore] Failed to load loadout for ${playerId}:`, e.message);
            return null;
        }
    }

    /**
     * @param {string} playerId
     * @param {object} data
     */
    save(playerId, data) {
        this._ensureDir();

        const doc = {
            playerId,
            updatedAt: new Date().toISOString(),
            roles: data.roles || {},
        };
        this.attachDecodedMetadata(doc);

        const filePath = this._playerPath(playerId);
        try {
            fs.writeFileSync(filePath, JSON.stringify(doc, null, 2), "utf8");
            this.cache.set(playerId, doc);
            console.log(`[LoadoutStore] Saved loadout for ${playerId}`);
        } catch (e) {
            console.error(`[LoadoutStore] Failed to save loadout for ${playerId}:`, e.message);
            throw e;
        }
    }

    toFlatItemId(value, fallback = "None") {
        if (typeof value === "string") {
            return value || fallback;
        }
        if (value && typeof value === "object" && !Array.isArray(value)) {
            if (typeof value.id === "string" && value.id) return value.id;
            if (typeof value.mobilityModuleId === "string" && value.mobilityModuleId) {
                return value.mobilityModuleId;
            }
        }
        return fallback;
    }

    toFlatStringId(value, fallback = null) {
        if (typeof value !== "string") return fallback;
        return value || "None";
    }

    decodeSkinData(hex) {
        if (!hex || typeof hex !== "string") return null;
        try {
            const SkinType = protobuf.loadSync(SKIN_PROTO).lookupType("ProjectBoundary.SkinPayload");
            const decoded = SkinType.toObject(SkinType.decode(Buffer.from(hex, "hex")), {
                longs: String,
                enums: String,
                bytes: Buffer,
                defaults: true,
                arrays: true,
                objects: true,
            });
            return {
                tokenId: decoded.TokenId || "",
                ornamentId: decoded.OrnamentId || "",
            };
        } catch (_) {
            return null;
        }
    }

    decodeWeaponArchiveRaw(hex) {
        if (!hex || typeof hex !== "string") return null;
        try {
            const ArchiveType = protobuf.loadSync(WEAPON_ARCHIVE_PROTO)
                .lookupType("ProjectBoundary.UpdateWeaponArchiveV2Request");
            const decoded = ArchiveType.toObject(ArchiveType.decode(Buffer.from(hex, "hex")), {
                longs: String,
                enums: String,
                bytes: Buffer,
                defaults: true,
                arrays: true,
                objects: true,
            });
            const archive = decoded.WeaponArchive || {};
            const skin = archive.Skin || {};
            const skinInfo = skin.SkinInfo || {};
            return {
                roleId: decoded.RoleId || "",
                weaponId: archive.WeaponId || "",
                parts: (archive.Parts || []).map((part) => {
                    const info = part.Ornament && part.Ornament.Info ? part.Ornament.Info : {};
                    return {
                        slotId: part.SlotId || 0,
                        partId: part.PartId || "",
                        ornamentType: info.Type || "",
                        ornamentId: info.Id || "",
                    };
                }),
                skin: {
                    type: skinInfo.Type || "",
                    id: skinInfo.Id || "",
                    weaponOrnament: skin.WeaponOrnament || "",
                },
            };
        } catch (_) {
            return null;
        }
    }

    attachDecodedMetadata(data) {
        if (!data || typeof data !== "object" || !data.roles || typeof data.roles !== "object") return data;

        for (const roleData of Object.values(data.roles)) {
            if (!roleData || typeof roleData !== "object") continue;

            delete roleData._weaponArchivesParsed;
            delete roleData._weaponArchiveRawParsed;
            delete roleData._skinDataParsed;

            if (roleData._weaponArchives && typeof roleData._weaponArchives === "object" && !Array.isArray(roleData._weaponArchives)) {
                const parsedArchives = {};
                for (const [weaponId, rawHex] of Object.entries(roleData._weaponArchives)) {
                    const parsed = this.decodeWeaponArchiveRaw(rawHex);
                    if (parsed) parsedArchives[weaponId] = parsed;
                }
                if (Object.keys(parsedArchives).length > 0) {
                    roleData._weaponArchivesParsed = parsedArchives;
                }
            }

            const parsedRaw = this.decodeWeaponArchiveRaw(roleData._weaponArchiveRaw);
            if (parsedRaw) roleData._weaponArchiveRawParsed = parsedRaw;

            const parsedSkin = this.decodeSkinData(roleData._skinData);
            if (parsedSkin) roleData._skinDataParsed = parsedSkin;
        }

        return data;
    }

    getDefaultRoleSkinMetadata(roleId) {
        const { index, role } = this.getRoleDefinition(roleId);
        const skinToken = this.firstFromSet(role && role.spaceSuitSkinScope, 0);
        if (this.isNoneish(skinToken)) return { skinToken: "", ornamentId: "" };

        const originalPainting = `${skinToken}_PTOriginal`;
        return {
            skinToken,
            ornamentId: index.getItemType(originalPainting) ? originalPainting : "",
        };
    }

    buildDefaultWeaponSkinInfo(baseWeaponId, weaponDef) {
        const suitType = this.firstFromSet(weaponDef && weaponDef.slotScopes && weaponDef.slotScopes.SuitScope, 0);
        if (this.isNoneish(suitType)) {
            return { SkinInfo: { Type: "", Id: "" }, WeaponOrnament: "WO-NONE" };
        }

        return {
            SkinInfo: {
                Type: suitType,
                Id: `${baseWeaponId}_Original_PTOriginal`,
            },
            WeaponOrnament: "WO-NONE",
        };
    }

    buildDefaultWeaponArchiveRaw(roleId, weaponId) {
        if (this.isNoneish(roleId) || this.isNoneish(weaponId)) return "";

        const { index } = this.getRoleDefinition(roleId);
        const baseWeaponId = index.resolveBaseWeaponId(weaponId) || weaponId;
        const weaponDef = index.getWeapon(baseWeaponId);
        if (!weaponDef) return "";

        try {
            const ArchiveType = protobuf.loadSync(WEAPON_ARCHIVE_PROTO)
                .lookupType("ProjectBoundary.UpdateWeaponArchiveV2Request");
            const parts = WEAPON_ARCHIVE_SLOT_SCOPES.map(([slotId, scopeName]) => {
                const partId = this.firstFromSet(weaponDef.slotScopes[scopeName], 0);
                return {
                    SlotId: slotId,
                    PartId: this.isNoneish(partId) ? "" : partId,
                    Ornament: { Info: { Type: "", Id: "" } },
                };
            });
            const message = ArchiveType.create({
                RoleId: roleId,
                WeaponArchive: {
                    WeaponId: weaponId,
                    Parts: parts,
                    Skin: this.buildDefaultWeaponSkinInfo(baseWeaponId, weaponDef),
                },
            });
            return Buffer.from(ArchiveType.encode(message).finish()).toString("hex");
        } catch (_) {
            return "";
        }
    }

    copyArchiveMetadata(target, source) {
        if (!source || typeof source !== "object") return;
        if (source._weaponArchiveRaw) target._weaponArchiveRaw = source._weaponArchiveRaw;
        if (source._weaponArchives && typeof source._weaponArchives === "object" && !Array.isArray(source._weaponArchives)) {
            target._weaponArchives = { ...source._weaponArchives };
        }
        if (source._skinToken) target._skinToken = source._skinToken;
        if (source._ornamentId) target._ornamentId = source._ornamentId;
        if (source._skinData) target._skinData = source._skinData;
        const decodedSkin = this.decodeSkinData(source._skinData);
        if (decodedSkin && decodedSkin.tokenId) target._skinToken = decodedSkin.tokenId;
        if (decodedSkin && decodedSkin.ornamentId) target._ornamentId = decodedSkin.ornamentId;
    }

    getWeaponArchiveRawForRole(roleData, preferredWeaponId = null, roleId = null) {
        if (!roleData || typeof roleData !== "object") return "";
        const archives = roleData._weaponArchives && typeof roleData._weaponArchives === "object" && !Array.isArray(roleData._weaponArchives)
            ? roleData._weaponArchives
            : {};
        const weaponId = this.toFlatItemId(preferredWeaponId || roleData.primaryWeapon, null);
        if (weaponId && archives[weaponId]) return archives[weaponId];
        if (Object.keys(archives).length === 0 && roleData._weaponArchiveRaw) return roleData._weaponArchiveRaw;
        return this.buildDefaultWeaponArchiveRaw(roleId, weaponId);
    }

    getRoleDefinition(roleId) {
        const index = getDefinitionIndex();
        if (index.getRole(roleId)) return { index, canonicalRoleId: roleId, role: index.getRole(roleId) };

        const upperRoleId = typeof roleId === "string" ? roleId.toUpperCase() : roleId;
        if (upperRoleId && index.getRole(upperRoleId)) {
            return { index, canonicalRoleId: upperRoleId, role: index.getRole(upperRoleId) };
        }

        for (const knownRoleId of index.getAllRoleIds()) {
            if (typeof roleId === "string" && knownRoleId.toLowerCase() === roleId.toLowerCase()) {
                return { index, canonicalRoleId: knownRoleId, role: index.getRole(knownRoleId) };
            }
        }

        return { index, canonicalRoleId: roleId, role: null };
    }

    firstFromSet(set, offset = 0) {
        if (!set || typeof set[Symbol.iterator] !== "function") return "None";
        const values = Array.from(set).filter(Boolean);
        return values[offset] || "None";
    }

    buildDefaultRoleData(roleId) {
        const { role } = this.getRoleDefinition(roleId);
        if (!role) return {};

        return {
            primaryWeapon: this.firstFromSet(role.weaponScope, 0),
            secondaryWeapon: this.firstFromSet(role.weaponScope, 1),
            leftPylon: this.firstFromSet(role.podScope, 0),
            rightPylon: this.firstFromSet(role.podScope, 1),
            mobilityModule: this.firstFromSet(role.mobilityScope, 0),
            meleeWeapon: this.firstFromSet(role.meleeWeaponScope, 0),
        };
    }

    isNoneish(value) {
        return !value || value === "None";
    }

    isAllowedForSlot(role, slot, itemId) {
        if (!role || this.isNoneish(itemId)) return false;
        if (slot === "primaryWeapon" || slot === "secondaryWeapon") return role.weaponScope.has(itemId);
        if (slot === "leftPylon" || slot === "rightPylon") return role.podScope.has(itemId);
        if (slot === "mobilityModule") return role.mobilityScope.has(itemId);
        if (slot === "meleeWeapon") return role.meleeWeaponScope.has(itemId);
        return false;
    }

    placeItemByType(target, role, index, itemId, preferredSlot = null) {
        if (this.isNoneish(itemId)) return false;
        const itemType = index.getItemType(itemId);

        if (itemType === ITEM_TYPE.Weapon && role.weaponScope.has(itemId)) {
            if (preferredSlot === "secondaryWeapon") target.secondaryWeapon = itemId;
            else if (preferredSlot === "primaryWeapon") target.primaryWeapon = itemId;
            else if (this.isNoneish(target.primaryWeapon)) target.primaryWeapon = itemId;
            else target.secondaryWeapon = itemId;
            return true;
        }

        if (itemType === ITEM_TYPE.Pod && role.podScope.has(itemId)) {
            if (preferredSlot === "rightPylon") target.rightPylon = itemId;
            else if (preferredSlot === "leftPylon") target.leftPylon = itemId;
            else if (this.isNoneish(target.leftPylon)) target.leftPylon = itemId;
            else target.rightPylon = itemId;
            return true;
        }

        if (itemType === ITEM_TYPE.Mobility && role.mobilityScope.has(itemId)) {
            target.mobilityModule = itemId;
            return true;
        }

        if (itemType === ITEM_TYPE.MeleeWeapon && role.meleeWeaponScope.has(itemId)) {
            target.meleeWeapon = itemId;
            return true;
        }

        return false;
    }

    getNormalizedRoleData(roleId, roleData = {}) {
        const { index, role } = this.getRoleDefinition(roleId);
        if (!role) return { ...(roleData || {}) };

        const normalized = this.buildDefaultRoleData(roleId);
        const slotKeys = [
            "primaryWeapon",
            "secondaryWeapon",
            "leftPylon",
            "rightPylon",
            "mobilityModule",
            "meleeWeapon",
        ];

        for (const slot of slotKeys) {
            const itemId = this.toFlatItemId(roleData[slot], null);
            if (this.isNoneish(itemId)) continue;
            if (this.isAllowedForSlot(role, slot, itemId)) {
                normalized[slot] = itemId;
            } else {
                this.placeItemByType(normalized, role, index, itemId, slot);
            }
        }

        this.copyArchiveMetadata(normalized, roleData);
        return normalized;
    }

    // ---- Native protobuf shape ----

    /**
     * @param {string} playerId
     * @param {string[]} roleIds
     * @returns {object[]} PlayerRoleData array
     */
    getRoleArchive(playerId, roleIds) {
        const data = this.load(playerId);
        const roles = (data && data.roles) || {};

        return roleIds.map((roleId) => {
            const roleData = this.getNormalizedRoleData(roleId, roles[roleId] || {});
            return {
                RoleID: roleId,
                LeftPylon: this.toFlatItemId(roleData.leftPylon),
                RightPylon: this.toFlatItemId(roleData.rightPylon),
                MobilityModule: this.toFlatItemId(roleData.mobilityModule),
                MeleeWeapon: this.toFlatItemId(roleData.meleeWeapon),
                PrimaryWeapon: this.toFlatItemId(roleData.primaryWeapon),
                SecondWeapon: this.toFlatItemId(roleData.secondaryWeapon),
            };
        });
    }

    /**
     * Update a single role archive from UpdateRoleArchiveV2.
     * @param {string} playerId
     * @param {string} roleId
     * @param {object} roleArchive
     */
    updateRoleArchive(playerId, roleId, roleArchive) {
        const data = this.load(playerId) || { playerId, roles: {} };

        data.roles[roleId] = {
            ...(data.roles[roleId] || {}),
            primaryWeapon: roleArchive.PrimaryWeapon || "None",
            secondaryWeapon: roleArchive.SecondWeapon || roleArchive.SecondaryWeapon || "None",
            leftPylon: roleArchive.LeftPylon || "None",
            rightPylon: roleArchive.RightPylon || "None",
            mobilityModule: roleArchive.MobilityModule || "None",
            meleeWeapon: roleArchive.MeleeWeapon || "None",
        };

        this.save(playerId, data);
    }

    // ---- Loadout JSON snapshots for Payload ----

    /**
     * Get a single role loadout snapshot.
     * @param {string} playerId
     * @param {string} roleId
     * @returns {object|null}
     */
    getRoleLoadoutSnapshot(playerId, roleId) {
        const data = this.load(playerId);
        const roleData = data && data.roles ? data.roles[roleId] : null;
        if (!roleData && !this.getRoleDefinition(roleId).role) return null;
        if (roleData && roleData.loadoutSnapshot) {
            return this.withSnapshotMetadata(roleId, roleData.loadoutSnapshot, roleData);
        }
        return this.buildFlatRoleSnapshot(roleId, this.getNormalizedRoleData(roleId, roleData || {}));
    }

    /**
     * Save a single role loadout snapshot.
     * @param {string} playerId
     * @param {string} roleId
     * @param {object} snapshot
     */
    setRoleLoadoutSnapshot(playerId, roleId, snapshot) {
        const data = this.load(playerId) || { playerId, roles: {} };

        if (!data.roles[roleId]) {
            data.roles[roleId] = {};
        }
        data.roles[roleId].loadoutSnapshot = snapshot;

        if (snapshot) {
            this.applySnapshotSummary(data.roles[roleId], snapshot);
        }

        this.save(playerId, data);
    }

    /**
     * Get all stored role loadouts for a player.
     * @param {string} playerId
     * @returns {object|null}
     */
    getFullLoadout(playerId) {
        const data = this.load(playerId);
        if (!data) return null;

        const result = {
            playerId: data.playerId,
            updatedAt: data.updatedAt,
            roles: {},
        };

        for (const [roleId, roleData] of Object.entries(data.roles || {})) {
            const normalizedRoleData = this.getNormalizedRoleData(roleId, roleData);
            result.roles[roleId] = roleData.loadoutSnapshot
                ? this.withSnapshotMetadata(roleId, roleData.loadoutSnapshot, normalizedRoleData)
                : this.buildFlatRoleSnapshot(roleId, normalizedRoleData);
        }

        return result;
    }

    normalizeFullLoadout(fullLoadout) {
        const normalized = { ...(fullLoadout || {}), roles: {} };
        const roles = fullLoadout && fullLoadout.roles;

        if (Array.isArray(roles)) {
            for (const role of roles) {
                if (!role || typeof role !== "object") continue;
                const roleId = role.roleId || role.RoleID;
                if (!roleId) continue;
                normalized.roles[roleId] = { ...role, roleId };
            }
            return normalized;
        }

        if (roles && typeof roles === "object") {
            for (const [roleId, role] of Object.entries(roles)) {
                if (!role || typeof role !== "object") continue;
                normalized.roles[roleId] = {
                    ...role,
                    roleId: role.roleId || role.RoleID || roleId,
                };
            }
        }

        return normalized;
    }

    isStructuredRoleSnapshot(snapshot) {
        if (!snapshot || typeof snapshot !== "object") return false;
        const isObject = (value) => Boolean(value && typeof value === "object" && !Array.isArray(value));
        return Boolean(
            isObject(snapshot.inventory) ||
            isObject(snapshot.weaponConfigs) ||
            isObject(snapshot.characterData) ||
            isObject(snapshot.leftLauncher) ||
            isObject(snapshot.rightLauncher) ||
            isObject(snapshot.meleeWeapon) ||
            isObject(snapshot.mobilityModule)
        );
    }

    applyFlatRoleData(target, roleData) {
        const copyIfPresent = (fromKey, toKey = fromKey) => {
            if (Object.prototype.hasOwnProperty.call(roleData, fromKey)) {
                const value = this.toFlatStringId(roleData[fromKey], null);
                if (value !== null) target[toKey] = value;
            }
        };

        copyIfPresent("primaryWeapon");
        copyIfPresent("secondaryWeapon");
        copyIfPresent("leftPylon");
        copyIfPresent("leftPod", "leftPylon");
        copyIfPresent("leftLauncher", "leftPylon");
        copyIfPresent("rightPylon");
        copyIfPresent("rightPod", "rightPylon");
        copyIfPresent("rightLauncher", "rightPylon");
        copyIfPresent("mobilityModule");
        copyIfPresent("meleeWeapon");

        this.copyArchiveMetadata(target, roleData);
    }

    applySnapshotSummary(target, snapshot) {
        const wc = (snapshot && snapshot.weaponConfigs && typeof snapshot.weaponConfigs === "object")
            ? snapshot.weaponConfigs
            : {};
        const weaponIds = Object.keys(wc);
        target.primaryWeapon = weaponIds[0] || this.toFlatStringId(snapshot && snapshot.primaryWeapon, null) || target.primaryWeapon || "None";
        target.secondaryWeapon = weaponIds[1] || this.toFlatStringId(snapshot && snapshot.secondaryWeapon, null) || target.secondaryWeapon || "None";

        const leftPylon = this.toFlatItemId(snapshot && snapshot.leftLauncher, null)
            || this.toFlatStringId(snapshot && snapshot.leftPylon, null)
            || this.toFlatStringId(snapshot && snapshot.leftPod, null);
        const rightPylon = this.toFlatItemId(snapshot && snapshot.rightLauncher, null)
            || this.toFlatStringId(snapshot && snapshot.rightPylon, null)
            || this.toFlatStringId(snapshot && snapshot.rightPod, null);
        const mobilityModule = this.toFlatItemId(snapshot && snapshot.mobilityModule, null);
        const meleeWeapon = this.toFlatItemId(snapshot && snapshot.meleeWeapon, null);

        if (leftPylon !== null) target.leftPylon = leftPylon;
        if (rightPylon !== null) target.rightPylon = rightPylon;
        if (mobilityModule !== null) target.mobilityModule = mobilityModule;
        if (meleeWeapon !== null) target.meleeWeapon = meleeWeapon;

        this.copyArchiveMetadata(target, snapshot || {});
    }

    withSnapshotMetadata(roleId, snapshot, roleData) {
        const result = { ...(snapshot || {}) };
        if (!result.roleId && !result.RoleID) result.roleId = roleId;
        this.copyArchiveMetadata(result, roleData);
        const preferredWeaponId = result.primaryWeapon || result.PrimaryWeapon || result.weaponId || null;
        result._weaponArchiveRaw = this.getWeaponArchiveRawForRole(roleData, preferredWeaponId, roleId);
        const defaultSkin = this.getDefaultRoleSkinMetadata(roleId);
        result._skinToken = result._skinToken || defaultSkin.skinToken;
        result._ornamentId = result._ornamentId || defaultSkin.ornamentId;
        return result;
    }

    buildFlatRoleSnapshot(roleId, roleData) {
        const defaultSkin = this.getDefaultRoleSkinMetadata(roleId);
        const primaryWeapon = this.toFlatItemId(roleData.primaryWeapon);
        const secondaryWeapon = this.toFlatItemId(roleData.secondaryWeapon);
        const snapshot = {
            roleId,
            primaryWeapon,
            secondaryWeapon,
            leftPylon: this.toFlatItemId(roleData.leftPylon),
            rightPylon: this.toFlatItemId(roleData.rightPylon),
            leftPod: this.toFlatItemId(roleData.leftPylon),
            rightPod: this.toFlatItemId(roleData.rightPylon),
            leftLauncher: this.toFlatItemId(roleData.leftPylon),
            rightLauncher: this.toFlatItemId(roleData.rightPylon),
            mobilityModule: this.toFlatItemId(roleData.mobilityModule),
            meleeWeapon: this.toFlatItemId(roleData.meleeWeapon),
            _weaponArchiveRaw: this.getWeaponArchiveRawForRole(roleData, primaryWeapon, roleId),
            _weaponArchives: (roleData._weaponArchives && typeof roleData._weaponArchives === "object" && !Array.isArray(roleData._weaponArchives))
                ? { ...roleData._weaponArchives }
                : {},
            _skinToken: roleData._skinToken || defaultSkin.skinToken,
            _ornamentId: roleData._ornamentId || defaultSkin.ornamentId,
        };
        if (roleData._skinData) snapshot._skinData = roleData._skinData;
        return snapshot;
    }
    /**
     * Replace or merge stored role loadouts for a player.
     * @param {string} playerId
     * @param {object} fullLoadout
     */
    setFullLoadout(playerId, fullLoadout) {
        const data = this.load(playerId) || { playerId, roles: {} };
        const normalized = this.normalizeFullLoadout(fullLoadout);

        for (const [roleId, snapshot] of Object.entries(normalized.roles || {})) {
            if (!data.roles[roleId]) {
                data.roles[roleId] = {};
            }

            if (snapshot && snapshot.loadoutSnapshot && typeof snapshot.loadoutSnapshot === "object") {
                data.roles[roleId].loadoutSnapshot = snapshot.loadoutSnapshot;
                this.applySnapshotSummary(data.roles[roleId], snapshot.loadoutSnapshot);
                this.copyArchiveMetadata(data.roles[roleId], snapshot);
            } else if (this.isStructuredRoleSnapshot(snapshot)) {
                data.roles[roleId].loadoutSnapshot = snapshot;
                this.applySnapshotSummary(data.roles[roleId], snapshot);
            } else {
                delete data.roles[roleId].loadoutSnapshot;
                this.applyFlatRoleData(data.roles[roleId], snapshot || {});
            }
        }

        this.save(playerId, data);
    }
}

// Singleton
let instance = null;

function getLoadoutStore() {
    if (!instance) {
        instance = new LoadoutStore();
    }
    return instance;
}

module.exports = { LoadoutStore, getLoadoutStore };
