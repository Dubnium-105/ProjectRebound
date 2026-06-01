// ======================================================
//  definitionIndex.js — 物品定义内存索引
// ======================================================
//  启动时一次性加载 game/definitions/DT_*.json 到内存，
//  提供角色/武器/配件/装备的兼容性查询和配装修验。
//
//  数据来源：
//    DT_CharacterDefinition.json  — 角色及其 WeaponScope 等
//    DT_WeaponDefinition.json     — 武器及其各槽位配件 Scope
//    DT_WeaponPartDefinition.json — 配件定义
//    DT_RawItemType.json          — 基础武器 → 角色专有武器重定向
//    DT_ItemType.json             — 物品 ID → EPBItemType
//    DT_PodDefinition.json        — 副武器/吊舱定义
//    DT_MeleeWeaponDefinition.json— 近战武器定义
//    DT_MobilityDefinition.json   — 机动模块定义
//    DT_RadarDefinition.json      — 雷达定义
//    DT_VehicleDefinition.json    — 载具/EMU 定义
//    DT_WeaponConfigDefinition.json — 武器属性配置

const fs = require("fs");
const path = require("path");

const DEFINITIONS_DIR = path.join(__dirname, "definitions");

// ---- 槽位名 → JSON 字段名映射 ----
// DT_WeaponDefinition CustomScope 中的字段名
const WEAPON_SLOT_SCOPES = [
    "MuzzleScope",
    "BarrelScope",
    "HandGuardScope",
    "ReceiverUpperScope",
    "GripScope",
    "SightOpticalScope",
    "PointerScope",
    "SightIronScope",
    "AmmoStorageDeviceScope",
    "StockScope",
    "SuitScope",
    "OrnamentScope",
];

// DT_CharacterDefinition CustomScope 中的装备 Scope 字段名
const ROLE_EQUIPMENT_SCOPES = [
    "WeaponScope",
    "PodScope",
    "MeleeWeaponScope",
    "MobilityScope",
    "SpaceSuitSkinScope",
    "ArmBadgeScope",
    "HeadAccessoryScope",
    "VehicleScope",
];

// ---- EPBItemType 枚举（与游戏一致） ----
const ITEM_TYPE = {
    Character: "EPBItemType::Character",
    Weapon: "EPBItemType::Weapon",
    WeaponPart: "EPBItemType::WeaponPart",
    MeleeWeapon: "EPBItemType::MeleeWeapon",
    Mobility: "EPBItemType::Mobility",
    Pod: "EPBItemType::Pod",
    Radar: "EPBItemType::Radar",
    Vehicle: "EPBItemType::Vehicle",
    Skin: "EPBItemType::Skin",
    ArmBadge: "EPBItemType::ArmBadge",
    HeadAccessory: "EPBItemType::HeadAccessory",
    Ornament: "EPBItemType::Ornament",
    PartnerItem: "EPBItemType::PartnerItem",
};

// ---- 槽位类型（与 EPBPartSlotType 对应） ----
const SLOT_TYPE = {
    Muzzle: "Muzzle",
    Barrel: "Barrel",
    HandGuard: "HandGuard",
    ReceiverUpper: "ReceiverUpper",
    Grip: "Grip",
    SightOptical: "SightOptical",
    Pointer: "Pointer",
    SightIron: "SightIron",
    AmmoStorageDevice: "AmmoStorageDevice",
    Stock: "Stock",
    Suit: "Suit",
    Ornament: "Ornament",
    ReceiverMain: "ReceiverMain",
};

// ---- 角色装备槽位（与 EPBCharacterSlotType 对应） ----
const CHAR_SLOT = {
    FirstWeapon: "FirstWeapon",
    SecondWeapon: "SecondWeapon",
    LeftPod: "LeftPod",
    RightPod: "RightPod",
    MeleeWeapon: "MeleeWeapon",
    Mobility: "Mobility",
};

class DefinitionIndex {
    constructor() {
        // 角色定义：roleId → { weaponScope: Set, podScope: Set, meleeWeaponScope: Set, mobilityScope: Set, radarId, vehicleId, skinScopes: {...} }
        this.roles = new Map();
        // 武器定义：weaponId → { slotScopes: { slotName → Set }, receiverMain, suitScope: Set, ornamentScope: Set }
        this.weapons = new Map();
        // 配件定义：partId → { propertyConfigID, weaponPartClass }
        this.parts = new Map();
        // 物品类型：itemId → EPBItemType 字符串
        this.itemTypes = new Map();
        // 基础武器 → 角色专有武器：baseWeaponId → [roleWeaponId, ...]
        this.baseToRoleWeapon = new Map();
        // 角色专有武器 → 基础武器：roleWeaponId → baseWeaponId
        this.roleToBaseWeapon = new Map();
    }

    // =================================================================
    //  加载
    // =================================================================

    load() {
        this._loadCharacterDefinitions();
        this._loadWeaponDefinitions();
        this._loadWeaponPartDefinitions();
        this._loadRawItemTypes();
        this._loadItemTypes();
        console.log(
            `[DefinitionIndex] Loaded: ${this.roles.size} roles, ${this.weapons.size} weapons, ` +
            `${this.parts.size} parts, ${this.itemTypes.size} item types, ` +
            `${this.baseToRoleWeapon.size} base-weapon→role-weapon redirections`
        );
    }

    _readJSON(filename) {
        const filePath = path.join(DEFINITIONS_DIR, filename);
        if (!fs.existsSync(filePath)) {
            console.warn(`[DefinitionIndex] Missing definition file: ${filePath}`);
            return null;
        }
        const raw = JSON.parse(fs.readFileSync(filePath, "utf8"));
        // 定义文件是数组包裹的单元素：[{ Type, Name, Properties, Rows }]
        if (Array.isArray(raw) && raw.length > 0) {
            return raw[0];
        }
        return raw;
    }

    _loadCharacterDefinitions() {
        const data = this._readJSON("DT_CharacterDefinition.json");
        if (!data || !data.Rows) return;

        for (const [roleId, entry] of Object.entries(data.Rows)) {
            const cs = (entry && entry.CustomScope) || {};
            const def = {
                weaponScope: new Set(cs.WeaponScope || []),
                podScope: new Set(cs.PodScope || []),
                meleeWeaponScope: new Set(cs.MeleeWeaponScope || []),
                mobilityScope: new Set(cs.MobilityScope || []),
                spaceSuitSkinScope: new Set(cs.SpaceSuitSkinScope || []),
                armBadgeScope: new Set(cs.ArmBadgeScope || []),
                headAccessoryScope: new Set(cs.HeadAccessoryScope || []),
                vehicleScope: new Set(cs.VehicleScope || []),
                radarId: entry.RadarID || null,
                vehicleId: entry.VehicleID || null,
            };
            this.roles.set(roleId, def);
        }
    }

    _loadWeaponDefinitions() {
        const data = this._readJSON("DT_WeaponDefinition.json");
        if (!data || !data.Rows) return;

        for (const [weaponId, entry] of Object.entries(data.Rows)) {
            const cs = (entry && entry.CustomScope) || {};
            const slotScopes = {};
            for (const slotName of WEAPON_SLOT_SCOPES) {
                const arr = cs[slotName];
                if (Array.isArray(arr) && arr.length > 0) {
                    slotScopes[slotName] = new Set(arr);
                }
            }
            this.weapons.set(weaponId, {
                slotScopes,
                receiverMain: cs.ReceiverMain || null,
            });
        }
    }

    _loadWeaponPartDefinitions() {
        const data = this._readJSON("DT_WeaponPartDefinition.json");
        if (!data || !data.Rows) return;

        for (const [partId, entry] of Object.entries(data.Rows)) {
            this.parts.set(partId, {
                propertyConfigID: entry.PropertyConfigID || null,
            });
        }
    }

    _loadRawItemTypes() {
        const data = this._readJSON("DT_RawItemType.json");
        if (!data || !data.Rows) return;

        for (const [baseId, entry] of Object.entries(data.Rows)) {
            const redirections = entry.RedirectionItemArray || [];
            this.baseToRoleWeapon.set(baseId, redirections);
            for (const roleWeaponId of redirections) {
                this.roleToBaseWeapon.set(roleWeaponId, baseId);
            }
        }
    }

    _loadItemTypes() {
        const data = this._readJSON("DT_ItemType.json");
        if (!data || !data.Rows) return;

        for (const [itemId, entry] of Object.entries(data.Rows)) {
            if (entry && entry.Type) {
                this.itemTypes.set(itemId, entry.Type);
            }
        }
    }

    // =================================================================
    //  角色查询
    // =================================================================

    getRole(roleId) {
        return this.roles.get(roleId) || null;
    }

    hasRole(roleId) {
        return this.roles.has(roleId);
    }

    getAllRoleIds() {
        return Array.from(this.roles.keys());
    }

    // =================================================================
    //  武器查询
    // =================================================================

    getWeapon(weaponId) {
        return this.weapons.get(weaponId) || null;
    }

    hasWeapon(weaponId) {
        return this.weapons.has(weaponId);
    }

    getAllWeaponIds() {
        return Array.from(this.weapons.keys());
    }

    getWeaponSlotScope(weaponId, slotName) {
        const weapon = this.weapons.get(weaponId);
        if (!weapon) return null;
        return weapon.slotScopes[slotName] || null;
    }

    // =================================================================
    //  配件查询
    // =================================================================

    getPart(partId) {
        return this.parts.get(partId) || null;
    }

    // =================================================================
    //  物品类型查询
    // =================================================================

    getItemType(itemId) {
        return this.itemTypes.get(itemId) || null;
    }

    // =================================================================
    //  武器重定向 (DT_RawItemType)
    // =================================================================

    resolveRoleWeaponId(roleId, baseWeaponId) {
        // 给出基础武器 ID，返回角色专有武器 ID
        // 例如: ("PEACE", "GSW-AR") → "PEACE_GSW-AR"
        const target = `${roleId}_${baseWeaponId}`;
        const redirections = this.baseToRoleWeapon.get(baseWeaponId);
        if (redirections && redirections.includes(target)) {
            return target;
        }
        return null;
    }

    resolveBaseWeaponId(roleWeaponId) {
        // 给出角色专有武器 ID，返回基础武器 ID
        // 例如: "PEACE_GSW-AR" → "GSW-AR"
        return this.roleToBaseWeapon.get(roleWeaponId) || null;
    }

    // =================================================================
    //  兼容性校验
    // =================================================================

    isWeaponAllowedForRole(roleId, roleWeaponId) {
        const role = this.roles.get(roleId);
        if (!role) return false;
        return role.weaponScope.has(roleWeaponId);
    }

    isPartAllowedForWeaponSlot(baseWeaponId, slotScopeName, partId) {
        const weapon = this.weapons.get(baseWeaponId);
        if (!weapon) return false;
        const scope = weapon.slotScopes[slotScopeName];
        if (!scope) return false;
        return scope.has(partId);
    }

    isPartAllowedForWeapon(baseWeaponId, partId) {
        // 检查 partId 是否在武器的任意槽位 Scope 中
        const weapon = this.weapons.get(baseWeaponId);
        if (!weapon) return false;
        for (const scope of Object.values(weapon.slotScopes)) {
            if (scope.has(partId)) return true;
        }
        return false;
    }

    isEquipmentAllowedForRole(roleId, itemId, slotType) {
        const role = this.roles.get(roleId);
        if (!role) return false;

        switch (slotType) {
            case CHAR_SLOT.FirstWeapon:
            case CHAR_SLOT.SecondWeapon:
                return role.weaponScope.has(itemId);
            case CHAR_SLOT.LeftPod:
            case CHAR_SLOT.RightPod:
                return role.podScope.has(itemId);
            case CHAR_SLOT.MeleeWeapon:
                return role.meleeWeaponScope.has(itemId);
            case CHAR_SLOT.Mobility:
                return role.mobilityScope.has(itemId);
            default:
                return false;
        }
    }

    isOrnamentAllowedForWeapon(baseWeaponId, ornamentId) {
        const weapon = this.weapons.get(baseWeaponId);
        if (!weapon) return false;
        const scope = weapon.slotScopes["OrnamentScope"];
        if (!scope) return false;
        return scope.has(ornamentId);
    }

    // =================================================================
    //  配装修验 (loadout JSON)
    // =================================================================

    /**
     * @param {object} loadoutJson — 标准 loadout snapshot JSON
     *   { roles: [{ roleId, inventory, weaponConfigs, meleeWeapon, leftLauncher, rightLauncher, mobilityModule, characterData }] }
     * @returns {object} { valid: bool, errors: string[], warnings: string[] }
     */
    validateLoadout(loadoutJson) {
        const errors = [];
        const warnings = [];

        if (!loadoutJson || typeof loadoutJson !== "object") {
            return { valid: false, errors: ["Loadout is not a valid object"], warnings: [] };
        }

        const roles = loadoutJson.roles;
        if (!Array.isArray(roles) || roles.length === 0) {
            return { valid: false, errors: ["Loadout has no roles array"], warnings: [] };
        }

        for (const roleData of roles) {
            const roleId = roleData.roleId;
            if (!roleId) {
                warnings.push("Role entry missing roleId");
                continue;
            }

            const role = this.roles.get(roleId);
            if (!role) {
                warnings.push(`Unknown role: ${roleId}`);
                continue;
            }

            // ---- 校验武器配置 ----
            const weaponConfigs = roleData.weaponConfigs || {};
            for (const [weaponId, wc] of Object.entries(weaponConfigs)) {
                // 检查武器是否在角色武器范围内
                if (!role.weaponScope.has(weaponId)) {
                    warnings.push(`Weapon "${weaponId}" not in role "${roleId}" WeaponScope`);
                }

                // 检查配件
                const baseWeaponId = this.resolveBaseWeaponId(weaponId);
                const parts = wc.parts || [];
                if (Array.isArray(parts)) {
                    for (const part of parts) {
                        const partId = part.weaponPartId;
                        const slotType = part.slotType; // 这是一个 int/string，代表 EPBPartSlotType
                        if (partId && partId !== "None" && baseWeaponId) {
                            if (!this.isPartAllowedForWeapon(baseWeaponId, partId)) {
                                warnings.push(
                                    `Part "${partId}" not found in any slot scope of weapon "${baseWeaponId}" ` +
                                    `(role weapon: ${weaponId})`
                                );
                            }
                        }
                    }
                }

                // 检查 ornament
                const ornamentId = wc.ornamentId;
                if (ornamentId && ornamentId !== "None" && baseWeaponId) {
                    if (!this.isOrnamentAllowedForWeapon(baseWeaponId, ornamentId)) {
                        warnings.push(`Ornament "${ornamentId}" not in weapon "${baseWeaponId}" OrnamentScope`);
                    }
                }
            }

            // ---- 校验装备 ----
            // 近战武器
            const meleeId = roleData.meleeWeapon && roleData.meleeWeapon.id;
            if (meleeId && meleeId !== "None") {
                if (!role.meleeWeaponScope.has(meleeId)) {
                    warnings.push(`Melee weapon "${meleeId}" not in role "${roleId}" MeleeWeaponScope`);
                }
            }

            // 左/右发射器
            for (const side of ["leftLauncher", "rightLauncher"]) {
                const launcher = roleData[side];
                const launcherId = launcher && launcher.id;
                if (launcherId && launcherId !== "None") {
                    if (!role.podScope.has(launcherId)) {
                        warnings.push(`${side} "${launcherId}" not in role "${roleId}" PodScope`);
                    }
                }
            }

            // 机动模块
            const mobilityId = roleData.mobilityModule && roleData.mobilityModule.mobilityModuleId;
            if (mobilityId && mobilityId !== "None") {
                if (!role.mobilityScope.has(mobilityId)) {
                    warnings.push(`Mobility module "${mobilityId}" not in role "${roleId}" MobilityScope`);
                }
            }

            // ---- 校验库存 ----
            const inventory = roleData.inventory;
            if (inventory && Array.isArray(inventory.slots)) {
                for (const slot of inventory.slots) {
                    const itemId = slot.itemId;
                    if (itemId && itemId !== "None") {
                        const itemType = this.itemTypes.get(itemId);
                        if (!itemType) {
                            warnings.push(`Unknown item "${itemId}" in inventory of role "${roleId}"`);
                        }
                    }
                }
            }
        }

        return {
            valid: errors.length === 0,
            errors,
            warnings,
        };
    }

    /**
     * @param {object} loadoutJson
     * @returns {object} 滤除不兼容物品后的 loadout JSON 副本
     */
    filterLoadout(loadoutJson) {
        if (!loadoutJson || typeof loadoutJson !== "object") return loadoutJson;

        const filtered = JSON.parse(JSON.stringify(loadoutJson)); // 深拷贝
        const roles = filtered.roles;
        if (!Array.isArray(roles)) return filtered;

        let removedCount = 0;

        for (const roleData of roles) {
            const roleId = roleData.roleId;
            const role = this.roles.get(roleId);
            if (!role) continue;

            // 过滤武器配置 — 移除不在角色范围内的武器
            const weaponConfigs = roleData.weaponConfigs || {};
            for (const [weaponId, wc] of Object.entries(weaponConfigs)) {
                if (!role.weaponScope.has(weaponId)) {
                    delete roleData.weaponConfigs[weaponId];
                    removedCount++;
                    continue;
                }

                // 过滤配件 — 移除不在武器槽位范围内的配件
                const baseWeaponId = this.resolveBaseWeaponId(weaponId);
                if (baseWeaponId && Array.isArray(wc.parts)) {
                    wc.parts = wc.parts.filter((part) => {
                        const partId = part.weaponPartId;
                        if (!partId || partId === "None") return true; // 保留空配件
                        if (this.isPartAllowedForWeapon(baseWeaponId, partId)) return true;
                        removedCount++;
                        return false;
                    });
                }

                // 过滤 ornament
                const ornamentId = wc.ornamentId;
                if (ornamentId && ornamentId !== "None" && baseWeaponId) {
                    if (!this.isOrnamentAllowedForWeapon(baseWeaponId, ornamentId)) {
                        wc.ornamentId = "None";
                        removedCount++;
                    }
                }
            }

            // 过滤装备
            const meleeId = roleData.meleeWeapon && roleData.meleeWeapon.id;
            if (meleeId && meleeId !== "None" && !role.meleeWeaponScope.has(meleeId)) {
                roleData.meleeWeapon.id = "None";
                removedCount++;
            }

            for (const side of ["leftLauncher", "rightLauncher"]) {
                const launcher = roleData[side];
                const launcherId = launcher && launcher.id;
                if (launcherId && launcherId !== "None" && !role.podScope.has(launcherId)) {
                    launcher.id = "None";
                    removedCount++;
                }
            }

            const mobilityId = roleData.mobilityModule && roleData.mobilityModule.mobilityModuleId;
            if (mobilityId && mobilityId !== "None" && !role.mobilityScope.has(mobilityId)) {
                roleData.mobilityModule.mobilityModuleId = "None";
                removedCount++;
            }

            // 过滤库存
            const inventory = roleData.inventory;
            if (inventory && Array.isArray(inventory.slots)) {
                inventory.slots = inventory.slots.filter((slot) => {
                    const itemId = slot.itemId;
                    if (!itemId || itemId === "None") return true;
                    if (this.itemTypes.has(itemId)) return true;
                    removedCount++;
                    return false;
                });
            }
        }

        // 添加过滤元数据
        filtered._filtered = {
            filteredAt: new Date().toISOString(),
            removedItemCount: removedCount,
        };

        return filtered;
    }
}

// 单例
let instance = null;

function getDefinitionIndex() {
    if (!instance) {
        instance = new DefinitionIndex();
        instance.load();
    }
    return instance;
}

module.exports = { DefinitionIndex, getDefinitionIndex, ITEM_TYPE, SLOT_TYPE, CHAR_SLOT };
