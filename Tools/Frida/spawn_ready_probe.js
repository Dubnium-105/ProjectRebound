'use strict';

// Fixed-build, read-only trace for Boundary's native StartSpot handshake.
const image = Process.getModuleByName('ProjectBoundarySteam-Win64-Shipping.exe');
const hooks = [
    { name: 'PC.ClientRestartAtStartSpot.native', rva: 0x15bf130, startSpotArg: 1 },
    { name: 'Character.ClientReadyAtStartSpot.native', rva: 0x15828a0, startSpotArg: 1 },
    { name: 'PC.ServerReadyAtStartSpot.wrapper', rva: 0x1843890 },
    { name: 'PC.ClientReadyAtStartSpot.wrapper', rva: 0x1841860 },
];

for (const hook of hooks) {
    Interceptor.attach(image.base.add(hook.rva), {
        onEnter(args) {
            const payload = {
                source: 'project-rebound-spawn-ready-probe',
                event: 'spawn.ready_call',
                timestamp_ms: Date.now(),
                function_name: hook.name,
                object: args[0].toString(),
            };
            if (hook.startSpotArg !== undefined) {
                payload.start_spot = args[hook.startSpotArg].toString();
            }
            send(payload);
        },
    });
}

send({
    source: 'project-rebound-spawn-ready-probe',
    event: 'probe.ready',
    timestamp_ms: Date.now(),
    image_base: image.base.toString(),
});
