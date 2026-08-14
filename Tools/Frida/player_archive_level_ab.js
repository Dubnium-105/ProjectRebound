'use strict';

/*
 * Controlled GetPlayerArchiveV2 player-level A/B.
 *
 * The deployed response currently ends with top-level field 2 = 1. This probe
 * rewrites only that one-byte value to zero, preserving the payload and all
 * outer frame lengths. Zero matches the archived native server behavior.
 */

const ARCHIVE_PREFIX = [0x0a, 0xa7, 0x01, 0x0a, 0x05, 0x50, 0x45, 0x41, 0x43, 0x45];
const ARCHIVE_PAYLOAD_BYTES = 1109;

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-player-archive-level-ab',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function matchesPrefix(bytes, start) {
    if (start + ARCHIVE_PREFIX.length > bytes.length) {
        return false;
    }
    for (let offset = 0; offset < ARCHIVE_PREFIX.length; offset += 1) {
        if (bytes[start + offset] !== ARCHIVE_PREFIX[offset]) {
            return false;
        }
    }
    return true;
}

function initialize() {
    const winsock = Process.getModuleByName('ws2_32.dll');
    const recv = winsock.getExportByName('recv');
    let payloadBytesRemaining = 0;
    let responsesRewritten = 0;

    Interceptor.attach(recv, {
        onEnter(args) {
            this.buffer = args[1];
            this.capacity = args[2].toInt32();
        },
        onLeave(retval) {
            const received = retval.toInt32();
            if (received < 2 || received > this.capacity) {
                return;
            }
            const raw = this.buffer.readByteArray(received);
            if (raw === null) {
                return;
            }
            const bytes = new Uint8Array(raw);
            let payloadStart = payloadBytesRemaining > 0 ? 0 : -1;
            if (payloadStart < 0) {
                for (let index = 0; index < bytes.length; index += 1) {
                    if (!matchesPrefix(bytes, index)) {
                        continue;
                    }
                    payloadStart = index;
                    payloadBytesRemaining = ARCHIVE_PAYLOAD_BYTES;
                    break;
                }
            }
            if (payloadStart < 0) {
                return;
            }

            const payloadBytesInChunk = Math.min(
                bytes.length - payloadStart,
                payloadBytesRemaining,
            );
            if (payloadBytesRemaining <= payloadBytesInChunk) {
                const levelTag = payloadStart + payloadBytesRemaining - 2;
                if (bytes[levelTag] === 0x10 && bytes[levelTag + 1] === 0x01) {
                    this.buffer.add(levelTag + 1).writeU8(0x00);
                    responsesRewritten += 1;
                    emit('player_archive.level_rewritten', {
                        response_number: responsesRewritten,
                        received_bytes: received,
                        payload_bytes: ARCHIVE_PAYLOAD_BYTES,
                        old_level: 1,
                        new_level: 0,
                    });
                } else {
                    emit('player_archive.level_not_found', {
                        received_bytes: received,
                        expected_tag_offset: levelTag,
                        observed_tag: bytes[levelTag],
                        observed_value: bytes[levelTag + 1],
                    });
                }
            }
            payloadBytesRemaining -= payloadBytesInChunk;
        },
    });

    emit('probe.ready', {
        pid: Process.id,
        mode: 'local_player_archive_level_ab',
    });
}

setImmediate(initialize);
