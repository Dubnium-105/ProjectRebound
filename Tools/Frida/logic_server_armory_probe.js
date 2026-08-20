'use strict';

/*
 * Read-only native armory call-path probe for the pinned Steam build.
 *
 * HandleEnteredArmory resolves the LogicServer interface twice.  This probe
 * records the concrete vtable targets used for the QueryAssets and armory
 * persistence calls, plus any writes to UPBArmoryManager::Armorys.  It never
 * changes arguments, return values, or game memory.
 */

const MODULE_NAME = 'ProjectBoundarySteam-Win64-Shipping.exe';
const EXPECTED_MODULE_SIZE = 105431040;
const OFFSETS = {
    resolveLogicServer: 0x009A18D0,
    handleEnteredArmory: 0x016CD460,
    queryAssetsDelegateBroadcast: 0x00999170,
};

const hookedTargets = new Set();
const hookedConsumerTargets = new Set();
let armoryManager = ptr(0);

function safeS32(address) {
    try {
        return address.readS32();
    } catch (_) {
        return -1;
    }
}

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-logic-server-armory-probe',
        event,
        timestamp_ms: Date.now(),
        thread_id: Process.getCurrentThreadId(),
    }, details));
}

function stack(context) {
    return Thread.backtrace(context, Backtracer.ACCURATE)
        .map(address => DebugSymbol.fromAddress(address).toString());
}

function safePointer(address) {
    try {
        return address.readPointer();
    } catch (_) {
        return ptr(0);
    }
}

function installVirtualHook(module, target, slotName, slotOffset) {
    const key = target.toString();
    if (target.isNull() || hookedTargets.has(key)) return;
    hookedTargets.add(key);
    Interceptor.attach(target, {
        onEnter(args) {
            this.receiver = args[0];
            emit('logic_server.virtual_call', {
                slot_name: slotName,
                slot_offset: `0x${slotOffset.toString(16)}`,
                target: target.toString(),
                target_offset: target.sub(module.base).toString(),
                receiver: args[0].toString(),
                argument_1: args[1].toString(),
                backtrace: stack(this.context),
            });
        },
        onLeave(retval) {
            emit('logic_server.virtual_return', {
                slot_name: slotName,
                target: target.toString(),
                receiver: this.receiver.toString(),
                return_value: retval.toString(),
            });
        },
    });
}

function observeLogicServer(module, object) {
    if (object.isNull()) return;
    const vtable = safePointer(object);
    if (vtable.isNull()) return;
    const queryAssets = safePointer(vtable.add(0x868));
    const armorySaved = safePointer(vtable.add(0x8A0));
    emit('logic_server.resolved', {
        object: object.toString(),
        vtable: vtable.toString(),
        vtable_offset: vtable.sub(module.base).toString(),
        query_assets_target: queryAssets.toString(),
        query_assets_target_offset: queryAssets.sub(module.base).toString(),
        armory_saved_target: armorySaved.toString(),
        armory_saved_target_offset: armorySaved.sub(module.base).toString(),
    });
    installVirtualHook(module, queryAssets, 'query_assets', 0x868);
    installVirtualHook(module, armorySaved, 'armory_saved', 0x8A0);
}

function describeQueryAssetsSubscriber(module, entry, index) {
    const receiver = safePointer(entry);
    const enabled = safeS32(entry.add(8));
    const vtable = safePointer(receiver);
    const callback = safePointer(vtable.add(0x50));
    const consumer = safePointer(receiver.add(0x20));
    installConsumerHook(module, consumer);
    return {
        index,
        entry: entry.toString(),
        enabled,
        receiver: receiver.toString(),
        vtable: vtable.toString(),
        vtable_offset: vtable.isNull() ? null : vtable.sub(module.base).toString(),
        callback: callback.toString(),
        callback_offset: callback.isNull() ? null : callback.sub(module.base).toString(),
        consumer: consumer.toString(),
        consumer_offset: consumer.isNull() ? null : consumer.sub(module.base).toString(),
        is_armory_manager: !armoryManager.isNull() && receiver.equals(armoryManager),
    };
}

function installConsumerHook(module, target) {
    const key = target.toString();
    if (target.isNull() || hookedConsumerTargets.has(key)) return;
    hookedConsumerTargets.add(key);
    Interceptor.attach(target, {
        onEnter(args) {
            const receiver = args[0];
            const itemArray = args[2];
            emit('query_assets.consumer_call', {
                target: target.toString(),
                target_offset: target.sub(module.base).toString(),
                receiver: receiver.toString(),
                count_argument: args[1].toInt32(),
                item_count: itemArray.isNull() ? -1 : safeS32(itemArray.add(8)),
                receiver_is_armory_manager:
                    !armoryManager.isNull() && receiver.equals(armoryManager),
                receiver_array_data: safePointer(receiver.add(0x40)).toString(),
                receiver_array_num: safeS32(receiver.add(0x48)),
                receiver_array_max: safeS32(receiver.add(0x4C)),
            });
        },
        onLeave(retval) {
            emit('query_assets.consumer_return', {
                target: target.toString(),
                return_value: retval.toString(),
            });
        },
    });
}

function initialize() {
    const module = Process.getModuleByName(MODULE_NAME);
    if (module.size !== EXPECTED_MODULE_SIZE) {
        throw new Error(
            `unsupported Boundary image size=${module.size} expected=${EXPECTED_MODULE_SIZE}`);
    }

    Interceptor.attach(module.base.add(OFFSETS.resolveLogicServer), {
        onLeave(retval) {
            try {
                observeLogicServer(module, retval);
            } catch (error) {
                emit('probe.error', {
                    scope: 'resolve_logic_server',
                    message: String(error.stack || error),
                });
            }
        },
    });

    Interceptor.attach(module.base.add(OFFSETS.handleEnteredArmory), {
        onEnter(args) {
            armoryManager = args[0];
            emit('armory.handle_entered', {
                phase: 'enter',
                manager: armoryManager.toString(),
                armory_data: safePointer(armoryManager.add(0x40)).toString(),
                armory_num: armoryManager.add(0x48).readS32(),
                armory_max: armoryManager.add(0x4C).readS32(),
                backtrace: stack(this.context),
            });
        },
        onLeave() {
            emit('armory.handle_entered', {
                phase: 'leave',
                manager: armoryManager.toString(),
                armory_data: safePointer(armoryManager.add(0x40)).toString(),
                armory_num: armoryManager.add(0x48).readS32(),
                armory_max: armoryManager.add(0x4C).readS32(),
            });
        },
    });

    Interceptor.attach(module.base.add(OFFSETS.queryAssetsDelegateBroadcast), {
        onEnter(args) {
            const delegate = args[0];
            const itemArray = args[2];
            const entries = safePointer(delegate);
            const subscriberCount = safeS32(delegate.add(8));
            const itemCount = itemArray.isNull() ? -1 : safeS32(itemArray.add(8));
            const subscribers = [];
            const boundedCount = Math.max(0, Math.min(subscriberCount, 64));
            for (let i = 0; i < boundedCount; ++i) {
                subscribers.push(describeQueryAssetsSubscriber(
                    module, entries.add(i * 0x10), i));
            }
            emit('query_assets.delegate_broadcast', {
                delegate: delegate.toString(),
                result_code: args[1].toInt32(),
                item_array: itemArray.toString(),
                item_count: itemCount,
                subscriber_count: subscriberCount,
                subscribers,
                backtrace: stack(this.context),
            });
        },
        onLeave() {
            emit('query_assets.delegate_complete', {
                manager: armoryManager.toString(),
                armory_data: armoryManager.isNull()
                    ? null : safePointer(armoryManager.add(0x40)).toString(),
                armory_num: armoryManager.isNull()
                    ? -1 : safeS32(armoryManager.add(0x48)),
                armory_max: armoryManager.isNull()
                    ? -1 : safeS32(armoryManager.add(0x4C)),
            });
        },
    });

    emit('probe.ready', {
        pid: Process.id,
        module_base: module.base.toString(),
        module_size: module.size,
        mode: 'read_only',
    });
}

setImmediate(() => {
    try {
        initialize();
    } catch (error) {
        emit('probe.error', {
            scope: 'initialize',
            message: String(error.stack || error),
        });
    }
});
