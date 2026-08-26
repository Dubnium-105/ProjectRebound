'use strict';

// Read-only diagnostic probe for the fixed Boundary build.  It observes the
// Payload's narrow multi-match console messages without changing game flow.

const interesting = /\[(?:MULTIMATCH(?:_GATE|_STATUS|_TRACE)?|SERVER|RESPAWN)\]|ServerTravel|ReturnToMainMenu|RequestExit|Traveling|LoadingNext/i;

function emit(api, text) {
    if (!text || !interesting.test(text)) {
        return;
    }

    for (const line of text.split(/\r?\n/)) {
        if (line && interesting.test(line)) {
            send({
                source: 'project-rebound-multimatch-console-probe',
                event: 'console.line',
                api,
                timestamp_ms: Date.now(),
                line,
            });
        }
    }
}

function attach(name, callbacks) {
    const address = Process.getModuleByName('kernel32.dll').getExportByName(name);
    Interceptor.attach(address, callbacks);
}

attach('OutputDebugStringA', {
    onEnter(args) {
        try { emit('OutputDebugStringA', args[0].readUtf8String()); } catch (_) {}
    },
});

attach('OutputDebugStringW', {
    onEnter(args) {
        try { emit('OutputDebugStringW', args[0].readUtf16String()); } catch (_) {}
    },
});

attach('WriteFile', {
    onEnter(args) {
        try {
            const length = Math.min(args[2].toUInt32(), 16384);
            if (length > 0) {
                emit('WriteFile', args[1].readUtf8String(length));
            }
        } catch (_) {}
    },
});

attach('WriteConsoleA', {
    onEnter(args) {
        try {
            const length = Math.min(args[2].toUInt32(), 16384);
            if (length > 0) {
                emit('WriteConsoleA', args[1].readUtf8String(length));
            }
        } catch (_) {}
    },
});

attach('WriteConsoleW', {
    onEnter(args) {
        try {
            const length = Math.min(args[2].toUInt32(), 8192);
            if (length > 0) {
                emit('WriteConsoleW', args[1].readUtf16String(length));
            }
        } catch (_) {}
    },
});

send({
    source: 'project-rebound-multimatch-console-probe',
    event: 'probe.ready',
    timestamp_ms: Date.now(),
});
