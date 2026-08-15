'use strict';

/*
 * Controlled native RPC frame-limit A/B for the pinned Steam build.
 *
 * The frame reader at module offset 0x9c3720 rejects a big-endian frame
 * length above 0x100000.  Its caller also allocates and clears a 0x10000a
 * byte single-frame output buffer.  The complete QueryAssets response is
 * about 1.31 MiB, so the stock limit discards it before protobuf decoding.
 * Raising only the comparison overflows that fixed output buffer.  This
 * experiment therefore raises the complete four-instruction limit group to
 * 2 MiB.  It does not hook RPC payloads or write armory/loadout state.
 *
 * The companion controller verifies the target executable SHA-256.  The
 * expected instruction bytes below are a second fail-closed build guard.
 */

const PATCHES = [
    {
        name: 'length_guard',
        offset: 0x9c37bb,
        expected: [0x81, 0xfe, 0x00, 0x00, 0x10, 0x00],
        patched: [0x81, 0xfe, 0x00, 0x00, 0x20, 0x00],
    },
    {
        name: 'output_allocation',
        offset: 0x9c3b47,
        expected: [0xba, 0x0a, 0x00, 0x10, 0x00],
        patched: [0xba, 0x0a, 0x00, 0x20, 0x00],
    },
    {
        name: 'output_capacity',
        offset: 0x9c3b68,
        expected: [0x8d, 0x83, 0x0a, 0x00, 0x10, 0x00],
        patched: [0x8d, 0x83, 0x0a, 0x00, 0x20, 0x00],
    },
    {
        name: 'output_clear',
        offset: 0x9c3b87,
        expected: [0x41, 0xb8, 0x0a, 0x00, 0x10, 0x00],
        patched: [0x41, 0xb8, 0x0a, 0x00, 0x20, 0x00],
    },
];

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-query-assets-frame-limit-ab',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function equalBytes(observed, expected) {
    if (observed.length !== expected.length) {
        return false;
    }
    for (let index = 0; index < expected.length; ++index) {
        if (observed[index] !== expected[index]) {
            return false;
        }
    }
    return true;
}

function hexBytes(bytes) {
    return Array.from(bytes, value => value.toString(16).padStart(2, '0')).join(' ');
}

function initialize() {
    const game = Process.getModuleByName(
        'ProjectBoundarySteam-Win64-Shipping.exe');
    for (const patch of PATCHES) {
        const instruction = game.base.add(patch.offset);
        const observed = new Uint8Array(
            instruction.readByteArray(patch.expected.length));
        if (!equalBytes(observed, patch.expected)) {
            throw new Error(
                `${patch.name} guard mismatch at ${instruction}: expected ` +
                `${hexBytes(patch.expected)}, observed ${hexBytes(observed)}`);
        }
    }

    const pageSize = Process.pageSize;
    const firstInstruction = game.base.add(PATCHES[0].offset);
    const page = firstInstruction.and(ptr(-pageSize));
    if (!Memory.protect(page, pageSize, 'rwx')) {
        throw new Error(`failed to make frame-limit page writable at ${page}`);
    }
    for (const patch of PATCHES) {
        game.base.add(patch.offset).writeByteArray(patch.patched);
    }
    Memory.protect(page, pageSize, 'r-x');

    for (const patch of PATCHES) {
        const instruction = game.base.add(patch.offset);
        const patched = new Uint8Array(
            instruction.readByteArray(patch.patched.length));
        if (!equalBytes(patched, patch.patched)) {
            throw new Error(
                `${patch.name} write verification failed: ${hexBytes(patched)}`);
        }
    }

    emit('frame_limit.patched', {
        module_base: game.base.toString(),
        instruction_offsets: PATCHES.map(
            patch => `0x${patch.offset.toString(16)}`),
        previous_limit_bytes: 0x100000,
        test_limit_bytes: 0x200000,
        patched_instruction_count: PATCHES.length,
    });
}

setImmediate(() => {
    try {
        initialize();
    } catch (error) {
        emit('probe.error', { message: String(error.stack || error) });
        throw error;
    }
});
