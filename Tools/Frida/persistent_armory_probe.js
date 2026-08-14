'use strict';

/* One-shot, read-only comparison of UPBPersistentUser armory state. */

const MODULE_NAME = 'ProjectBoundarySteam-Win64-Shipping.exe';
const GOBJECTS_OFFSET = 0x05D65FE0;
const APPEND_STRING_OFFSET = 0x019D82B0;
const MAX_OBJECTS = 2_000_000;
const MAX_ITEMS = 250_000;

function emit(event, details = {}) {
    send(Object.assign({
        source: 'project-rebound-persistent-armory-probe',
        event,
        timestamp_ms: Date.now(),
    }, details));
}

function initialize() {
    const module = Process.getModuleByName(MODULE_NAME);
    const appendString = new NativeFunction(
        module.base.add(APPEND_STRING_OFFSET),
        'void',
        ['pointer', 'pointer'],
        'win64',
    );
    const nameCache = new Map();
    const classCache = new Map();

    function fname(address) {
        const key = `${address.readS32()}:${address.add(4).readU32()}`;
        if (nameCache.has(key)) {
            return nameCache.get(key);
        }
        const data = Memory.alloc(512);
        const output = Memory.alloc(16);
        output.writePointer(data);
        output.add(8).writeS32(0);
        output.add(12).writeS32(256);
        appendString(address, output);
        const value = data.readUtf16String() || '';
        nameCache.set(key, value);
        return value;
    }

    function objectName(object) {
        return fname(object.add(0x18));
    }

    function className(object) {
        const klass = object.add(0x10).readPointer();
        if (klass.isNull()) {
            return '';
        }
        const key = klass.toString();
        if (!classCache.has(key)) {
            classCache.set(key, objectName(klass));
        }
        return classCache.get(key);
    }

    function readArray(owner, offset, stride, withCount) {
        const array = owner.add(offset);
        const data = array.readPointer();
        const num = array.add(8).readS32();
        const max = array.add(12).readS32();
        if (num < 0 || max < num || max > MAX_ITEMS || (num > 0 && data.isNull())) {
            throw new Error(`invalid array offset=${offset} num=${num} max=${max}`);
        }
        const ids = [];
        let countZero = 0;
        let countPositive = 0;
        for (let index = 0; index < num; index += 1) {
            const item = data.add(index * stride);
            ids.push(fname(item));
            if (withCount) {
                const count = item.add(8).readS32();
                if (count === 0) {
                    countZero += 1;
                } else if (count > 0) {
                    countPositive += 1;
                }
            }
        }
        return {
            data: data.toString(),
            num,
            max,
            count_zero: countZero,
            count_positive: countPositive,
            item_ids: ids,
        };
    }

    try {
        const objectArray = module.base.add(GOBJECTS_OFFSET);
        const chunks = objectArray.readPointer();
        const numElements = objectArray.add(0x14).readS32();
        const numChunks = objectArray.add(0x1C).readS32();
        if (chunks.isNull() || numElements <= 0 || numElements > MAX_OBJECTS || numChunks <= 0) {
            throw new Error(`GObjects not ready num=${numElements} chunks=${numChunks}`);
        }
        let found = 0;
        for (let index = 0; index < numElements; index += 1) {
            const chunkIndex = Math.floor(index / 0x10000);
            if (chunkIndex >= numChunks) {
                break;
            }
            const chunk = chunks.add(chunkIndex * Process.pointerSize).readPointer();
            if (chunk.isNull()) {
                continue;
            }
            const object = chunk.add((index % 0x10000) * 0x18).readPointer();
            if (object.isNull()) {
                continue;
            }
            try {
                const typeName = className(object);
                if (!typeName.includes('PersistentUser') || objectName(object).startsWith('Default__')) {
                    continue;
                }
                found += 1;
                emit('persistent_user.snapshot', {
                    object: object.toString(),
                    object_name: objectName(object),
                    class_name: typeName,
                    saved_armory: readArray(object, 0x48, 0x08, false),
                    runtime_armory: readArray(object, 0x68, 0x10, true),
                });
            } catch (_) {
                // Object slots can change while scanning.
            }
        }
        emit('probe.done', { found, num_elements: numElements });
    } catch (error) {
        emit('probe.error', { message: String(error.stack || error) });
        emit('probe.done', { found: 0 });
    }
}

setImmediate(initialize);
