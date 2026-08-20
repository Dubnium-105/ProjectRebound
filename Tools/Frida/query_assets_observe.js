'use strict';

/*
 * Read-only QueryAssets response observer.
 *
 * The current Meta response starts with a stable protobuf prefix after its
 * RPC framing. This probe reports each occurrence without modifying recv()
 * buffers, so it is suitable for an unmodified baseline capture.
 */

const QUERY_ASSETS_PREFIX = [0x08, 0x8e, 0xbc, 0x02, 0x12];

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-query-assets-observe',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function prefixMatches(bytes, start) {
    if (start + QUERY_ASSETS_PREFIX.length > bytes.length) {
        return false;
    }
    for (let offset = 0; offset < QUERY_ASSETS_PREFIX.length; offset += 1) {
        if (bytes[start + offset] !== QUERY_ASSETS_PREFIX[offset]) {
            return false;
        }
    }
    return true;
}

function hex(bytes, start, length) {
    const end = Math.min(bytes.length, start + length);
    const output = [];
    for (let index = start; index < end; index += 1) {
        output.push(bytes[index].toString(16).padStart(2, '0'));
    }
    return output.join(' ');
}

function initialize() {
    const winsock = Process.getModuleByName('ws2_32.dll');
    const recv = winsock.getExportByName('recv');
    let responsesObserved = 0;

    Interceptor.attach(recv, {
        onEnter(args) {
            this.buffer = args[1];
            this.capacity = args[2].toInt32();
        },
        onLeave(retval) {
            const received = retval.toInt32();
            if (received < QUERY_ASSETS_PREFIX.length || received > this.capacity) {
                return;
            }
            const raw = this.buffer.readByteArray(received);
            if (raw === null) {
                return;
            }
            const bytes = new Uint8Array(raw);
            for (let index = 0; index < bytes.length; index += 1) {
                if (!prefixMatches(bytes, index)) {
                    continue;
                }
                responsesObserved += 1;
                emit('query_assets.observed', {
                    response_number: responsesObserved,
                    received_bytes: received,
                    prefix_offset: index,
                    first_bytes: hex(bytes, index, 24),
                });
            }
        },
    });

    emit('probe.ready', {
        pid: Process.id,
        mode: 'read_only_query_assets_observer',
    });
}

setImmediate(initialize);
