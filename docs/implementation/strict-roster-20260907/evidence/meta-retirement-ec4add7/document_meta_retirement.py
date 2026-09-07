from pathlib import Path
import re

repo=Path('C:/wksp/ProjectRebound')
path=repo/'Backend/api/openapi/openapi.yaml'
text=path.read_text(encoding='utf-8')
paths=[
 '/v1/meta/matchmaking/tickets',
 '/v1/meta/matchmaking/tickets/{ticket_id}',
 '/internal/v1/meta/matches/{match_id}/players/{player_id}/connected',
 '/internal/v1/meta/matches/{match_id}/completed',
 '/v1/admin/meta/matches/{match_id}/cancel',
]
for route in paths:
    start=text.index('  '+route+':\n')
    match=re.search(r'\n  /',text[start+3:])
    end=start+3+match.start()+1 if match else len(text)
    block=text[start:end]
    block=re.sub(r'(      operationId: [^\n]+\n)',r'\1      deprecated: true\n',block)
    block=re.sub(r'      description: [^\n]+\n','',block)
    # All legacy positive/error result variants are replaced by the explicit
    # retirement response. Existing authentication and path metadata remain.
    block=re.sub(r'      responses:\n.*?(?=    (?:get|post|delete):\n|\Z)',
        '      description: Retired. MatchLobby and MatchAttempt exclusively own online admission and lifecycle.\n'
        '      responses:\n'
        '        "410": {description: "META_MATCHMAKING_RETIRED; no database or lifecycle mutation."}\n'
        '        "401": {$ref: "#/components/responses/Unauthorized"}\n',block,flags=re.S)
    text=text[:start]+block+text[end:]
text=text.replace('description: Requires meta.loadouts.read scope and an active match assigned to this Game Server.',
                  'description: Requires meta.loadouts.read scope and an active MatchAttempt projection assigned to this Game Server; legacy standalone Meta matches are rejected.')
path.write_text(text,encoding='utf-8',newline='\n')
for language in ['zh-CN','en']:
    filename='auth-permission-matrix.zh-CN.md' if language=='zh-CN' else 'auth-permission-matrix.md'
    p=repo/'Backend/api/openapi'/filename
    text=p.read_text(encoding='utf-8')
    note=('在线匹配只通过 MatchLobby/MatchAttempt。Meta 独立匹配 ticket、原生 Start/Query/StopUnityMatchmaking、旧 connected/completed 和管理员 Meta cancel 已退役；HTTP 返回 410，原生 RPC 返回非零错误。Meta 不再运行独立调度器或修改 Game Server lifecycle。'
          if language=='zh-CN' else
          'Online matchmaking exclusively uses MatchLobby/MatchAttempt. Independent Meta tickets, native Start/Query/StopUnityMatchmaking, legacy connected/completed and administrator Meta cancel are retired: HTTP returns 410 and native RPC returns a nonzero error. Meta runs no independent scheduler and cannot change Game Server lifecycle.')
    first=text.index('\n')+1
    text=text[:first]+'\n'+note+'\n'+text[first:]
    if language=='zh-CN':
        text=text.replace('Meta 配装、Party、Gate 和匹配写操作','Meta 配装、Party 和 Gate 写操作')
        text=text.replace('房间、连接、MetaServer session、Party、配装和匹配操作','MatchLobby 在线操作、连接、MetaServer session、Party 和配装操作')
    p.write_text(text,encoding='utf-8',newline='\n')
print('Documented retired Meta endpoints and strict loadout projection requirement')
