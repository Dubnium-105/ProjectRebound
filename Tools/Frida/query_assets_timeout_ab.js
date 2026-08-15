'use strict';

/*
 * Controlled QueryAssets native-timeout A/B for the pinned Steam build.
 *
 * Static analysis proved that the double at module offset 0x41fcb48 is read
 * only by the QueryAssets async task at 0x9c9200.  This probe changes that
 * task's deadline from five to thirty seconds.  It does not hook RPC payloads
 * and never writes armory, PersistentUser, or loadout state.
 *
 * The companion controller verifies the target executable SHA-256 before
 * loading this script.  The expected bytes below provide a second fail-closed
 * guard against applying the experiment to another game build.
 */

const QUERY_ASSETS_TIMEOUT_OFFSET = 0x41fcb48;
const EXPECTED_TIMEOUT_SECONDS = 5.0;
const TEST_TIMEOUT_SECONDS = 30.0;

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-query-assets-timeout-ab',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function initialize() {
    const game = Process.getModuleByName(
        'ProjectBoundarySteam-Win64-Shipping.exe');
    const timeoutAddress = game.base.add(QUERY_ASSETS_TIMEOUT_OFFSET);
    const observed = timeoutAddress.readDouble();
    if (observed !== EXPECTED_TIMEOUT_SECONDS) {
        throw new Error(
            `QueryAssets timeout guard mismatch at ${timeoutAddress}: ` +
            `expected ${EXPECTED_TIMEOUT_SECONDS}, observed ${observed}`);
    }

    const pageSize = Process.pageSize;
    const page = timeoutAddress.and(ptr(-pageSize));
    if (!Memory.protect(page, pageSize, 'rw-')) {
        throw new Error(`failed to make timeout page writable at ${page}`);
    }
    timeoutAddress.writeDouble(TEST_TIMEOUT_SECONDS);
    Memory.protect(page, pageSize, 'r--');

    const patched = timeoutAddress.readDouble();
    if (patched !== TEST_TIMEOUT_SECONDS) {
        throw new Error(
            `QueryAssets timeout write verification failed: ${patched}`);
    }

    emit('timeout.patched', {
        module_base: game.base.toString(),
        timeout_address: timeoutAddress.toString(),
        timeout_offset: `0x${QUERY_ASSETS_TIMEOUT_OFFSET.toString(16)}`,
        previous_seconds: observed,
        test_seconds: patched,
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
