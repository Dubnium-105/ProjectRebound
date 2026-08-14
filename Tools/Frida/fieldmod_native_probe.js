'use strict';

/*
 * Read-only PBFieldModManager native-function locator.
 *
 * Unreal registers the native implementation pointer in UFunction::ExecFunction.
 * Locating that pointer at runtime gives IDA an exact RVA without signature
 * guessing or patching the game.
 */

const SETTINGS = {
    moduleName: 'ProjectBoundarySteam-Win64-Shipping.exe',
    gObjects: 0x05D65FE0,
    appendString: 0x019D82B0,
    object: { class: 0x10, name: 0x18, outer: 0x20 },
    execFunction: 0xD8,
    maxObjects: 8 * 1024 * 1024,
};

const TARGETS = new Map([
    ['SelectCharacter', 'PBFieldModManager'],
    ['SelectCharacterSlot', 'PBFieldModManager'],
    ['SelectInventoryItem', 'PBFieldModManager'],
    ['GetPreOrderingItemIDInSlotType', 'PBFieldModManager'],
    ['SavePreOrderGameSaved', 'PBFieldModManager'],
    ['ClientInitFieldMod', 'PBPlayerState'],
    ['ClientRefreshRoleEquippingInventory', 'PBPlayerState'],
    ['ClientRefreshRolePreOrderingInventory', 'PBPlayerState'],
]);

const game = Process.getModuleByName(SETTINGS.moduleName);
const appendString = new NativeFunction(
    game.base.add(SETTINGS.appendString),
    'void',
    ['pointer', 'pointer']);
const nameCache = new Map();

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-fieldmod-native-probe',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function fnameToString(address) {
    const key = `${address.readS32()}:${address.add(4).readU32()}`;
    if (nameCache.has(key)) return nameCache.get(key);

    const data = Memory.alloc(0x2000);
    data.writeByteArray(new Uint8Array(0x2000));
    const output = Memory.alloc(Process.pointerSize + 8);
    output.writePointer(data);
    output.add(Process.pointerSize).writeS32(0);
    output.add(Process.pointerSize + 4).writeS32(0x1000);
    appendString(address, output);
    const value = data.readUtf16String() || '';
    nameCache.set(key, value);
    return value;
}

function objectName(object) {
    return fnameToString(object.add(SETTINGS.object.name));
}

function className(object) {
    const klass = object.add(SETTINGS.object.class).readPointer();
    return klass.isNull() ? '' : objectName(klass);
}

function dumpFieldModManager(object) {
    const map = object.add(0x98);
    const elements = map.readPointer();
    const allocated = map.add(8).readS32();
    const max = map.add(12).readS32();
    const flagBits = map.add(0x28).readS32();
    const secondaryFlags = map.add(0x20).readPointer();
    const flags = secondaryFlags.isNull() ? map.add(0x10) : secondaryFlags;
    if (allocated < 0 || max < allocated || flagBits < allocated ||
        (allocated > 0 && elements.isNull())) {
        throw new Error(`invalid FieldMod map allocated=${allocated} max=${max} flags=${flagBits}`);
    }

    const roles = [];
    for (let index = 0; index < allocated; index += 1) {
        const word = flags.add(Math.floor(index / 32) * 4).readU32();
        if ((word & (1 << (index % 32))) === 0) continue;

        const element = elements.add(index * 0x30);
        const roleId = fnameToString(element);
        const value = element.add(8);
        const slotData = value.readPointer();
        const slotCount = value.add(8).readS32();
        const itemData = value.add(0x10).readPointer();
        const itemCount = value.add(0x18).readS32();
        const slots = [];
        const count = Math.min(slotCount, itemCount, 32);
        if (count > 0 && !slotData.isNull() && !itemData.isNull()) {
            for (let itemIndex = 0; itemIndex < count; itemIndex += 1) {
                slots.push({
                    slot: slotData.add(itemIndex).readU8(),
                    item_id: fnameToString(itemData.add(itemIndex * 8)),
                });
            }
        }
        roles.push({ role_id: roleId, slot_count: slotCount, item_count: itemCount, slots });
    }
    return { manager: object.toString(), allocated, max, flag_bits: flagBits, roles };
}

function scanObjects() {
    const objectArray = game.base.add(SETTINGS.gObjects);
    const chunks = objectArray.readPointer();
    const numElements = objectArray.add(0x14).readS32();
    const numChunks = objectArray.add(0x1C).readS32();
    if (chunks.isNull() || numElements <= 0 || numElements > SETTINGS.maxObjects || numChunks <= 0) {
        throw new Error(`GObjects not ready num=${numElements} chunks=${numChunks}`);
    }

    const found = [];
    const managers = [];
    for (let index = 0; index < numElements; index += 1) {
        const chunkIndex = Math.floor(index / 0x10000);
        if (chunkIndex >= numChunks) break;
        const chunk = chunks.add(chunkIndex * Process.pointerSize).readPointer();
        if (chunk.isNull()) continue;
        const object = chunk.add((index % 0x10000) * 0x18).readPointer();
        if (object.isNull()) continue;

        try {
            const typeName = className(object);
            if (typeName === 'PBFieldModManager' && !objectName(object).startsWith('Default__')) {
                managers.push(dumpFieldModManager(object));
                continue;
            }
            if (typeName !== 'Function') continue;
            const functionName = objectName(object);
            if (!TARGETS.has(functionName)) continue;
            const outer = object.add(SETTINGS.object.outer).readPointer();
            const ownerName = outer.isNull() ? '' : objectName(outer);
            if (ownerName !== TARGETS.get(functionName)) continue;

            const exec = object.add(SETTINGS.execFunction).readPointer();
            found.push({
                function_name: functionName,
                owner_name: ownerName,
                ufunction: object.toString(),
                exec_function: exec.toString(),
                exec_rva: exec.isNull() ? null : exec.sub(game.base).toString(),
            });
        } catch (_) {
            // UObject slots can change during a scan.
        }
    }

    emit('probe.result', {
        pid: Process.id,
        module_base: game.base.toString(),
        object_count: numElements,
        functions: found,
        managers,
    });
}

setImmediate(() => {
    try {
        scanObjects();
    } catch (error) {
        emit('probe.error', { message: String(error), stack: error.stack || '' });
    }
});
