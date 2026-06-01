// analyze-logs.js — Automated log analysis & proto schema correction
//
// Usage:
//   node tools/analyze-logs.js [session-dir-or-log-file]
//   node tools/analyze-logs.js logs/proxy-2026-04-29T05-10-01.log
//   node tools/analyze-logs.js logs/binary/           # analyze all sessions
//   node tools/analyze-logs.js --latest                # most recent session
//   node tools/analyze-logs.js --latest --llm          # with LLM semantic analysis
//
// Output: logs/binary/{session}/analysis-report.md

const fs = require('fs');
const path = require('path');
const { execSync, spawnSync } = require('child_process');
const protobuf = require('protobufjs');

// =====================================================================
// Config
// =====================================================================

const BASE_DIR = path.join(__dirname, '..');
const LOG_DIR = path.join(BASE_DIR, 'logs');
const BINARY_DIR = path.join(LOG_DIR, 'binary');
const PROTO_BASE = path.join(BASE_DIR, 'game', 'proto');
const REPORT_FILE = 'analysis-report.md';

// LLM config (set via env vars)
const LLM_ENABLED = process.argv.includes('--llm');
const LLM_API_KEY = process.env.ANTHROPIC_API_KEY || '';
const LLM_MODEL = process.env.LLM_MODEL || 'claude-sonnet-4-6';

// =====================================================================
// Proto schema index — load all existing protos, index by message name
// =====================================================================

const protoSchemas = {}; // messageName → { fields: { fieldNum: { name, type, rule } }, file: string }

function loadProtoSchemas() {
    for (const sub of ['Request', 'Response']) {
        const dir = path.join(PROTO_BASE, sub);
        if (!fs.existsSync(dir)) continue;
        for (const file of fs.readdirSync(dir)) {
            if (!file.endsWith('.proto')) continue;
            try {
                const root = protobuf.loadSync(path.join(dir, file));
                const ns = root.lookup('ProjectBoundary');
                for (const [name, type] of Object.entries(ns.nested || {})) {
                    if (!type.fields) continue;
                    const fields = {};
                    for (const [fname, field] of Object.entries(type.fields)) {
                        fields[field.id] = {
                            name: fname,
                            type: field.type,
                            rule: field.rule,     // 'repeated' | 'optional' | undefined(required)
                        };
                    }
                    protoSchemas[name] = {
                        fields,
                        file: path.relative(BASE_DIR, path.join(dir, file)),
                        fullPath: path.join(dir, file),
                    };
                }
            } catch (_) { /* skip */ }
        }
    }
    return Object.keys(protoSchemas).length;
}

// =====================================================================
// protoc --decode_raw
// =====================================================================

function protocDecodeRaw(buf) {
    if (!buf || buf.length === 0) return '(empty)';
    try {
        const result = spawnSync('protoc', ['--decode_raw'], {
            input: buf,
            encoding: 'utf-8',
            timeout: 10000,
            maxBuffer: 10 * 1024 * 1024,
        });
        if (result.error) return `protoc error: ${result.error.message}`;
        if (result.status !== 0) return `protoc exit ${result.status}: ${result.stderr}`;
        return result.stdout.trim() || '(empty output)';
    } catch (e) {
        return `protoc exception: ${e.message}`;
    }
}

function parseDecodeRaw(raw) {
    // Parse protoc --decode_raw output into structured form
    // Format: "1: \"value\"\n2: 42\n3 {\n  1: \"nested\"\n}"
    const lines = raw.split('\n');
    const fields = [];
    for (const line of lines) {
        const m = line.match(/^(\d+):\s+(.*)$/);
        if (m) {
            const num = parseInt(m[1]);
            let val = m[2].trim();
            let wireType = 'length-delimited';
            if (/^-?\d+$/.test(val)) {
                wireType = 'varint';
                val = parseInt(val);
            } else if (/^0x[0-9a-fA-F]+$/.test(val)) {
                wireType = 'varint';
            } else if (/^\d+\.\d+$/.test(val)) {
                wireType = '64-bit';
                val = parseFloat(val);
            } else if (val.startsWith('"') && val.endsWith('"')) {
                wireType = 'length-delimited';
                val = val.slice(1, -1);
            } else if (val === '{}') {
                wireType = 'length-delimited';
                val = '(empty)';
            }
            fields.push({ num, val, wireType, embedded: null });
        }
    }
    return fields;
}

// =====================================================================
// Compare --decode_raw output against existing proto schema
// =====================================================================

function compareWithSchema(messageName, rawFields, innerBuf) {
    const schema = protoSchemas[messageName];
    const issues = [];
    const suggestions = [];

    if (!schema) {
        // No schema exists — suggest creating one from raw
        suggestions.push({
            type: 'new_message',
            messageName,
            fields: rawFields.map(f => ({
                num: f.num,
                suggestedType: inferTypeFromWireType(f.wireType, f.val),
                suggestedName: `field${f.num}`,
                sampleValue: f.val,
            })),
        });
        return { issues, suggestions };
    }

    // Check each raw field against schema
    const schemaFieldNums = new Set(Object.keys(schema.fields).map(Number));
    const rawFieldNums = new Set(rawFields.map(f => f.num));

    for (const rf of rawFields) {
        if (!schemaFieldNums.has(rf.num)) {
            issues.push(`Field #${rf.num} (value: ${JSON.stringify(rf.val)}) not in schema for ${messageName}`);
            suggestions.push({
                type: 'add_field',
                messageName,
                fieldNum: rf.num,
                suggestedType: inferTypeFromWireType(rf.wireType, rf.val),
                suggestedName: guessFieldName(rf.val, rf.wireType),
                sampleValue: rf.val,
            });
        } else {
            const sf = schema.fields[rf.num];
            const expectedWT = typeToWireType(sf.type);
            if (rf.wireType !== expectedWT && rf.wireType !== 'varint') {
                // varint covers int32/int64/bool/enum — too broad to flag
                issues.push(`Field #${rf.num} (${sf.name}): expected wire type ${expectedWT} (${sf.type}), got ${rf.wireType}. Value: ${JSON.stringify(rf.val)}`);
                suggestions.push({
                    type: 'fix_type',
                    messageName,
                    fieldNum: rf.num,
                    fieldName: sf.name,
                    currentType: sf.type,
                    suggestedType: inferTypeFromWireType(rf.wireType, rf.val),
                    sampleValue: rf.val,
                });
            }
        }
    }

    // Check for schema fields not in raw data (may be unused or have defaults)
    for (const num of schemaFieldNums) {
        if (!rawFieldNums.has(num)) {
            const sf = schema.fields[num];
            issues.push(`Schema field #${num} (${sf.name}) not present in captured data`);
        }
    }

    return { issues, suggestions };
}

function inferTypeFromWireType(wireType, sampleVal) {
    switch (wireType) {
        case 'varint':
            if (typeof sampleVal === 'number' && sampleVal <= 1) return 'bool';
            if (typeof sampleVal === 'number') return 'int32';
            return 'int32';
        case '64-bit': return 'double';
        case '32-bit': return 'float';
        case 'length-delimited':
            if (typeof sampleVal === 'string' && /^[0-9a-f-]{36}$/i.test(sampleVal)) return 'string'; // UUID
            if (typeof sampleVal === 'string' && sampleVal.length > 100) return 'string';
            if (typeof sampleVal === 'string' && /^\d+$/.test(sampleVal)) return 'string'; // numeric ID
            if (typeof sampleVal === 'string' && sampleVal === '(empty)') return 'bytes';
            return 'string';
        default: return 'bytes';
    }
}

function typeToWireType(type) {
    switch (type) {
        case 'int32': case 'int64': case 'uint32': case 'uint64':
        case 'sint32': case 'sint64': case 'bool': case 'enum':
            return 'varint';
        case 'double': case 'fixed64': case 'sfixed64': return '64-bit';
        case 'float': case 'fixed32': case 'sfixed32': return '32-bit';
        case 'string': case 'bytes': return 'length-delimited';
        default: return 'length-delimited'; // message types
    }
}

function guessFieldName(sampleVal, wireType) {
    if (wireType === 'varint' && typeof sampleVal === 'number' && sampleVal <= 1) return 'enabled';
    if (typeof sampleVal === 'string' && /^[0-9a-f-]{36}$/i.test(sampleVal)) return 'id';
    if (typeof sampleVal === 'string' && /^\d+$/.test(sampleVal) && sampleVal.length > 10) return 'userId';
    if (typeof sampleVal === 'string' && /^\d+$/.test(sampleVal)) return 'count';
    return 'unknown';
}

// =====================================================================
// Text log parser — extract messages from proxy text log format
// =====================================================================

function parseHexDumpBlock(lines, startIdx) {
    // Parse hex dump like: "  0000: 00 00 00 51 08 02 12 21 ..."
    const hexBytes = [];
    let i = startIdx;
    while (i < lines.length) {
        const line = lines[i];
        const m = line.match(/^\s*[0-9a-f]{4}:\s+((?:[0-9a-f]{2}\s?)+)/i);
        if (!m) break;
        const hexStr = m[1].replace(/\s+/g, '');
        for (let j = 0; j < hexStr.length; j += 2) {
            hexBytes.push(parseInt(hexStr.substring(j, j + 2), 16));
        }
        i++;
    }
    return { buf: Buffer.from(hexBytes), endIdx: i };
}

function parseTextLog(logPath) {
    if (!fs.existsSync(logPath)) return [];
    const content = fs.readFileSync(logPath, 'utf-8');
    const sections = content.split(/={40,}/);
    const messages = [];

    for (const section of sections) {
        const lines = section.split('\n');
        let direction = null, msgId = null, rpcPath = null, isUnknown = false;
        let frameBuf = null, innerBuf = null, time = null, wrapperType = '?';
        let decoded = null;

        for (let i = 0; i < lines.length; i++) {
            const line = lines[i];

            // Header line: [→ REQ] #2 | /path | 85 bytes | RequestWrapper *** UNKNOWN RPC ***
            const hdrMatch = line.match(/\[(→ REQ|← RES)\]\s+#(\d+)\s+\|\s+(\S+)\s+\|\s+(\d+)\s+bytes\s+\|\s+(\S+)(.*)/);
            if (hdrMatch) {
                direction = hdrMatch[1] === '→ REQ' ? 'req' : 'res';
                msgId = parseInt(hdrMatch[2]);
                rpcPath = hdrMatch[3];
                wrapperType = hdrMatch[5];
                isUnknown = hdrMatch[6].includes('UNKNOWN RPC');
            }

            // HANDSHAKE
            const hsMatch = line.match(/\[(→|←) HANDSHAKE/);
            if (hsMatch) {
                direction = hsMatch[1] === '→' ? 'req' : 'res';
                rpcPath = '(handshake)';
            }

            // Time
            const tMatch = line.match(/Time:\s+(.+)$/);
            if (tMatch) time = tMatch[1].trim();

            // Raw frame hex dump
            if (line.includes('-- Raw frame')) {
                const result = parseHexDumpBlock(lines, i + 1);
                frameBuf = result.buf;
                i = result.endIdx - 1;
            }

            // Inner message hex dump
            if (line.includes('-- Inner message')) {
                const result = parseHexDumpBlock(lines, i + 1);
                innerBuf = result.buf;
                i = result.endIdx - 1;
            }

            // Decoded JSON
            if (line.includes('-- Decoded --')) {
                const jsonLines = [];
                let j = i + 1;
                while (j < lines.length && lines[j].trim() && !lines[j].startsWith('=')) {
                    jsonLines.push(lines[j]);
                    j++;
                }
                try {
                    decoded = JSON.parse(jsonLines.join('\n'));
                } catch (_) {}
                i = j - 1;
            }
        }

        if (direction && rpcPath && rpcPath !== '?' && rpcPath !== '(handshake)') {
            messages.push({
                direction, msgId, rpcPath, wrapperType, isUnknown,
                frameLen: frameBuf ? frameBuf.length : 0,
                innerLen: innerBuf ? innerBuf.length : 0,
                decoded,
                _frameBuf: frameBuf,
                _innerBuf: innerBuf,
                time,
            });
        }
    }
    return messages;
}

// =====================================================================
// Session discovery
// =====================================================================

function findLatestSession() {
    // Prefer binary dir if it has data
    if (fs.existsSync(BINARY_DIR)) {
        const binFiles = fs.readdirSync(BINARY_DIR).filter(f => f.endsWith('_meta.json'));
        if (binFiles.length > 0) return { type: 'binary', path: BINARY_DIR };
    }
    // Fall back to text logs
    if (fs.existsSync(LOG_DIR)) {
        const logs = fs.readdirSync(LOG_DIR)
            .filter(f => f.startsWith('proxy-') && f.endsWith('.log'))
            .sort()
            .reverse();
        if (logs.length > 0) return { type: 'text', path: path.join(LOG_DIR, logs[0]) };
    }
    return null;
}

function getSessionData(source) {
    if (!source) return [];
    if (source.type === 'binary') {
        const metas = fs.readdirSync(source.path)
            .filter(f => f.endsWith('_meta.json'))
            .sort();
        const messages = [];
        for (const metaFile of metas) {
            try {
                const meta = JSON.parse(fs.readFileSync(path.join(source.path, metaFile), 'utf-8'));
                const prefix = metaFile.replace('_meta.json', '');
                meta._frameBuf = fs.existsSync(path.join(source.path, `${prefix}_frame.bin`))
                    ? fs.readFileSync(path.join(source.path, `${prefix}_frame.bin`)) : null;
                meta._innerBuf = fs.existsSync(path.join(source.path, `${prefix}_inner.bin`))
                    ? fs.readFileSync(path.join(source.path, `${prefix}_inner.bin`)) : null;
                messages.push(meta);
            } catch (_) {}
        }
        return messages;
    }
    if (source.type === 'text') {
        return parseTextLog(source.path);
    }
    return [];
}

// =====================================================================
// LLM Integration (optional)
// =====================================================================

async function callLLM(prompt) {
    if (!LLM_ENABLED) return null;
    if (!LLM_API_KEY) {
        console.log('[LLM] Skipped: set ANTHROPIC_API_KEY env var to enable');
        return null;
    }

    // Try using @anthropic-ai/sdk if available
    try {
        const { default: Anthropic } = await import('@anthropic-ai/sdk');
        const client = new Anthropic({ apiKey: LLM_API_KEY });
        const msg = await client.messages.create({
            model: LLM_MODEL,
            max_tokens: 2048,
            messages: [{ role: 'user', content: prompt }],
        });
        return msg.content[0].text;
    } catch (e) {
        if (e.code === 'ERR_MODULE_NOT_FOUND' || e.message.includes('Cannot find')) {
            console.log('[LLM] @anthropic-ai/sdk not installed. Run: npm install @anthropic-ai/sdk');
        } else {
            console.log(`[LLM] Error: ${e.message}`);
        }
        return null;
    }
}

function buildLLMPrompt(suggestions, messageName) {
    const fieldList = suggestions
        .filter(s => s.type === 'add_field' || s.type === 'new_message')
        .map(s => `  field #${s.fieldNum}: type=${s.suggestedType}, sample_value=${JSON.stringify(s.sampleValue)}`)
        .join('\n');

    return `You are reverse-engineering a game's protobuf protocol.

Message type: "${messageName}"
Discovered fields (from --decode_raw):
${fieldList}

Based on the field types and sample values, suggest meaningful field names and the likely purpose of this message. Keep names in camelCase. Respond with ONLY a JSON array of {"fieldNum": N, "suggestedName": "name", "reason": "brief reason"}.`;
}

// =====================================================================
// Report generation
// =====================================================================

function generateReport(sessionData, analyses) {
    const lines = [];

    lines.push('# Proto Analysis Report');
    lines.push(`Generated: ${new Date().toISOString()}`);
    lines.push(`Session messages: ${sessionData.length}`);
    lines.push(`Analyzed: ${analyses.length}`);
    lines.push('');

    // Summary
    const total = sessionData.length;
    const unknownRPCs = sessionData.filter(m => m.isUnknown).length;
    const reqCount = sessionData.filter(m => m.direction === 'req').length;
    const resCount = sessionData.filter(m => m.direction === 'res').length;
    const decodeErrors = sessionData.filter(m => m.decoded && m.decoded._error).length;

    lines.push('## Summary');
    lines.push('');
    lines.push(`| Metric | Count |`);
    lines.push(`|--------|-------|`);
    lines.push(`| Total messages | ${total} |`);
    lines.push(`| Requests | ${reqCount} |`);
    lines.push(`| Responses | ${resCount} |`);
    lines.push(`| Unknown RPCs | ${unknownRPCs} |`);
    lines.push(`| Decode errors | ${decodeErrors} |`);
    lines.push('');

    // RPC frequency
    const rpcFreq = new Map();
    for (const m of sessionData) {
        const key = `${m.direction} ${m.rpcPath}`;
        rpcFreq.set(key, (rpcFreq.get(key) || 0) + 1);
    }
    lines.push('## RPC Call Summary');
    lines.push('');
    lines.push('| Direction | RPCPath | Count |');
    lines.push('|-----------|---------|-------|');
    for (const [key, count] of [...rpcFreq.entries()].sort()) {
        lines.push(`| ${key} | ${count} |`);
    }
    lines.push('');

    // Unknown RPCs detail
    if (unknownRPCs > 0) {
        lines.push('## Unknown RPCPaths');
        lines.push('');
        const unknowns = sessionData.filter(m => m.isUnknown);
        for (const m of unknowns) {
            lines.push(`### \`${m.rpcPath}\``);
            lines.push(`- Direction: ${m.direction}`);
            lines.push(`- MessageId: ${m.msgId}`);
            lines.push(`- Frame size: ${m.frameLen} bytes`);
            lines.push(`- Inner size: ${m.innerLen} bytes`);
            lines.push('');

            if (m._innerBuf) {
                const raw = protocDecodeRaw(m._innerBuf);
                lines.push('```');
                lines.push(raw);
                lines.push('```');
                lines.push('');
            }
        }
    }

    // Schema corrections
    if (analyses.length > 0) {
        lines.push('## Schema Corrections');
        lines.push('');

        for (const a of analyses) {
            if (!a.suggestions || a.suggestions.length === 0) continue;
            lines.push(`### ${a.messageName || 'Unknown'} (\`${a.rpcPath}\`)`);
            lines.push('');

            if (a.issues && a.issues.length > 0) {
                lines.push('**Issues:**');
                for (const issue of a.issues) {
                    lines.push(`- ${issue}`);
                }
                lines.push('');
            }

            lines.push('**Suggested corrections:**');
            lines.push('');
            lines.push('| Type | Field # | Current | Suggested | Sample Value |');
            lines.push('|------|---------|---------|-----------|--------------|');
            for (const s of a.suggestions) {
                const current = s.currentType || '-';
                const suggested = s.suggestedName ? `${s.suggestedType} ${s.suggestedName}` : s.suggestedType;
                const sample = typeof s.sampleValue === 'string'
                    ? `\`${s.sampleValue.substring(0, 50)}\``
                    : s.sampleValue;
                lines.push(`| ${s.type} | ${s.fieldNum || '-'} | ${current} | ${suggested} | ${sample} |`);
            }
            lines.push('');

            // LLM suggestions
            if (a.llmSuggestions) {
                lines.push('**LLM suggestions:**');
                lines.push('');
                for (const ls of a.llmSuggestions) {
                    lines.push(`- Field #${ls.fieldNum}: \`${ls.suggestedName}\` — ${ls.reason}`);
                }
                lines.push('');
            }
        }
    }

    // Proto file fix suggestions
    const newMessages = analyses.filter(a =>
        a.suggestions && a.suggestions.some(s => s.type === 'new_message')
    );
    if (newMessages.length > 0) {
        lines.push('## Suggested New Proto Files');
        lines.push('');
        for (const nm of newMessages) {
            const newFields = nm.suggestions.filter(s => s.type === 'new_message');
            for (const nf of newFields) {
                lines.push(`### \`${nm.messageName || nm.rpcPath}\``);
                lines.push('');
                lines.push('```protobuf');
                lines.push('syntax = "proto3";');
                lines.push('package ProjectBoundary;');
                lines.push('');
                lines.push(`message ${nm.messageName || 'UnknownMessage'} {`);
                for (const f of nf.fields) {
                    const name = f.suggestedName || `field${f.num}`;
                    lines.push(`  ${f.suggestedType} ${name} = ${f.num};  // sample: ${JSON.stringify(f.sampleValue)}`);
                }
                lines.push('}');
                lines.push('```');
                lines.push('');
            }
        }
    }

    return lines.join('\n');
}

// =====================================================================
// Main
// =====================================================================

async function main() {
    const schemaCount = loadProtoSchemas();
    console.log(`[INFO] Loaded ${schemaCount} proto schemas`);

    let sessionSource = null;

    // Parse args
    const args = process.argv.slice(2).filter(a => a !== '--llm');
    if (args.includes('--latest') || args.length === 0) {
        sessionSource = findLatestSession();
        if (!sessionSource) {
            console.log('[ERROR] No session data found in logs/');
            console.log('Run the proxy first to capture traffic, then re-run this tool.');
            process.exit(1);
        }
    } else {
        const arg = args[0];
        if (arg.endsWith('.log')) {
            sessionSource = { type: 'text', path: path.resolve(arg) };
        } else if (fs.existsSync(arg) && fs.statSync(arg).isDirectory()) {
            sessionSource = { type: 'binary', path: path.resolve(arg) };
        } else {
            console.log(`[ERROR] Invalid argument: ${arg}`);
            console.log('Usage: node tools/analyze-logs.js [--latest | logfile.log | binary-dir] [--llm]');
            process.exit(1);
        }
    }

    console.log(`[INFO] Source: ${sessionSource.type} — ${sessionSource.path}`);
    const sessionData = getSessionData(sessionSource);
    console.log(`[INFO] Found ${sessionData.length} messages`);

    if (sessionData.length === 0) {
        console.log('[WARN] No messages to analyze.');
        process.exit(0);
    }

    // Analyze messages with issues
    const analyses = [];
    const seenRPCs = new Set();

    for (const msg of sessionData) {
        const key = `${msg.direction}|${msg.rpcPath}`;
        if (seenRPCs.has(key)) continue;
        seenRPCs.add(key);

        // Only deep-analyze unknown RPCs or messages with decode errors
        if (!msg.isUnknown && !(msg.decoded && msg.decoded._error)) continue;

        console.log(`[ANALYZE] ${msg.direction} ${msg.rpcPath}`);

        let messageName = null;
        // Try to determine the expected message type from RPC path
        // Extract last component: /service.Service/Method → Method
        const parts = msg.rpcPath.split('/');
        const method = parts[parts.length - 1];
        if (method && method !== '?') {
            // Try common suffixes
            for (const suffix of ['Request', 'Req', 'Resp', 'Response']) {
                if (protoSchemas[method + suffix]) {
                    messageName = method + suffix;
                    break;
                }
            }
        }

        const rawFields = msg._innerBuf
            ? parseDecodeRaw(protocDecodeRaw(msg._innerBuf))
            : [];

        const { issues, suggestions } = messageName
            ? compareWithSchema(messageName, rawFields, msg._innerBuf)
            : { issues: ['No schema available'], suggestions: [{ type: 'new_message', messageName: method || msg.rpcPath, fields: rawFields.map(f => ({ num: f.num, suggestedType: inferTypeFromWireType(f.wireType, f.val), suggestedName: guessFieldName(f.val, f.wireType), sampleValue: f.val })) }] };

        let llmSuggestions = null;
        if (LLM_ENABLED && suggestions.length > 0) {
            const llmPrompt = buildLLMPrompt(suggestions, messageName || method || msg.rpcPath);
            const llmResponse = await callLLM(llmPrompt);
            if (llmResponse) {
                try {
                    // Extract JSON from response
                    const jsonMatch = llmResponse.match(/\[[\s\S]*\]/);
                    if (jsonMatch) {
                        llmSuggestions = JSON.parse(jsonMatch[0]);
                    }
                } catch (_) {}
            }
        }

        analyses.push({
            rpcPath: msg.rpcPath,
            messageName,
            direction: msg.direction,
            rawDecoded: msg._innerBuf ? protocDecodeRaw(msg._innerBuf) : '',
            issues,
            suggestions,
            llmSuggestions,
        });
    }

    // Generate report
    const report = generateReport(sessionData, analyses);
    const reportDir = sessionSource.type === 'text' ? path.dirname(sessionSource.path) : sessionSource.path;
    const reportPath = path.join(reportDir, REPORT_FILE);
    fs.writeFileSync(reportPath, report);
    console.log(`\n[DONE] Report written to: ${reportPath}`);
    console.log(`  Unknown RPCs: ${sessionData.filter(m => m.isUnknown).length}`);
    console.log(`  Analyzed: ${analyses.length}`);

    // Print summary to console
    console.log('\n--- Quick Summary ---');
    for (const a of analyses) {
        console.log(`  ${a.direction} ${a.rpcPath}`);
        for (const s of a.suggestions) {
            console.log(`    ${s.type}: field #${s.fieldNum || '?'} → ${s.suggestedType || '?'} ${s.suggestedName || ''}`);
        }
    }
}

main().catch(e => {
    console.error('[FATAL]', e);
    process.exit(1);
});
