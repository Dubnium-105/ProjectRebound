'use strict';

/*
 * Read-only probe for the pinned Boundary client's QoS completion boundary.
 *
 * It records control-flow entries and a pair of one-byte state flags. It does
 * not replace return values, call game functions, or write process memory.
 * The Python controller validates the exact executable SHA-256 before loading
 * this script.
 */

const RVAS = {
    qos_async_entry: 0x11defc0,
    qos_async_worker: 0x11df300,
    qos_send_probe: 0x11e07f0,
    qos_completion_dispatch: 0x11e0f80,
    qos_finalize: 0x11e1490,
    qos_receive: 0x11e2180,
    qos_tick: 0x11e2710,
};

const base = Process.mainModule.base;
const counters = Object.create(null);

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-qos-login-probe',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function readState(instance) {
    if (instance === null || instance.isNull()) {
        return { instance: '0x0' };
    }
    const state = { instance: instance.toString() };
    try {
        state.finalized = instance.add(288).readU8();
        state.waiting = instance.add(289).readU8();
    } catch (error) {
        state.state_error = String(error);
    }
    return state;
}

function attach(name, callbacks) {
    const address = base.add(RVAS[name]);
    Interceptor.attach(address, callbacks);
    emit('hook.installed', { name, address: address.toString() });
}

function initialize() {
    attach('qos_async_entry', {
        onEnter(args) {
            emit('qos.async_entry', { context: args[0].toString() });
        },
    });

    attach('qos_async_worker', {
        onEnter(args) {
            emit('qos.async_worker.enter', readState(args[0]));
        },
        onLeave(retval) {
            emit('qos.async_worker.leave', { return_value: retval.toString() });
        },
    });

    attach('qos_send_probe', {
        onEnter(args) {
            counters.send = (counters.send || 0) + 1;
            emit('qos.send_probe', Object.assign({ call: counters.send }, readState(args[0])));
        },
    });

    attach('qos_receive', {
        onEnter(args) {
            counters.receive = (counters.receive || 0) + 1;
            emit('qos.receive', Object.assign({ call: counters.receive }, readState(args[0])));
        },
    });

    attach('qos_tick', {
        onEnter(args) {
            counters.tick = (counters.tick || 0) + 1;
            if (counters.tick <= 20 || counters.tick % 100 === 0) {
                emit('qos.tick.enter', Object.assign({ call: counters.tick }, readState(args[0])));
            }
        },
        onLeave(retval) {
            if (counters.tick <= 20 || counters.tick % 100 === 0) {
                emit('qos.tick.leave', { call: counters.tick, return_value: retval.toString() });
            }
        },
    });

    attach('qos_finalize', {
        onEnter(args) {
            this.instance = args[0];
            emit('qos.finalize.enter', readState(this.instance));
        },
        onLeave(retval) {
            emit('qos.finalize.leave', Object.assign({ return_value: retval.toString() }, readState(this.instance)));
        },
    });

    attach('qos_completion_dispatch', {
        onEnter(args) {
            emit('qos.completion_dispatch.enter', {
                context: args[0].toString(),
                status: args[1].toInt32(),
                result: args[2].toString(),
            });
        },
        onLeave(retval) {
            emit('qos.completion_dispatch.leave', { return_value: retval.toString() });
        },
    });

    emit('probe.ready', {
        pid: Process.id,
        image_base: base.toString(),
        mode: 'read_only_qos_login',
    });
}

setImmediate(initialize);
