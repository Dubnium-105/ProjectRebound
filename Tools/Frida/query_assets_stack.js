'use strict';

/*
 * Read-only QueryAssets transport stack probe.
 *
 * Captures the native callers that send the QueryAssets RPC and receive its
 * current protobuf response. The probe never changes the socket buffers.
 */

const QUERY_PATH = '/assets.Assets/QueryAssets';
const QUERY_ASSETS_PREFIX = [0x08, 0x8e, 0xbc, 0x02, 0x12];

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-query-assets-stack',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function stack(context) {
    return Thread.backtrace(context, Backtracer.ACCURATE)
        .map(address => DebugSymbol.fromAddress(address).toString());
}

function containsAscii(bytes, text) {
    const expected = Array.from(text, character => character.charCodeAt(0));
    for (let index = 0; index + expected.length <= bytes.length; index += 1) {
        let matches = true;
        for (let offset = 0; offset < expected.length; offset += 1) {
            if (bytes[index + offset] !== expected[offset]) {
                matches = false;
                break;
            }
        }
        if (matches) return true;
    }
    return false;
}

function containsPrefix(bytes) {
    for (let index = 0; index + QUERY_ASSETS_PREFIX.length <= bytes.length; index += 1) {
        let matches = true;
        for (let offset = 0; offset < QUERY_ASSETS_PREFIX.length; offset += 1) {
            if (bytes[index + offset] !== QUERY_ASSETS_PREFIX[offset]) {
                matches = false;
                break;
            }
        }
        if (matches) return true;
    }
    return false;
}

function initialize() {
    const winsock = Process.getModuleByName('ws2_32.dll');
    const sendAddress = winsock.getExportByName('send');
    const recvAddress = winsock.getExportByName('recv');

    Interceptor.attach(sendAddress, {
        onEnter(args) {
            const length = args[2].toInt32();
            if (length <= 0 || length > 16 * 1024 * 1024) return;
            const raw = args[1].readByteArray(length);
            if (raw === null) return;
            if (containsAscii(new Uint8Array(raw), QUERY_PATH)) {
                emit('query_assets.send', {
                    bytes: length,
                    backtrace: stack(this.context),
                });
            }
        },
    });

    Interceptor.attach(recvAddress, {
        onEnter(args) {
            this.buffer = args[1];
            this.capacity = args[2].toInt32();
        },
        onLeave(retval) {
            const length = retval.toInt32();
            if (length <= 0 || length > this.capacity) return;
            const raw = this.buffer.readByteArray(length);
            if (raw === null || !containsPrefix(new Uint8Array(raw))) return;
            emit('query_assets.recv', {
                bytes: length,
                backtrace: stack(this.context),
            });
        },
    });

    emit('probe.ready', { pid: Process.id, mode: 'read_only' });
}

setImmediate(initialize);
