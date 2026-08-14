'use strict';

/*
 * Controlled local A/B for QueryAssets field 1.
 *
 * The current response starts with:
 *   08 8e bc 02 12 ...  (field 1 = 40462, followed by item data field 2)
 *
 * This probe changes only the three-byte varint value to a length-preserving,
 * non-canonical encoding of zero:
 *   08 80 80 00 12 ...  (field 1 = 0)
 *
 * No frame sizes move and no request or server-side state is changed.
 */

const MODULE = 'ws2_32.dll';
const QUERY_ASSETS_PREFIX = '08 8e bc 02 12';
const ZERO_SAME_WIDTH = [0x80, 0x80, 0x00];

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-query-assets-status-ab',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function initialize() {
    const winsock = Process.getModuleByName(MODULE);
    const recv = winsock.getExportByName('recv');
    let rewrites = 0;
    Interceptor.attach(recv, {
        onEnter(args) {
            this.buffer = args[1];
            this.capacity = args[2].toInt32();
        },
        onLeave(retval) {
            const received = retval.toInt32();
            if (received < 5 || received > this.capacity) {
                return;
            }
            const matches = Memory.scanSync(this.buffer, received, QUERY_ASSETS_PREFIX);
            for (const match of matches) {
                match.address.add(1).writeByteArray(ZERO_SAME_WIDTH);
                rewrites += 1;
                emit('query_assets.status_rewritten', {
                    address: match.address.toString(),
                    received_bytes: received,
                    from: 40462,
                    to: 0,
                    rewrite_count: rewrites,
                });
            }
        },
    });
    emit('probe.ready', {
        pid: Process.id,
        mode: 'local_query_assets_field1_ab',
        target_prefix: QUERY_ASSETS_PREFIX,
    });
}

setImmediate(initialize);
