'use strict';

/*
 * Project Rebound armory/loadout observer.
 *
 * This probe never writes to game-owned memory and never changes a native
 * return value. It observes the MetaTunnel plaintext stream, Unreal
 * ProcessEvent calls, and UPBArmoryManager::Armorys.
 */

const SETTINGS = {
    moduleName: 'ProjectBoundarySteam-Win64-Shipping.exe',
    expectedModuleSize: 105431040,
    expectedImageSha256: '181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843',
    offsets: {
        gObjects: 0x05D65FE0,
        appendString: 0x019D82B0,
        processEvent: 0x01BCBE40,
    },
    object: {
        class: 0x10,
        name: 0x18,
        outer: 0x20,
    },
    armory: {
        ownedItemsData: 0x40,
        ownedItemsNum: 0x48,
        ownedItemsMax: 0x4C,
        newItemCounter: 0x50,
        itemSize: 0x10,
        itemCount: 0x08,
        itemIsNew: 0x0C,
    },
    fieldMod: {
        preOrderingMap: 0x98,
        mapElementSize: 0x30,
    },
    persistentUser: {
        savedArmory: 0x48,
        runtimeArmory: 0x68,
    },
    maxObjects: 2_000_000,
    maxOwnedItems: 250_000,
    maxFrameBytes: 64 * 1024 * 1024,
};

const OBSERVED_FUNCTIONS = new Map([
    ['HasItem', 'PBArmoryManager'],
    ['HandleEnteredArmory', 'PBArmoryManager'],
    ['ClientInitFieldMod', 'PBPlayerState'],
    ['ClientRefreshRoleEquippingInventory', 'PBPlayerState'],
    ['ClientRefreshRolePreOrderingInventory', 'PBPlayerState'],
    ['SelectCharacter', 'PBFieldModManager'],
    ['SelectCharacterSlot', 'PBFieldModManager'],
    ['SelectInventoryItem', 'PBFieldModManager'],
    ['GetEquippingItemIDInSlotType', 'PBFieldModManager'],
    ['GetPreOrderingItemIDInSlotType', 'PBFieldModManager'],
    ['SpawnWeapon', 'PBFieldModManager'],
    ['ConfirmRoleSelection', 'PBPlayerController'],
]);

const textEncoderFallback = (value) => {
    const output = [];
    for (let index = 0; index < value.length; index += 1) {
        const code = value.charCodeAt(index);
        if (code < 0x80) {
            output.push(code);
        } else if (code < 0x800) {
            output.push(0xC0 | (code >> 6), 0x80 | (code & 0x3F));
        } else {
            output.push(0xE0 | (code >> 12), 0x80 | ((code >> 6) & 0x3F), 0x80 | (code & 0x3F));
        }
    }
    return new Uint8Array(output);
};

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-armory-probe',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function reportError(scope, error) {
    emit('probe.error', {
        scope,
        message: String(error && error.stack ? error.stack : error),
    });
}

function toHex(value) {
    return `0x${(value >>> 0).toString(16).padStart(8, '0')}`;
}

function fnvStep(hash, value) {
    return Math.imul((hash ^ (value >>> 0)) >>> 0, 0x01000193) >>> 0;
}

function hashBytes(bytes) {
    let hash = 0x811C9DC5;
    for (let index = 0; index < bytes.length; index += 1) {
        hash = fnvStep(hash, bytes[index]);
    }
    return toHex(hash);
}

function hashString(value) {
    return hashBytes(textEncoderFallback(value));
}

function decodeUtf8(bytes) {
    let output = '';
    for (let index = 0; index < bytes.length;) {
        const first = bytes[index++];
        if (first < 0x80) {
            output += String.fromCharCode(first);
            continue;
        }
        if ((first & 0xE0) === 0xC0 && index < bytes.length) {
            const second = bytes[index++];
            output += String.fromCharCode(((first & 0x1F) << 6) | (second & 0x3F));
            continue;
        }
        if ((first & 0xF0) === 0xE0 && index + 1 < bytes.length) {
            const second = bytes[index++];
            const third = bytes[index++];
            output += String.fromCharCode(
                ((first & 0x0F) << 12) | ((second & 0x3F) << 6) | (third & 0x3F),
            );
            continue;
        }
        output += '\uFFFD';
    }
    return output;
}

function concatBytes(first, second) {
    if (first.length === 0) {
        return second;
    }
    const output = new Uint8Array(first.length + second.length);
    output.set(first, 0);
    output.set(second, first.length);
    return output;
}

function readBytes(address, length) {
    if (length <= 0) {
        return new Uint8Array(0);
    }
    const raw = address.readByteArray(length);
    return raw === null ? new Uint8Array(0) : new Uint8Array(raw);
}

function readVarint(bytes, start) {
    let value = 0;
    let multiplier = 1;
    let index = start;
    for (let count = 0; count < 10 && index < bytes.length; count += 1) {
        const current = bytes[index++];
        value += (current & 0x7F) * multiplier;
        if ((current & 0x80) === 0) {
            return { value, next: index };
        }
        multiplier *= 128;
    }
    throw new Error(`invalid varint at ${start}`);
}

function parseProto(bytes) {
    const fields = new Map();
    let index = 0;
    while (index < bytes.length) {
        const tag = readVarint(bytes, index);
        index = tag.next;
        const field = Math.floor(tag.value / 8);
        const wire = tag.value & 7;
        let value;
        if (wire === 0) {
            const decoded = readVarint(bytes, index);
            value = decoded.value;
            index = decoded.next;
        } else if (wire === 1) {
            if (index + 8 > bytes.length) {
                throw new Error('truncated fixed64');
            }
            value = bytes.slice(index, index + 8);
            index += 8;
        } else if (wire === 2) {
            const decodedLength = readVarint(bytes, index);
            index = decodedLength.next;
            const length = decodedLength.value;
            if (length < 0 || index + length > bytes.length) {
                throw new Error(`truncated bytes field ${field}`);
            }
            value = bytes.slice(index, index + length);
            index += length;
        } else if (wire === 5) {
            if (index + 4 > bytes.length) {
                throw new Error('truncated fixed32');
            }
            value = bytes.slice(index, index + 4);
            index += 4;
        } else {
            throw new Error(`unsupported protobuf wire type ${wire}`);
        }
        const values = fields.get(field) || [];
        values.push({ wire, value });
        fields.set(field, values);
    }
    return fields;
}

function firstVarint(fields, field, fallback = 0) {
    const values = fields.get(field);
    return values && values.length > 0 && values[0].wire === 0 ? values[0].value : fallback;
}

function firstBytes(fields, field) {
    const values = fields.get(field);
    return values && values.length > 0 && values[0].wire === 2 ? values[0].value : new Uint8Array(0);
}

function allBytes(fields, field) {
    const values = fields.get(field) || [];
    return values.filter((entry) => entry.wire === 2).map((entry) => entry.value);
}

function firstString(fields, field) {
    return decodeUtf8(firstBytes(fields, field));
}

function zigZag32(value) {
    const unsigned = value >>> 0;
    return ((unsigned >>> 1) ^ -(unsigned & 1)) | 0;
}

function parseWrapper(bytes, direction) {
    const fields = parseProto(bytes);
    if (direction === 'send') {
        return {
            messageId: firstVarint(fields, 1, -1) | 0,
            rpcPath: firstString(fields, 2),
            errorCode: 0,
            payload: firstBytes(fields, 3),
        };
    }
    return {
        messageId: firstVarint(fields, 1, -1) | 0,
        rpcPath: firstString(fields, 2),
        errorCode: firstVarint(fields, 3, 0) | 0,
        payload: firstBytes(fields, 4),
    };
}

function parseQueryAssets(payload) {
    const fields = parseProto(payload);
    const itemRows = allBytes(fields, 2);
    const unknowns = [new Map(), new Map(), new Map()];
    const ids = new Set();
    const first = [];
    const last = [];
    let emptyIds = 0;
    let duplicateIds = 0;
    let rollingHash = 0x811C9DC5;

    for (let index = 0; index < itemRows.length; index += 1) {
        const itemFields = parseProto(itemRows[index]);
        const itemId = firstString(itemFields, 1);
        const values = [
            zigZag32(firstVarint(itemFields, 2, 0)),
            zigZag32(firstVarint(itemFields, 3, 0)),
            zigZag32(firstVarint(itemFields, 4, 0)),
        ];
        if (itemId.length === 0) {
            emptyIds += 1;
        } else if (ids.has(itemId)) {
            duplicateIds += 1;
        } else {
            ids.add(itemId);
        }
        for (let field = 0; field < 3; field += 1) {
            const map = unknowns[field];
            map.set(values[field], (map.get(values[field]) || 0) + 1);
        }
        const encodedId = textEncoderFallback(itemId);
        for (let byteIndex = 0; byteIndex < encodedId.length; byteIndex += 1) {
            rollingHash = fnvStep(rollingHash, encodedId[byteIndex]);
        }
        if (first.length < 8) {
            first.push({ item_id: itemId, unknowns: values });
        }
        last.push({ item_id: itemId, unknowns: values });
        if (last.length > 8) {
            last.shift();
        }
    }

    const distribution = unknowns.map((map) => {
        const result = {};
        for (const [key, value] of map.entries()) {
            result[String(key)] = value;
        }
        return result;
    });

    return {
        item_count: firstVarint(fields, 1, 0) | 0,
        rows: itemRows.length,
        unique_ids: ids.size,
        empty_ids: emptyIds,
        duplicate_ids: duplicateIds,
        ids_hash: toHex(rollingHash),
        unknown_distributions: distribution,
        first_items: first,
        last_items: last,
    };
}

function parsePlayerArchive(payload) {
    const fields = parseProto(payload);
    const roles = [];
    for (const encodedRole of allBytes(fields, 1)) {
        const role = parseProto(encodedRole);
        const weaponArchive = firstBytes(role, 8);
        const skinToken = firstBytes(role, 9);
        roles.push({
            role_id: firstString(role, 1),
            left_pylon: firstString(role, 2),
            right_pylon: firstString(role, 3),
            mobility_module: firstString(role, 4),
            melee_weapon: firstString(role, 5),
            primary_weapon: firstString(role, 6),
            second_weapon: firstString(role, 7),
            weapon_archive_hex_chars: weaponArchive.length,
            weapon_archive_hash: hashBytes(weaponArchive),
            skin_token_bytes: skinToken.length,
            skin_token_hash: hashBytes(skinToken),
            ornament_hash: hashBytes(firstBytes(role, 10)),
        });
    }
    return {
        player_level: firstVarint(fields, 2, 0) | 0,
        role_count: roles.length,
        roles,
    };
}

function parseRoleArchiveUpdate(payload) {
    const fields = parseProto(payload);
    const skinPayload = firstBytes(fields, 4);
    const skin = skinPayload.length === 0 ? new Map() : parseProto(skinPayload);
    return {
        operation: firstVarint(fields, 1, 0) | 0,
        role_id: firstString(fields, 2),
        item_id: firstString(fields, 3),
        skin_payload_bytes: skinPayload.length,
        skin_payload_hash: hashBytes(skinPayload),
        skin_token_hash: hashBytes(firstBytes(skin, 1)),
        ornament_hash: hashBytes(firstBytes(skin, 2)),
    };
}

function parseWeaponArchiveUpdate(payload) {
    const fields = parseProto(payload);
    const archivePayload = firstBytes(fields, 3);
    const archive = archivePayload.length === 0 ? new Map() : parseProto(archivePayload);
    return {
        role_id: firstString(fields, 1),
        weapon_id: firstString(archive, 1),
        part_count: allBytes(archive, 2).length,
        archive_bytes: archivePayload.length,
        archive_hash: hashBytes(archivePayload),
    };
}

const pendingRpc = new Map();

function shouldCapturePayload(rpcPath) {
    // QueryAssets is a version-pinned public definition set used for the
    // protobuf golden. Player archives and update payloads may contain full
    // customization records or tokens and are summarized only.
    return /QueryAssets/i.test(rpcPath);
}

function captureRpcPayload(socket, direction, wrapper, rpcPath) {
    if (!shouldCapturePayload(rpcPath) || wrapper.payload.length === 0) {
        return;
    }
    send({
        source: 'project-rebound-armory-probe',
        event: 'rpc.payload_capture',
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
        socket,
        direction,
        message_id: wrapper.messageId,
        rpc_path: rpcPath,
        error_code: wrapper.errorCode,
        payload_bytes: wrapper.payload.length,
        payload_hash: hashBytes(wrapper.payload),
    }, wrapper.payload.buffer);
}

function handleFrame(socket, direction, frame) {
    // MetaTunnel uses a two-byte heartbeat that is not a protobuf RPC wrapper.
    if (frame.length === 2) {
        return;
    }
    let wrapper;
    try {
        wrapper = parseWrapper(frame, direction);
    } catch (error) {
        emit('rpc.frame_parse_error', {
            socket,
            direction,
            frame_bytes: frame.length,
            frame_hash: hashBytes(frame),
            message: String(error),
        });
        return;
    }

    if (direction === 'send') {
        if (wrapper.rpcPath.length > 0) {
            pendingRpc.set(wrapper.messageId, wrapper.rpcPath);
            emit('rpc.request', {
                socket,
                message_id: wrapper.messageId,
                rpc_path: wrapper.rpcPath,
                payload_bytes: wrapper.payload.length,
            });
            captureRpcPayload(socket, direction, wrapper, wrapper.rpcPath);
            try {
                if (/UpdateRoleArchiveV2/i.test(wrapper.rpcPath)) {
                    emit('rpc.role_archive_update', Object.assign({
                        socket,
                        message_id: wrapper.messageId,
                        rpc_path: wrapper.rpcPath,
                    }, parseRoleArchiveUpdate(wrapper.payload)));
                } else if (/UpdateWeaponArchiveV2/i.test(wrapper.rpcPath)) {
                    emit('rpc.weapon_archive_update', Object.assign({
                        socket,
                        message_id: wrapper.messageId,
                        rpc_path: wrapper.rpcPath,
                    }, parseWeaponArchiveUpdate(wrapper.payload)));
                }
            } catch (error) {
                emit('rpc.payload_parse_error', {
                    socket,
                    message_id: wrapper.messageId,
                    rpc_path: wrapper.rpcPath,
                    message: String(error),
                });
            }
        }
        return;
    }

    const rpcPath = wrapper.rpcPath || pendingRpc.get(wrapper.messageId) || '';
    pendingRpc.delete(wrapper.messageId);
    if (rpcPath.length === 0) {
        return;
    }

    const response = {
        socket,
        message_id: wrapper.messageId,
        rpc_path: rpcPath,
        error_code: wrapper.errorCode,
        payload_bytes: wrapper.payload.length,
        payload_hash: hashBytes(wrapper.payload),
    };
    emit('rpc.response', response);
    captureRpcPayload(socket, direction, wrapper, rpcPath);

    try {
        if (/QueryAssets/i.test(rpcPath)) {
            emit('rpc.query_assets', Object.assign({}, response, parseQueryAssets(wrapper.payload)));
        } else if (/GetPlayerArchiveV2/i.test(rpcPath)) {
            emit('rpc.player_archive', Object.assign({}, response, parsePlayerArchive(wrapper.payload)));
        }
    } catch (error) {
        emit('rpc.payload_parse_error', Object.assign({}, response, { message: String(error) }));
    }
}

const socketStreams = new Map();

function socketKey(socket) {
    return socket.toString();
}

function markSocket(socket, endpoint) {
    const key = socketKey(socket);
    if (!socketStreams.has(key)) {
        socketStreams.set(key, {
            endpoint,
            send: { bytes: new Uint8Array(0), rejected: false },
            recv: { bytes: new Uint8Array(0), rejected: false },
        });
        emit('network.loopback_socket', { socket: key, endpoint });
    }
}

function parseSockaddr(address, length) {
    if (address.isNull() || length < 8) {
        return null;
    }
    const family = address.readU16();
    const port = (address.add(2).readU8() << 8) | address.add(3).readU8();
    if (family === 2 && length >= 8) {
        const octets = [0, 1, 2, 3].map((offset) => address.add(4 + offset).readU8());
        return {
            loopback: octets[0] === 127,
            text: `${octets.join('.')}:${port}`,
        };
    }
    if (family === 23 && length >= 24) {
        let loopback = true;
        for (let offset = 8; offset < 23; offset += 1) {
            loopback = loopback && address.add(offset).readU8() === 0;
        }
        loopback = loopback && address.add(23).readU8() === 1;
        return { loopback, text: `[IPv6]:${port}` };
    }
    return null;
}

function captureSocketBytes(socket, direction, bytes) {
    const key = socketKey(socket);
    const socketState = socketStreams.get(key);
    if (!socketState || bytes.length === 0) {
        return;
    }
    const stream = socketState[direction];
    if (stream.rejected) {
        return;
    }
    stream.bytes = concatBytes(stream.bytes, bytes);
    if (stream.bytes.length > SETTINGS.maxFrameBytes * 2) {
        stream.rejected = true;
        stream.bytes = new Uint8Array(0);
        emit('network.stream_rejected', { socket: key, direction, reason: 'buffer_limit' });
        return;
    }

    while (stream.bytes.length >= 4) {
        const frameLength = (
            stream.bytes[0] * 0x1000000
            + stream.bytes[1] * 0x10000
            + stream.bytes[2] * 0x100
            + stream.bytes[3]
        );
        if (frameLength <= 0 || frameLength > SETTINGS.maxFrameBytes) {
            stream.rejected = true;
            stream.bytes = new Uint8Array(0);
            emit('network.stream_rejected', {
                socket: key,
                direction,
                reason: 'not_meta_framing',
                declared_frame_bytes: frameLength,
            });
            return;
        }
        if (stream.bytes.length < frameLength + 4) {
            return;
        }
        const frame = stream.bytes.slice(4, frameLength + 4);
        stream.bytes = stream.bytes.slice(frameLength + 4);
        handleFrame(key, direction, frame);
    }
}

function gatherWSABuffers(buffers, count, totalBytes) {
    const chunks = [];
    let remaining = totalBytes;
    for (let index = 0; index < count && remaining > 0; index += 1) {
        const entry = buffers.add(index * 16);
        const capacity = entry.readU32();
        const data = entry.add(8).readPointer();
        const length = Math.min(capacity, remaining);
        if (!data.isNull() && length > 0) {
            chunks.push(readBytes(data, length));
            remaining -= length;
        }
    }
    let output = new Uint8Array(0);
    for (const chunk of chunks) {
        output = concatBytes(output, chunk);
    }
    return output;
}

function hookWinsock() {
    let winsock;
    try {
        winsock = Process.getModuleByName('ws2_32.dll');
    } catch (error) {
        reportError('winsock.module', error);
        return;
    }

    function exported(name) {
        try {
            return winsock.getExportByName(name);
        } catch (_) {
            return null;
        }
    }

    const getPeerNameAddress = exported('getpeername');
    const getPeerName = getPeerNameAddress === null
        ? null
        : new NativeFunction(getPeerNameAddress, 'int', ['pointer', 'pointer', 'pointer'], 'win64');

    function ensureLoopbackSocket(socket) {
        const key = socketKey(socket);
        if (socketStreams.has(key) || getPeerName === null) {
            return;
        }
        const address = Memory.alloc(128);
        const addressLength = Memory.alloc(4);
        addressLength.writeS32(128);
        if (getPeerName(socket, address, addressLength) !== 0) {
            return;
        }
        const endpoint = parseSockaddr(address, addressLength.readS32());
        if (endpoint && endpoint.loopback) {
            markSocket(socket, endpoint.text);
        }
    }

    for (const name of ['connect', 'WSAConnect']) {
        const target = exported(name);
        if (target === null) {
            continue;
        }
        Interceptor.attach(target, {
            onEnter(args) {
                try {
                    const endpoint = parseSockaddr(args[1], args[2].toInt32());
                    if (endpoint && endpoint.loopback) {
                        markSocket(args[0], endpoint.text);
                    }
                } catch (error) {
                    reportError(`winsock.${name}`, error);
                }
            },
        });
    }

    const sendTarget = exported('send');
    if (sendTarget !== null) {
        Interceptor.attach(sendTarget, {
            onEnter(args) {
                this.socket = args[0];
                this.buffer = args[1];
                this.length = args[2].toInt32();
            },
            onLeave(retval) {
                try {
                    const written = retval.toInt32();
                    if (written > 0 && written <= this.length) {
                        ensureLoopbackSocket(this.socket);
                        captureSocketBytes(this.socket, 'send', readBytes(this.buffer, written));
                    }
                } catch (error) {
                    reportError('winsock.send', error);
                }
            },
        });
    }

    const recvTarget = exported('recv');
    if (recvTarget !== null) {
        Interceptor.attach(recvTarget, {
            onEnter(args) {
                this.socket = args[0];
                this.buffer = args[1];
                this.length = args[2].toInt32();
            },
            onLeave(retval) {
                try {
                    const received = retval.toInt32();
                    if (received > 0 && received <= this.length) {
                        ensureLoopbackSocket(this.socket);
                        captureSocketBytes(this.socket, 'recv', readBytes(this.buffer, received));
                    }
                } catch (error) {
                    reportError('winsock.recv', error);
                }
            },
        });
    }

    for (const descriptor of [
        { name: 'WSASend', direction: 'send' },
        { name: 'WSARecv', direction: 'recv' },
    ]) {
        const target = exported(descriptor.name);
        if (target === null) {
            continue;
        }
        Interceptor.attach(target, {
            onEnter(args) {
                this.socket = args[0];
                this.buffers = args[1];
                this.bufferCount = args[2].toInt32();
                this.transferred = args[3];
                this.overlapped = args[5];
            },
            onLeave(retval) {
                try {
                    if (retval.toInt32() === 0 && !this.transferred.isNull()) {
                        const total = this.transferred.readU32();
                        const bytes = gatherWSABuffers(this.buffers, this.bufferCount, total);
                        ensureLoopbackSocket(this.socket);
                        captureSocketBytes(this.socket, descriptor.direction, bytes);
                    } else if (!this.overlapped.isNull()) {
                        emit('network.async_io_notice', {
                            api: descriptor.name,
                            socket: socketKey(this.socket),
                            note: 'overlapped completion is not decoded; recv/send hooks remain active',
                        });
                    }
                } catch (error) {
                    reportError(`winsock.${descriptor.name}`, error);
                }
            },
        });
    }

    const closeTarget = exported('closesocket');
    if (closeTarget !== null) {
        Interceptor.attach(closeTarget, {
            onEnter(args) {
                const key = socketKey(args[0]);
                if (socketStreams.delete(key)) {
                    emit('network.socket_closed', { socket: key });
                }
            },
        });
    }

    emit('network.hooks_ready', {
        module: winsock.name,
        note: 'loopback sockets only; authentication payloads are not logged',
    });
}

let gameModule;
let appendString;
const fnameCache = new Map();
const classNameCache = new Map();
const targetFunctions = new Map();
let armoryManager = null;
let inventoryState = null;
let fieldModManager = null;
const persistentUsers = new Map();
const persistentSignatures = new Map();
const playerLevelTables = new Map();

function fnameKey(address) {
    return `${address.readS32()}:${address.add(4).readU32()}`;
}

function fnameToString(address) {
    const key = fnameKey(address);
    if (fnameCache.has(key)) {
        return fnameCache.get(key);
    }
    const capacity = 256;
    const data = Memory.alloc(capacity * 2);
    const output = Memory.alloc(16);
    output.writePointer(data);
    output.add(8).writeS32(0);
    output.add(12).writeS32(capacity);
    appendString(address, output);
    const value = data.readUtf16String() || '';
    fnameCache.set(key, value);
    return value;
}

function objectName(object) {
    return fnameToString(object.add(SETTINGS.object.name));
}

function className(object) {
    const klass = object.add(SETTINGS.object.class).readPointer();
    if (klass.isNull()) {
        return '';
    }
    const key = klass.toString();
    if (!classNameCache.has(key)) {
        classNameCache.set(key, objectName(klass));
    }
    return classNameCache.get(key);
}

function readArrayHeader(address, maximum, label) {
    const data = address.readPointer();
    const num = address.add(8).readS32();
    const max = address.add(12).readS32();
    if (num < 0 || max < num || max > maximum || (num > 0 && data.isNull())) {
        throw new Error(`invalid ${label} TArray num=${num} max=${max} data=${data}`);
    }
    return { data, num, max };
}

function compactSample(values) {
    return values.length > 12
        ? values.slice(0, 6).concat(values.slice(-6))
        : values;
}

function dumpFNameArray(address, maximum = 128) {
    const header = readArrayHeader(address, maximum, 'FName');
    const values = [];
    let hash = 0x811C9DC5;
    for (let index = 0; index < header.num; index += 1) {
        const item = header.data.add(index * 8);
        const value = fnameToString(item);
        values.push(value);
        hash = fnvStep(hash, hashString(value));
    }
    return { num: header.num, max: header.max, hash: toHex(hash), values };
}

function dumpIntArray(address, maximum = 128) {
    const header = readArrayHeader(address, maximum, 'int32');
    const values = [];
    for (let index = 0; index < header.num; index += 1) {
        values.push(header.data.add(index * 4).readS32());
    }
    return { num: header.num, max: header.max, values };
}

function dumpInventoryConfig(address) {
    const slots = readArrayHeader(address, 32, 'inventory slots');
    const items = readArrayHeader(address.add(0x10), 32, 'inventory items');
    const count = Math.min(slots.num, items.num);
    const entries = [];
    let hash = 0x811C9DC5;
    for (let index = 0; index < count; index += 1) {
        const slot = slots.data.add(index).readU8();
        const itemId = fnameToString(items.data.add(index * 8));
        entries.push({ slot, item_id: itemId });
        hash = fnvStep(hash, slot);
        hash = fnvStep(hash, hashString(itemId));
    }
    return {
        slot_count: slots.num,
        item_count: items.num,
        entries,
        config_hash: toHex(hash),
    };
}

function dumpSavedRoleConfig(address) {
    const roles = readArrayHeader(address, 32, 'saved roles');
    const inventories = readArrayHeader(address.add(0x10), 32, 'saved inventories');
    const count = Math.min(roles.num, inventories.num);
    const result = [];
    for (let index = 0; index < count; index += 1) {
        result.push({
            role_id: fnameToString(roles.data.add(index * 8)),
            inventory: dumpInventoryConfig(inventories.data.add(index * 0x20)),
        });
    }
    return {
        role_count: roles.num,
        inventory_count: inventories.num,
        roles: result,
    };
}

function dumpFieldModState(manager) {
    const map = manager.add(SETTINGS.fieldMod.preOrderingMap);
    const elements = map.readPointer();
    const allocated = map.add(8).readS32();
    const max = map.add(12).readS32();
    const flagBits = map.add(0x28).readS32();
    const secondaryFlags = map.add(0x20).readPointer();
    const flags = secondaryFlags.isNull() ? map.add(0x10) : secondaryFlags;
    if (allocated < 0 || max < allocated || flagBits < allocated ||
        allocated > 128 || (allocated > 0 && elements.isNull())) {
        throw new Error(`invalid FieldMod map allocated=${allocated} max=${max} flags=${flagBits}`);
    }
    const roles = [];
    let hash = 0x811C9DC5;
    for (let index = 0; index < allocated; index += 1) {
        const word = flags.add(Math.floor(index / 32) * 4).readU32();
        if ((word & (1 << (index % 32))) === 0) continue;
        const element = elements.add(index * SETTINGS.fieldMod.mapElementSize);
        const roleId = fnameToString(element);
        const inventory = dumpInventoryConfig(element.add(8));
        roles.push({ role_id: roleId, inventory });
        hash = fnvStep(hash, hashString(roleId));
        hash = fnvStep(hash, parseInt(inventory.config_hash.slice(2), 16));
    }
    return {
        manager: manager.toString(),
        allocated,
        max,
        roles,
        state_hash: toHex(hash),
    };
}

function dumpPlayerLevelTable(table) {
    const map = table.add(0x30);
    const elements = map.readPointer();
    const allocated = map.add(8).readS32();
    const max = map.add(12).readS32();
    const flagBits = map.add(0x28).readS32();
    const secondaryFlags = map.add(0x20).readPointer();
    const flags = secondaryFlags.isNull() ? map.add(0x10) : secondaryFlags;
    if (allocated < 0 || max < allocated || flagBits < allocated ||
        allocated > 1024 || (allocated > 0 && elements.isNull())) {
        throw new Error(`invalid PlayerLevel RowMap allocated=${allocated} max=${max}`);
    }
    const rowNames = [];
    let maximumNumericLevel = null;
    for (let index = 0; index < allocated; index += 1) {
        const word = flags.add(Math.floor(index / 32) * 4).readU32();
        if ((word & (1 << (index % 32))) === 0) continue;
        const rowName = fnameToString(elements.add(index * 0x18));
        rowNames.push(rowName);
        if (/^[0-9]+$/.test(rowName)) {
            const level = parseInt(rowName, 10);
            if (maximumNumericLevel === null || level > maximumNumericLevel) {
                maximumNumericLevel = level;
            }
        }
    }
    const canonicalRows = rowNames.slice().sort();
    let hash = 0x811C9DC5;
    for (const rowName of canonicalRows) {
        hash = fnvStep(hash, hashString(rowName));
    }
    return {
        table: table.toString(),
        row_count: rowNames.length,
        maximum_numeric_level: maximumNumericLevel,
        row_set_hash: toHex(hash),
        row_sample: compactSample(canonicalRows),
    };
}

function emitFieldModSnapshot(reason) {
    if (fieldModManager === null) return;
    try {
        emit('fieldmod.snapshot', Object.assign({ reason }, dumpFieldModState(fieldModManager)));
    } catch (error) {
        reportError('fieldmod.snapshot', error);
    }
}

function persistentArraySummary(address, stride, withCount) {
    const header = readArrayHeader(address, SETTINGS.maxOwnedItems, 'persistent armory');
    const ids = [];
    let hash = 0x811C9DC5;
    let countZero = 0;
    let countPositive = 0;
    for (let index = 0; index < header.num; index += 1) {
        const item = header.data.add(index * stride);
        const id = fnameToString(item);
        ids.push(id);
        hash = fnvStep(hash, item.readS32());
        hash = fnvStep(hash, item.add(4).readU32());
        if (withCount) {
            const count = item.add(8).readS32();
            hash = fnvStep(hash, count);
            if (count === 0) countZero += 1;
            if (count > 0) countPositive += 1;
        }
    }
    return {
        data: header.data.toString(),
        num: header.num,
        max: header.max,
        count_zero: countZero,
        count_positive: countPositive,
        set_hash: toHex(hash),
        item_sample: compactSample(ids),
    };
}

function refreshPersistentUser(object, reason, force = false) {
    try {
        const saved = persistentArraySummary(
            object.add(SETTINGS.persistentUser.savedArmory), 8, false);
        const runtime = persistentArraySummary(
            object.add(SETTINGS.persistentUser.runtimeArmory), 0x10, true);
        const signature = `${saved.num}:${saved.set_hash}:${runtime.num}:${runtime.set_hash}`;
        const key = object.toString();
        if (force || persistentSignatures.get(key) !== signature) {
            persistentSignatures.set(key, signature);
            emit('persistent_user.snapshot', {
                reason,
                object: key,
                object_name: objectName(object),
                saved_armory: saved,
                runtime_armory: runtime,
            });
        }
    } catch (error) {
        reportError('persistent_user.snapshot', error);
    }
}

function inventoryRecordForKey(key) {
    if (inventoryState === null) {
        return null;
    }
    return inventoryState.items.get(key) || null;
}

function refreshInventory(manager, reason, force = false) {
    try {
        const data = manager.add(SETTINGS.armory.ownedItemsData).readPointer();
        const num = manager.add(SETTINGS.armory.ownedItemsNum).readS32();
        const max = manager.add(SETTINGS.armory.ownedItemsMax).readS32();
        const newItemCounter = manager.add(SETTINGS.armory.newItemCounter).readS32();
        if (num < 0 || max < num || max > SETTINGS.maxOwnedItems || (num > 0 && data.isNull())) {
            throw new Error(`invalid OwnedItems TArray num=${num} max=${max} data=${data}`);
        }

        const items = new Map();
        const itemIds = [];
        let countZero = 0;
        let countPositive = 0;
        let countNegative = 0;
        let isNew = 0;
        let hash = 0x811C9DC5;
        for (let index = 0; index < num; index += 1) {
            const item = data.add(index * SETTINGS.armory.itemSize);
            const comparisonIndex = item.readS32();
            const number = item.add(4).readU32();
            const count = item.add(SETTINGS.armory.itemCount).readS32();
            const itemIsNew = item.add(SETTINGS.armory.itemIsNew).readU8() !== 0;
            const key = `${comparisonIndex}:${number}`;
            const itemId = fnameToString(item);
            itemIds.push(itemId);
            items.set(key, { index, item_id: itemId, count, is_new: itemIsNew });
            if (count === 0) {
                countZero += 1;
            } else if (count > 0) {
                countPositive += 1;
            } else {
                countNegative += 1;
            }
            if (itemIsNew) {
                isNew += 1;
            }
            hash = fnvStep(hash, comparisonIndex);
            hash = fnvStep(hash, number);
            hash = fnvStep(hash, count);
            hash = fnvStep(hash, itemIsNew ? 1 : 0);
        }
        hash = fnvStep(hash, newItemCounter);
        const signature = `${num}:${newItemCounter}:${hash}`;
        const changed = inventoryState === null || inventoryState.signature !== signature;
        inventoryState = {
            manager: manager.toString(),
            data: data.toString(),
            num,
            max,
            newItemCounter,
            items,
            signature,
            refreshedAt: Date.now(),
        };
        if (force || changed) {
            emit(force ? 'armory.snapshot' : 'armory.changed', {
                reason,
                manager: manager.toString(),
                data: data.toString(),
                owned_items_num: num,
                owned_items_max: max,
                new_item_counter: newItemCounter,
                unique_fname_keys: items.size,
                count_zero: countZero,
                count_positive: countPositive,
                count_negative: countNegative,
                is_new_true: isNew,
                signature_hash: toHex(hash),
                owned_item_ids: itemIds,
            });
        }
        return inventoryState;
    } catch (error) {
        reportError('armory.refresh', error);
        return null;
    }
}

function considerObject(object) {
    const typeName = className(object);
    if (typeName === 'PBArmoryManager') {
        const name = objectName(object);
        if (!name.startsWith('Default__')) {
            const changed = armoryManager === null || !armoryManager.equals(object);
            if (changed) {
                armoryManager = object;
                inventoryState = null;
                emit('armory.manager_found', { manager: object.toString(), object_name: name });
            }
            if (inventoryState === null) {
                refreshInventory(object, changed ? 'manager_found' : 'manager_rescan', true);
            }
        }
        return;
    }
    if (typeName === 'PBFieldModManager') {
        const name = objectName(object);
        if (!name.startsWith('Default__')) {
            const changed = fieldModManager === null || !fieldModManager.equals(object);
            fieldModManager = object;
            if (changed) {
                emit('fieldmod.manager_found', {
                    manager: object.toString(),
                    object_name: name,
                });
                emitFieldModSnapshot('manager_found');
            }
        }
        return;
    }
    if (typeName.includes('PersistentUser')) {
        const name = objectName(object);
        if (!name.startsWith('Default__')) {
            const key = object.toString();
            persistentUsers.set(key, object);
            refreshPersistentUser(object, 'object_scan', !persistentSignatures.has(key));
        }
        return;
    }
    if ((typeName === 'DataTable' || typeName === 'CompositeDataTable') &&
        objectName(object).toLowerCase().includes('playerlevelexp')) {
        const key = object.toString();
        if (!playerLevelTables.has(key)) {
            playerLevelTables.set(key, object);
            emit('progression.player_level_table', Object.assign({
                object_name: objectName(object),
                class_name: typeName,
            }, dumpPlayerLevelTable(object)));
        }
        return;
    }
    if (typeName !== 'Function') {
        return;
    }
    const name = objectName(object);
    if (!OBSERVED_FUNCTIONS.has(name)) {
        return;
    }
    const outer = object.add(SETTINGS.object.outer).readPointer();
    const ownerName = outer.isNull() ? '' : objectName(outer);
    if (ownerName === OBSERVED_FUNCTIONS.get(name)) {
        if (targetFunctions.has(object.toString())) {
            return;
        }
        targetFunctions.set(object.toString(), name);
        let execFunction = '0x0';
        try {
            execFunction = object.add(0xD8).readPointer().toString();
        } catch (_) {
            // The ProcessEvent hook does not require ExecFunction.
        }
        emit('unreal.function_found', {
            function: object.toString(),
            function_name: name,
            owner_name: ownerName,
            exec_function: execFunction,
        });
    }
}

function scanObjects() {
    try {
        const objectArray = gameModule.base.add(SETTINGS.offsets.gObjects);
        const chunks = objectArray.readPointer();
        const numElements = objectArray.add(0x14).readS32();
        const numChunks = objectArray.add(0x1C).readS32();
        if (chunks.isNull() || numElements <= 0 || numElements > SETTINGS.maxObjects || numChunks <= 0) {
            throw new Error(`GObjects not ready num=${numElements} chunks=${numChunks}`);
        }
        for (let index = 0; index < numElements; index += 1) {
            const chunkIndex = Math.floor(index / 0x10000);
            const inChunkIndex = index % 0x10000;
            if (chunkIndex >= numChunks) {
                break;
            }
            const chunk = chunks.add(chunkIndex * Process.pointerSize).readPointer();
            if (chunk.isNull()) {
                continue;
            }
            const object = chunk.add(inChunkIndex * 0x18).readPointer();
            if (object.isNull()) {
                continue;
            }
            try {
                considerObject(object);
            } catch (_) {
                // Object slots can change while the array is scanned.
            }
        }
        emit('unreal.object_scan', {
            num_elements: numElements,
            num_chunks: numChunks,
            target_functions: targetFunctions.size,
            armory_manager_found: armoryManager !== null,
            fieldmod_manager_found: fieldModManager !== null,
            persistent_users_found: persistentUsers.size,
            player_level_tables_found: playerLevelTables.size,
        });
        if (armoryManager !== null && inventoryState === null) {
            refreshInventory(armoryManager, 'object_scan_recovery', true);
        }
    } catch (error) {
        reportError('unreal.object_scan', error);
    }
}

function observedCallDetails(functionName, params, phase) {
    if (params.isNull()) return {};
    if (functionName === 'ClientInitFieldMod' && phase === 'enter') {
        return {
            server_equipping_saved: dumpSavedRoleConfig(params),
            role_ids: dumpFNameArray(params.add(0x20), 32),
            owned_quotas: dumpIntArray(params.add(0x30), 32),
        };
    }
    if ((functionName === 'ClientRefreshRoleEquippingInventory' ||
         functionName === 'ClientRefreshRolePreOrderingInventory') && phase === 'enter') {
        return {
            role_id: fnameToString(params),
            inventory: dumpInventoryConfig(params.add(8)),
        };
    }
    if (functionName === 'SelectCharacter' ||
        functionName === 'SelectInventoryItem' ||
        functionName === 'ConfirmRoleSelection') {
        return { item_or_role_id: fnameToString(params) };
    }
    if (functionName === 'SelectCharacterSlot') {
        return { slot: params.readU8() };
    }
    if (functionName === 'GetEquippingItemIDInSlotType' ||
        functionName === 'GetPreOrderingItemIDInSlotType') {
        const details = { slot: params.readU8() };
        if (phase === 'leave') details.return_item_id = fnameToString(params.add(4));
        return details;
    }
    if (functionName === 'SpawnWeapon') {
        const details = {
            role_id: fnameToString(params),
            weapon_id: fnameToString(params.add(8)),
        };
        if (phase === 'leave') details.return_weapon = params.add(0x10).readPointer().toString();
        return details;
    }
    return {};
}

function hookProcessEvent() {
    const address = gameModule.base.add(SETTINGS.offsets.processEvent);
    Interceptor.attach(address, {
        onEnter(args) {
            this.probeKind = null;
            this.probeParams = ptr(0);
            this.probeObject = ptr(0);
            const functionName = targetFunctions.get(args[1].toString());
            if (functionName === undefined) {
                return;
            }
            try {
                if (functionName === 'HandleEnteredArmory') {
                    armoryManager = args[0];
                    this.probeKind = functionName;
                    refreshInventory(armoryManager, 'HandleEnteredArmory.before', true);
                    emit('unreal.lifecycle', {
                        function_name: functionName,
                        phase: 'enter',
                        object: args[0].toString(),
                    });
                    return;
                }
                if (functionName === 'HasItem' && !args[2].isNull()) {
                    armoryManager = args[0];
                    if (
                        inventoryState === null
                        || inventoryState.manager !== armoryManager.toString()
                        || Date.now() - inventoryState.refreshedAt > 1000
                    ) {
                        refreshInventory(armoryManager, 'HasItem.refresh', false);
                    }
                    const key = fnameKey(args[2]);
                    const name = fnameToString(args[2]);
                    const record = inventoryRecordForKey(key);
                    this.probeKind = functionName;
                    this.probeParams = args[2];
                    this.probeItem = {
                        item_id: name,
                        fname_key: key,
                        present: record !== null,
                        index: record === null ? -1 : record.index,
                        count: record === null ? null : record.count,
                        is_new: record === null ? null : record.is_new,
                    };
                    return;
                }

                this.probeKind = functionName;
                this.probeParams = args[2];
                this.probeObject = args[0];
                if (OBSERVED_FUNCTIONS.get(functionName) === 'PBFieldModManager') {
                    fieldModManager = args[0];
                }
                emitFieldModSnapshot(`${functionName}.before`);
                emit('fieldmod.native_call', Object.assign({
                    function_name: functionName,
                    phase: 'enter',
                    object: args[0].toString(),
                }, observedCallDetails(functionName, args[2], 'enter')));
            } catch (error) {
                reportError(`unreal.${functionName}.enter`, error);
            }
        },
        onLeave() {
            if (this.probeKind === null) {
                return;
            }
            try {
                if (this.probeKind === 'HandleEnteredArmory') {
                    refreshInventory(armoryManager, 'HandleEnteredArmory.after', true);
                    emitFieldModSnapshot('HandleEnteredArmory.after');
                    for (const user of persistentUsers.values()) {
                        refreshPersistentUser(user, 'HandleEnteredArmory.after', true);
                    }
                    emit('unreal.lifecycle', {
                        function_name: this.probeKind,
                        phase: 'leave',
                        object: armoryManager.toString(),
                    });
                } else if (this.probeKind === 'HasItem') {
                    emit('armory.has_item', Object.assign({}, this.probeItem, {
                        return_value: this.probeParams.add(8).readU8() !== 0,
                    }));
                } else {
                    emit('fieldmod.native_call', Object.assign({
                        function_name: this.probeKind,
                        phase: 'leave',
                        object: this.probeObject.toString(),
                    }, observedCallDetails(this.probeKind, this.probeParams, 'leave')));
                    emitFieldModSnapshot(`${this.probeKind}.after`);
                    for (const user of persistentUsers.values()) {
                        refreshPersistentUser(user, `${this.probeKind}.after`, false);
                    }
                }
            } catch (error) {
                reportError(`unreal.${this.probeKind}.leave`, error);
            }
        },
    });
    emit('unreal.process_event_hook_ready', { address: address.toString() });
}

function initialize() {
    try {
        gameModule = Process.getModuleByName(SETTINGS.moduleName);
        if (gameModule.size !== SETTINGS.expectedModuleSize) {
            throw new Error(
                `unsupported Boundary image size=${gameModule.size} ` +
                `expected=${SETTINGS.expectedModuleSize}`);
        }
        appendString = new NativeFunction(
            gameModule.base.add(SETTINGS.offsets.appendString),
            'void',
            ['pointer', 'pointer'],
            'win64',
        );
        emit('probe.ready', {
            pid: Process.id,
            architecture: Process.arch,
            module: gameModule.name,
            module_base: gameModule.base.toString(),
            module_size: gameModule.size,
            expected_image_sha256: SETTINGS.expectedImageSha256,
            offsets: SETTINGS.offsets,
            mode: 'read_only',
        });
        hookWinsock();
        hookProcessEvent();
        setTimeout(scanObjects, 1000);
        setInterval(() => {
            if (targetFunctions.size < OBSERVED_FUNCTIONS.size ||
                armoryManager === null || fieldModManager === null ||
                persistentUsers.size === 0 || playerLevelTables.size === 0) {
                scanObjects();
            }
            if (armoryManager !== null) {
                refreshInventory(armoryManager, 'poll', false);
            }
            for (const user of persistentUsers.values()) {
                refreshPersistentUser(user, 'poll', false);
            }
        }, 2000);
    } catch (error) {
        reportError('initialize', error);
    }
}

setImmediate(initialize);
