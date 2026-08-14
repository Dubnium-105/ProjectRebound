'use strict';

/*
 * Second controlled QueryAssets A/B.
 *
 * In addition to rewriting top-level field 1 from 40462 to one, this changes
 * the outer tag of every complete ItemData row except PEACE_RU-AKM from field
 * 2 (0x12) to unknown field 3 (0x1a). The bytes and frame sizes stay fixed;
 * the native protobuf decoder therefore sees one known inventory item.
 */

const TARGET_ITEM = 'PEACE_RU-AKM';
const QUERY_ASSETS_PREFIX = [0x08, 0x8e, 0xbc, 0x02, 0x12];
const QUERY_ASSETS_PAYLOAD_BYTES = 1615627;
// Equal-width, non-canonical varint encoding of 1. Keeping the original three
// value bytes means recv() and the outer RPC frame lengths remain untouched.
const ONE_SAME_WIDTH = [0x81, 0x80, 0x00];

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-query-assets-single-item-ab',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function readVarint(bytes, start) {
    let value = 0;
    let multiplier = 1;
    let index = start;
    for (let count = 0; count < 5 && index < bytes.length; count += 1) {
        const current = bytes[index++];
        value += (current & 0x7f) * multiplier;
        if ((current & 0x80) === 0) {
            return { value, next: index };
        }
        multiplier *= 128;
    }
    return null;
}

function ascii(bytes, start, length) {
    let output = '';
    for (let index = 0; index < length; index += 1) {
        const value = bytes[start + index];
        if (value < 0x20 || value > 0x7e) {
            return '';
        }
        output += String.fromCharCode(value);
    }
    return output;
}

function initialize() {
    const winsock = Process.getModuleByName('ws2_32.dll');
    const recv = winsock.getExportByName('recv');
    let statusRewrites = 0;
    let rowsHidden = 0;
    let targetRowsKept = 0;
    let payloadBytesRemaining = 0;

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
            const raw = this.buffer.readByteArray(received);
            if (raw === null) {
                return;
            }
            const bytes = new Uint8Array(raw);
            let chunkHidden = 0;
            let chunkKept = 0;
            let filterStart = payloadBytesRemaining > 0 ? 0 : -1;

            if (filterStart < 0) {
                for (let index = 0; index + QUERY_ASSETS_PREFIX.length <= bytes.length; index += 1) {
                    let matches = true;
                    for (let offset = 0; offset < QUERY_ASSETS_PREFIX.length; offset += 1) {
                        matches = matches && bytes[index + offset] === QUERY_ASSETS_PREFIX[offset];
                    }
                    if (matches) {
                        this.buffer.add(index + 1).writeByteArray(ONE_SAME_WIDTH);
                        statusRewrites += 1;
                        payloadBytesRemaining = QUERY_ASSETS_PAYLOAD_BYTES;
                        filterStart = index;
                        break;
                    }
                }
            }

            if (filterStart < 0) {
                return;
            }
            const filterBytes = Math.min(bytes.length - filterStart, payloadBytesRemaining);
            const filterEnd = filterStart + filterBytes;
            for (let index = filterStart; index + 4 < filterEnd; index += 1) {
                if (bytes[index] !== 0x12) {
                    continue;
                }
                const rowLength = readVarint(bytes, index + 1);
                if (rowLength === null || rowLength.value < 3) {
                    continue;
                }
                const rowStart = rowLength.next;
                const rowEnd = rowStart + rowLength.value;
                if (rowEnd > filterEnd || bytes[rowStart] !== 0x0a) {
                    continue;
                }
                const idLength = readVarint(bytes, rowStart + 1);
                if (idLength === null || idLength.value === 0 || idLength.next + idLength.value > rowEnd) {
                    continue;
                }
                const itemId = ascii(bytes, idLength.next, idLength.value);
                if (itemId === '') {
                    continue;
                }
                if (itemId === TARGET_ITEM) {
                    targetRowsKept += 1;
                    chunkKept += 1;
                } else {
                    this.buffer.add(index).writeU8(0x1a);
                    rowsHidden += 1;
                    chunkHidden += 1;
                }
                index = rowEnd - 1;
            }
            payloadBytesRemaining -= filterBytes;

            if (chunkHidden > 0 || chunkKept > 0) {
                emit('query_assets.rows_filtered', {
                    received_bytes: received,
                    chunk_rows_hidden: chunkHidden,
                    chunk_target_rows_kept: chunkKept,
                    total_rows_hidden: rowsHidden,
                    total_target_rows_kept: targetRowsKept,
                    status_rewrites: statusRewrites,
                    payload_bytes_remaining: payloadBytesRemaining,
                    target_item: TARGET_ITEM,
                });
            }
        },
    });
    emit('probe.ready', {
        pid: Process.id,
        mode: 'local_query_assets_single_item_ab',
        target_item: TARGET_ITEM,
    });
}

setImmediate(initialize);
