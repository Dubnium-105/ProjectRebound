'use strict';

// One-shot, read-only state capture for the fixed Boundary build.  This probe
// deliberately avoids PR_GetActiveNetDriver/PR_GetNetDriverSnapshot because
// those helpers may repair a cached World::NetDriver binding.

const SOURCE = 'project-rebound-multimatch-state-probe';

function pointerValue(address) {
    return address && !address.isNull() ? address.toString() : '0x0';
}

function safeReadPointer(address) {
    try { return address.readPointer(); } catch (_) { return ptr(0); }
}

function safeReadU8(address) {
    try { return address.readU8(); } catch (_) { return null; }
}

function safeReadS32(address) {
    try { return address.readS32(); } catch (_) { return null; }
}

function safeReadU32(address) {
    try { return address.readU32(); } catch (_) { return null; }
}

function safeReadFloat(address) {
    try { return address.readFloat(); } catch (_) { return null; }
}

function safeReadFString(address) {
    try {
        const data = address.readPointer();
        const count = address.add(Process.pointerSize).readS32();
        const capacity = address.add(Process.pointerSize + 4).readS32();
        if (data.isNull() || count <= 0 || count > 4096 || capacity < count || capacity > 8192) {
            return { data: pointerValue(data), count, capacity, text: null };
        }
        return {
            data: pointerValue(data),
            count,
            capacity,
            text: data.readUtf16String(Math.max(0, count - 1)),
        };
    } catch (error) {
        return { error: String(error) };
    }
}

function moduleAddress(address, image) {
    const module = Process.findModuleByAddress(address);
    return {
        address: pointerValue(address),
        module: module ? module.name : null,
        rva: module ? address.sub(module.base).toString() : null,
        image_rva: image && address.compare(image.base) >= 0 &&
            address.compare(image.base.add(image.size)) < 0
            ? address.sub(image.base).toString()
            : null,
    };
}

function hexBytes(address, length) {
    try {
        const bytes = new Uint8Array(address.readByteArray(length));
        return Array.from(bytes, value => value.toString(16).padStart(2, '0')).join(' ');
    } catch (error) {
        return 'error: ' + String(error);
    }
}

function makeNameDecoder(image) {
    const pool = image.base.add(0x5d29c80);
    return function decodeName(index) {
        try {
            const block = pool.add(0x10 + (index >>> 16) * Process.pointerSize).readPointer();
            const entry = block.add((index & 0xffff) * 2);
            const header = entry.readU16();
            const length = header >>> 6;
            if (length <= 0 || length > 1024) {
                return null;
            }
            return (header & 1) !== 0
                ? entry.add(2).readUtf16String(length)
                : entry.add(2).readUtf8String(length);
        } catch (_) {
            return null;
        }
    };
}

function captureGameModeClassChain(image) {
    const decodeName = makeNameDecoder(image);
    const objectArray = image.base.add(0x5d65fe0);
    const chunks = safeReadPointer(objectArray);
    const count = safeReadS32(objectArray.add(0x14));
    const targets = new Set(['GameModeBase', 'GameMode', 'PBGameMode']);
    const records = [];
    if (chunks.isNull() || count === null || count <= 0 || count > 10000000) {
        return { error: 'invalid GObjects', chunks: pointerValue(chunks), count };
    }
    const maxObjects = Math.min(count, 4000000);
    for (let index = 0; index < maxObjects; ++index) {
        try {
            const chunk = chunks.add(Math.floor(index / 0x10000) * Process.pointerSize).readPointer();
            if (chunk.isNull()) continue;
            const object = chunk.add((index & 0xffff) * 0x18).readPointer();
            if (object.isNull()) continue;
            const name = decodeName(object.add(0x18).readU32());
            if (!targets.has(name)) continue;
            const objectClass = safeReadPointer(object.add(0x10));
            const className = objectClass.isNull()
                ? null : decodeName(objectClass.add(0x18).readU32());
            if (className !== 'Class') continue;
            const superClass = safeReadPointer(object.add(0x40));
            const defaultObject = safeReadPointer(object.add(0x118));
            const defaultVtable = defaultObject.isNull()
                ? ptr(0) : safeReadPointer(defaultObject);
            const slot708 = defaultVtable.isNull()
                ? ptr(0) : safeReadPointer(defaultVtable.add(0x708));
            const slot710 = defaultVtable.isNull()
                ? ptr(0) : safeReadPointer(defaultVtable.add(0x710));
            records.push({
                index,
                name,
                class_object: pointerValue(object),
                super_class: pointerValue(superClass),
                super_name: superClass.isNull()
                    ? null : decodeName(superClass.add(0x18).readU32()),
                default_object: pointerValue(defaultObject),
                default_vtable: moduleAddress(defaultVtable, image),
                virtual_0x708: moduleAddress(slot708, image),
                virtual_0x710: moduleAddress(slot710, image),
            });
        } catch (_) {}
    }
    return { count, records };
}

function captureWorldsAndNetDrivers(image) {
    const decodeName = makeNameDecoder(image);
    const objectArray = image.base.add(0x5d65fe0);
    const chunks = safeReadPointer(objectArray);
    const count = safeReadS32(objectArray.add(0x14));
    const worlds = [];
    const netDrivers = [];
    if (chunks.isNull() || count === null || count <= 0 || count > 10000000) {
        return { error: 'invalid GObjects', chunks: pointerValue(chunks), count };
    }

    const maxObjects = Math.min(count, 4000000);
    for (let index = 0; index < maxObjects; ++index) {
        try {
            const chunk = chunks.add(Math.floor(index / 0x10000) * Process.pointerSize).readPointer();
            if (chunk.isNull()) continue;
            const object = chunk.add((index & 0xffff) * 0x18).readPointer();
            if (object.isNull()) continue;
            const objectClass = safeReadPointer(object.add(0x10));
            if (objectClass.isNull()) continue;
            const className = decodeName(objectClass.add(0x18).readU32());
            if (className === 'World') {
                worlds.push({
                    index,
                    address: pointerValue(object),
                    identity: captureObjectIdentity(object, image),
                    net_driver: pointerValue(safeReadPointer(object.add(0x38))),
                    authority_game_mode: pointerValue(safeReadPointer(object.add(0x118))),
                    game_state: pointerValue(safeReadPointer(object.add(0x120))),
                    next_url: safeReadFString(object.add(0x5f0)),
                    travel_type: safeReadU8(object.add(0x5e9)),
                });
            } else if (className === 'IpNetDriver') {
                netDrivers.push({
                    index,
                    address: pointerValue(object),
                    identity: captureObjectIdentity(object, image),
                    server_connection: pointerValue(safeReadPointer(object.add(0x88))),
                    client_connections_data: pointerValue(safeReadPointer(object.add(0x90))),
                    client_connections_count: safeReadS32(object.add(0x98)),
                    client_connections_capacity: safeReadS32(object.add(0x9c)),
                    world: pointerValue(safeReadPointer(object.add(0x140))),
                });
            }
        } catch (_) {}
    }
    return { count, worlds, net_drivers: netDrivers };
}

function captureObjectIdentity(object, image) {
    const decodeName = makeNameDecoder(image);
    const records = [];
    let current = object;
    for (let depth = 0; depth < 8 && current && !current.isNull(); ++depth) {
        try {
            const objectClass = safeReadPointer(current.add(0x10));
            records.push({
                depth,
                address: pointerValue(current),
                name: decodeName(current.add(0x18).readU32()),
                name_number: safeReadU32(current.add(0x1c)),
                class_name: objectClass.isNull()
                    ? null : decodeName(objectClass.add(0x18).readU32()),
            });
            current = safeReadPointer(current.add(0x20));
        } catch (_) {
            break;
        }
    }
    return records;
}

try {
    const image = Process.getModuleByName('ProjectBoundarySteam-Win64-Shipping.exe');
    const payload = Process.getModuleByName('Payload.dll');
    const getActiveWorldAddress = payload.getExportByName('PR_GetActiveWorld');
    const getActiveWorld = new NativeFunction(getActiveWorldAddress, 'pointer', []);
    const world = getActiveWorld();
    if (world.isNull()) {
        throw new Error('PR_GetActiveWorld returned null');
    }

    const netDriver = safeReadPointer(world.add(0x38));
    const gameMode = safeReadPointer(world.add(0x118));
    const gameState = safeReadPointer(world.add(0x120));
    const gameModeVtable = gameMode.isNull() ? ptr(0) : safeReadPointer(gameMode);
    const canServerTravel = gameModeVtable.isNull()
        ? ptr(0) : safeReadPointer(gameModeVtable.add(0x708));
    const processServerTravel = gameModeVtable.isNull()
        ? ptr(0) : safeReadPointer(gameModeVtable.add(0x710));

    let driverState = null;
    if (!netDriver.isNull()) {
        const connectionsData = safeReadPointer(netDriver.add(0x90));
        driverState = {
            address: pointerValue(netDriver),
            server_connection: pointerValue(safeReadPointer(netDriver.add(0x88))),
            client_connections_data: pointerValue(connectionsData),
            client_connections_count: safeReadS32(netDriver.add(0x98)),
            client_connections_capacity: safeReadS32(netDriver.add(0x9c)),
            world: pointerValue(safeReadPointer(netDriver.add(0x140))),
        };
    }

    send({
        source: SOURCE,
        event: 'state.capture',
        timestamp_ms: Date.now(),
        image: { base: pointerValue(image.base), size: image.size },
        payload: { base: pointerValue(payload.base), size: payload.size },
        world: pointerValue(world),
        world_identity: captureObjectIdentity(world, image),
        world_net_driver: pointerValue(netDriver),
        authority_game_mode: pointerValue(gameMode),
        game_state: pointerValue(gameState),
        world_fields: {
            next_switch_countdown_0x5dc_float: safeReadFloat(world.add(0x5dc)),
            next_switch_countdown_0x5dc_u32: safeReadU32(world.add(0x5dc)),
            travel_type_0x5e9: safeReadU8(world.add(0x5e9)),
            opaque_pointer_0x5f8: pointerValue(safeReadPointer(world.add(0x5f8))),
            next_url_0x5f0: safeReadFString(world.add(0x5f0)),
            raw_0x5c0_0x620: hexBytes(world.add(0x5c0), 0x60),
        },
        game_mode_fields: {
            vtable: pointerValue(gameModeVtable),
            flags_0x2a8: gameMode.isNull() ? null : safeReadU8(gameMode.add(0x2a8)),
            b_use_seamless_travel: gameMode.isNull() ? null :
                ((safeReadU8(gameMode.add(0x2a8)) || 0) & 1) !== 0,
            virtual_0x708: moduleAddress(canServerTravel, image),
            virtual_0x710: moduleAddress(processServerTravel, image),
        },
        game_mode_classes: captureGameModeClassChain(image),
        object_inventory: captureWorldsAndNetDrivers(image),
        net_driver: driverState,
    });
} catch (error) {
    send({
        source: SOURCE,
        event: 'probe.error',
        timestamp_ms: Date.now(),
        error: String(error),
        stack: error.stack || null,
    });
}

send({ source: SOURCE, event: 'probe.complete', timestamp_ms: Date.now() });
