'use strict';

/*
 * Controlled GetPlayerArchiveV2 player-level A/B.
 *
 * This probe understands the native four-byte big-endian frame and the
 * ResponseWrapper protobuf. It rewrites only top-level field 2 in the nested
 * GetPlayerArchiveV2Response. Both levels must fit in one protobuf byte so no
 * frame length changes are required.
 *
 * Set this to the low or high arm of the experiment before attaching. The
 * server default is configured separately through META_NATIVE_PLAYER_LEVEL.
 */
const TARGET_PLAYER_LEVEL = Number.isInteger(
    globalThis.__PROJECT_REBOUND_TARGET_PLAYER_LEVEL__)
    ? globalThis.__PROJECT_REBOUND_TARGET_PLAYER_LEVEL__
    : 1;
const EXPECTED_MODULE_NAME = 'ProjectBoundarySteam-Win64-Shipping.exe';
const EXPECTED_MODULE_SIZE = 105431040;
const EXPECTED_IMAGE_SHA256 =
    '181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843';
const ARCHIVE_RPC_PATH = '/assets.Assets/GetPlayerArchiveV2';

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-player-archive-level-ab',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function readU32BE(bytes, offset) {
    return ((bytes[offset] << 24) >>> 0) |
        (bytes[offset + 1] << 16) |
        (bytes[offset + 2] << 8) |
        bytes[offset + 3];
}

function readVarint(bytes, offset, limit) {
    let value = 0;
    let multiplier = 1;
    for (let index = offset; index < limit && index < offset + 10; index += 1) {
        const current = bytes[index];
        value += (current & 0x7f) * multiplier;
        if ((current & 0x80) === 0) {
            return { value, start: offset, end: index + 1 };
        }
        multiplier *= 128;
    }
    return null;
}

function parseFields(bytes, start, limit) {
    const fields = [];
    let cursor = start;
    while (cursor < limit) {
        const tag = readVarint(bytes, cursor, limit);
        if (tag === null || tag.value === 0) {
            return null;
        }
        cursor = tag.end;
        const number = Math.floor(tag.value / 8);
        const wireType = tag.value & 7;
        const field = { number, wireType, tagStart: tag.start };
        if (wireType === 0) {
            const scalar = readVarint(bytes, cursor, limit);
            if (scalar === null) return null;
            field.value = scalar.value;
            field.valueStart = scalar.start;
            field.valueEnd = scalar.end;
            cursor = scalar.end;
        } else if (wireType === 1) {
            if (cursor + 8 > limit) return null;
            field.valueStart = cursor;
            field.valueEnd = cursor + 8;
            cursor += 8;
        } else if (wireType === 2) {
            const length = readVarint(bytes, cursor, limit);
            if (length === null) return null;
            cursor = length.end;
            if (length.value < 0 || cursor + length.value > limit) return null;
            field.valueStart = cursor;
            field.valueEnd = cursor + length.value;
            cursor = field.valueEnd;
        } else if (wireType === 5) {
            if (cursor + 4 > limit) return null;
            field.valueStart = cursor;
            field.valueEnd = cursor + 4;
            cursor += 4;
        } else {
            return null;
        }
        fields.push(field);
    }
    return fields;
}

function decodeUtf8(bytes, start, end) {
    const raw = bytes.slice(start, end);
    let encoded = '';
    for (let index = 0; index < raw.length; index += 1) {
        encoded += String.fromCharCode(raw[index]);
    }
    try {
        return decodeURIComponent(escape(encoded));
    } catch (_) {
        return encoded;
    }
}

function rewriteArchiveLevel(bytes, wrapperStart, wrapperEnd, memoryBase) {
    const wrapper = parseFields(bytes, wrapperStart, wrapperEnd);
    if (wrapper === null) return false;

    const pathField = wrapper.find(
        field => field.number === 2 && field.wireType === 2,
    );
    const messageField = wrapper.find(
        field => field.number === 4 && field.wireType === 2,
    );
    if (pathField === undefined || messageField === undefined) return false;
    const rpcPath = decodeUtf8(bytes, pathField.valueStart, pathField.valueEnd);
    if (rpcPath !== ARCHIVE_RPC_PATH) return false;

    const archive = parseFields(
        bytes, messageField.valueStart, messageField.valueEnd,
    );
    if (archive === null) {
        emit('player_archive.invalid_payload', { rpc_path: rpcPath });
        return false;
    }
    const levelField = archive.find(
        field => field.number === 2 && field.wireType === 0,
    );
    if (levelField === undefined) {
        emit('player_archive.level_not_found', { rpc_path: rpcPath });
        return false;
    }
    if (levelField.valueEnd - levelField.valueStart !== 1) {
        emit('player_archive.level_width_mismatch', {
            rpc_path: rpcPath,
            current_level: levelField.value,
            encoded_bytes: levelField.valueEnd - levelField.valueStart,
            target_level: TARGET_PLAYER_LEVEL,
        });
        return false;
    }

    memoryBase.add(levelField.valueStart).writeU8(TARGET_PLAYER_LEVEL);
    emit('player_archive.level_rewritten', {
        rpc_path: rpcPath,
        old_level: levelField.value,
        new_level: TARGET_PLAYER_LEVEL,
        payload_bytes: messageField.valueEnd - messageField.valueStart,
    });
    return true;
}

function initialize() {
    if (!Number.isInteger(TARGET_PLAYER_LEVEL) ||
        TARGET_PLAYER_LEVEL < 0 || TARGET_PLAYER_LEVEL > 127) {
        throw new Error('TARGET_PLAYER_LEVEL must be an integer in 0..127');
    }
    const mainModule = Process.mainModule;
    if (mainModule.name.toLowerCase() !== EXPECTED_MODULE_NAME.toLowerCase() ||
        mainModule.size !== EXPECTED_MODULE_SIZE) {
        emit('probe.target_mismatch', {
            expected_module: EXPECTED_MODULE_NAME,
            observed_module: mainModule.name,
            expected_module_size: EXPECTED_MODULE_SIZE,
            observed_module_size: mainModule.size,
            expected_image_sha256: EXPECTED_IMAGE_SHA256,
        });
        throw new Error('unsupported ProjectBoundary executable');
    }

    const winsock = Process.getModuleByName('ws2_32.dll');
    const recv = winsock.getExportByName('recv');
    let responsesRewritten = 0;
    Interceptor.attach(recv, {
        onEnter(args) {
            this.buffer = args[1];
            this.capacity = args[2].toInt32();
        },
        onLeave(retval) {
            const received = retval.toInt32();
            if (received < 4 || received > this.capacity) return;
            const raw = this.buffer.readByteArray(received);
            if (raw === null) return;
            const bytes = new Uint8Array(raw);

            let cursor = 0;
            while (cursor + 4 <= bytes.length) {
                const frameLength = readU32BE(bytes, cursor);
                const frameStart = cursor + 4;
                const frameEnd = frameStart + frameLength;
                if (frameLength === 0 || frameEnd > bytes.length) {
                    emit('network.fragment_skipped', {
                        received_bytes: received,
                        frame_offset: cursor,
                        declared_frame_bytes: frameLength,
                    });
                    break;
                }
                if (rewriteArchiveLevel(
                    bytes, frameStart, frameEnd, this.buffer,
                )) {
                    responsesRewritten += 1;
                }
                cursor = frameEnd;
            }
        },
    });

    emit('probe.ready', {
        pid: Process.id,
        mode: 'local_player_archive_level_ab',
        target_player_level: TARGET_PLAYER_LEVEL,
        module_name: mainModule.name,
        module_size: mainModule.size,
        expected_image_sha256: EXPECTED_IMAGE_SHA256,
    });
}

setImmediate(initialize);
