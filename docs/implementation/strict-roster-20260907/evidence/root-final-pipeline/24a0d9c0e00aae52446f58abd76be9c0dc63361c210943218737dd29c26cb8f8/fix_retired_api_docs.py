from pathlib import Path
import re

repo = Path('C:/wksp/ProjectRebound')
path = repo/'Backend/api/openapi/openapi.yaml'
text = path.read_text(encoding='utf-8')
start = text.index('  /v1/p2p-rooms:\n')
end = text.index('  /v1/connections:\n', start)
old = text[start:end]
blocks = re.split(r'(?=^  /v1/)', old, flags=re.M)
new = []
for block in filter(None, blocks):
    route = block.splitlines()[0][2:-1]
    new.append(f'  {route}:')
    parameters = re.findall(r'\{([^}]+)\}', route)
    if parameters:
        new.append('    parameters:')
        for parameter in parameters:
            new.extend(['      - in: path', f'        name: {parameter}', '        required: true', '        schema: {type: string}'])
    for method, op_id in re.findall(r'^    (get|post|put|delete):\n      operationId: (\w+)', block, re.M):
        public = method == 'get' and route in ('/v1/p2p-rooms', '/v1/p2p-rooms/{room_id}')
        code = 'ONLINE_MATCH_ROUTE_RETIRED' if '/matches/' in route or '/p2p-matches/' in route else 'ONLINE_P2P_ROOM_RETIRED'
        new.extend([f'    {method}:', f'      operationId: {op_id}', '      tags: [Retired Online Routes]', '      deprecated: true',
                    '      description: Standalone online admission and lifecycle are retired. Use MatchLobby and MatchAttempt.'])
        if not public:
            new.append('      security: [{playerAccessToken: []}]')
        new.extend(['      responses:', '        "410":', f'          description: {code}. No room, grant, transport secret, or match state is issued or mutated.',
                    '          content: {application/json: {schema: {$ref: "#/components/schemas/ErrorResponse"}}}'])
        if not public:
            new.extend(['        "401": {$ref: "#/components/responses/Unauthorized"}', '        "403": {$ref: "#/components/responses/Forbidden"}'])
text = text[:start]+'\n'.join(new)+'\n'+text[end:]
text = text.replace('Idempotently creates a host-to-peer session. Joining a P2P room also creates this session automatically.',
                    'Coordinates a host-to-peer transport session within an authoritative attempt. It does not grant native admission or create an independent online room.')
path.write_bytes(text.replace('\n','\r\n').encode())

path = repo/'docs/api/external.md'
text = path.read_text(encoding='utf-8')
start = text.index('### 3.4 P2P Room\n')
end = text.index('### 3.5 ', start)
section = text[start:end].splitlines()
replacement = ['### 3.4 Retired standalone P2P room routes', '',
    'These routes return `410 ONLINE_P2P_ROOM_RETIRED` after any required player authentication. They do not create, join, start, or renew online rooms. Use the authoritative match lobby and attempt flow below.', '',
    '| Method | Path | Response |', '| --- | --- | --- |']
for line in section:
    if line.startswith('| ') and '`/v1/' in line:
        columns = [v.strip() for v in line.split('|')]
        replacement.append(f'| {columns[1]} | {columns[2]} | 410 retired |')
text = text[:start]+'\n'.join(replacement)+'\n\n'+text[end:]
start = text.index('### 3.6 P2P BattleLog v3\n')
end = text.index('### 3.7 ', start)
section = text[start:end].splitlines()
replacement = ['### 3.6 Retired independent P2P match routes', '',
    'These routes return `410 ONLINE_MATCH_ROUTE_RETIRED` after active, Steam-verified player authentication. MatchAttempt exclusively owns the frozen roster, native admission, and lifecycle. Existing carrier projections remain internal records.', '',
    '| Method | Path | Response |', '| --- | --- | --- |']
for line in section:
    if line.startswith('| ') and '`/v1/' in line:
        columns = [v.strip() for v in line.split('|')]
        replacement.append(f'| {columns[1]} | {columns[2]} | 410 retired |')
text = text[:start]+'\n'.join(replacement)+'\n\n'+text[end:]
for line in text.splitlines():
    if line.startswith('| ') and '`/v1/p2p-rooms/{room_id}/vnt/' in line:
        columns = [v.strip() for v in line.split('|')]
        text = text.replace(line, f'| {columns[1]} | {columns[2]} | Active, Steam-verified player | 410 `ONLINE_P2P_ROOM_RETIRED`; use the scoped MatchAttempt transport flow |')
text = text.replace('3. VNT room `network_token` and `e2e_password` are never issued to a node owner or public directory client. Only an active member of a VNT room receives the current generation through `POST /v1/p2p-rooms/{room_id}/vnt/bootstrap`, and the response must remain in memory with `Cache-Control: no-store` handling.',
                    '3. Managed VNT `network_token` and `e2e_password` are delivered only through the scoped MatchAttempt transport flow and remain in core memory with `Cache-Control: no-store` handling. The standalone room bootstrap endpoint is retired; node enrollment does not grant access to match transport secrets.')
text = text.replace('The feature is omitted from normal use unless `/v1/client/config` returns `features.strict_roster_v1=true`; the server requires an explicit Ed25519 key and the locked game SHA-256 before that flag can be enabled.',
                    'Strict roster admission is mandatory for online matches. The retired `strict_roster_v1_enabled` configuration is rejected; `match_lobby.accept_new_lobbies` controls creation only and cannot relax an existing attempt. The client capability and signed artifact compatibility gates must also be satisfied before online use.')
text = text.replace('Signed allocation/grant staging and scoped transactions exist, but client NMT_Login Grant injection, authenticated native identity, and field correspondence still require proof.',
                    'Signed allocation/grant staging and scoped transactions exist. One real client has completed native admission and disconnect, while complete Playable, multi-player, rejection, recovery, and next-match acceptance remains incomplete.')
path.write_bytes(text.replace('\n','\r\n').encode())
print('Updated 17 retired operations and the public reference descriptions.')
