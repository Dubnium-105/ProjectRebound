from pathlib import Path
import re

root=Path('C:/wksp/ProjectRebound/Backend/internal/metaserver')
def replace_function(path, marker, body):
    text=path.read_text(encoding='utf-8')
    start=text.index(marker)
    next_declaration=re.search(r'\n(?:func|type|const|var) ',text[start+5:])
    end=start+5+next_declaration.start() if next_declaration else len(text)
    text=text[:start]+body.rstrip()+'\n'+text[end:]
    path.write_text(text,encoding='utf-8',newline='\n')

service=root/'service.go'
replace_function(service,'func (s *Service) CreateMatchTicket(','''func (s *Service) CreateMatchTicket(
    ctx context.Context,
    playerID, partyID, mode, region, clientVersion string,
) (MatchTicket, error) {
    return MatchTicket{}, retiredMatchmaking()
}''')
repository=root/'repository.go'
for marker,body in [
('func (r *Repository) CreateTicket(','''func (r *Repository) CreateTicket(
    ctx context.Context, playerID, partyID, mode, region, clientVersion string,
    protocolVersion int, ttl time.Duration,
) (MatchTicket, error) {
    return MatchTicket{}, retiredMatchmaking()
}'''),
('func (r *Repository) GetTicket(','''func (r *Repository) GetTicket(ctx context.Context, ticketID, playerID string) (MatchTicket, error) {
    return MatchTicket{}, retiredMatchmaking()
}'''),
('func (r *Repository) CancelTicket(','''func (r *Repository) CancelTicket(ctx context.Context, ticketID, playerID string) error {
    return retiredMatchmaking()
}'''),
('func (r *Repository) MarkMatchPlayerConnected(','''func (r *Repository) MarkMatchPlayerConnected(
    ctx context.Context, principal GameServerPrincipal, matchID, playerID string,
) error {
    return retiredMatchmaking()
}'''),
('func (r *Repository) CompleteMatch(','''func (r *Repository) CompleteMatch(
    ctx context.Context, principal GameServerPrincipal, matchID string, result json.RawMessage,
) error {
    return retiredMatchmaking()
}''')]: replace_function(repository,marker,body)
text=repository.read_text()
old="AND match.state IN ('RESERVED', 'RUNNING')"
assert text.count(old)==1
text=text.replace(old,old+'\n              AND match.match_attempt_id IS NOT NULL')
repository.write_text(text,encoding='utf-8',newline='\n')
for name in ['CreateMatchTicket','GetMatchTicket','CancelMatchTicket','InternalConnected','InternalCompleted']:
    replace_function(root/'http.go',f'func (h *HTTPHandler) {name}(',f'''func (h *HTTPHandler) {name}(w http.ResponseWriter, r *http.Request) {{
    h.writeError(w, r, retiredMatchmaking())
}}''')
replace_function(root/'admin.go','func (h *MetaAdminHandler) CancelMatch(','''func (h *MetaAdminHandler) CancelMatch(w http.ResponseWriter, r *http.Request) {
    h.writeError(w, r, retiredMatchmaking())
}''')
server=root/'server.go'
text=server.read_text().replace('\n\t"sync"','')
text=re.sub(r'\n\tscheduler \*Scheduler\n','\n',text)
start=text.index('\n\t\tscheduler: NewScheduler(')
end=text.index('\n\t}, nil',start)
text=text[:start]+text[end:]
start=text.index('\n\tvar background sync.WaitGroup')
end=text.index('\n\terrorsCh :=',start)
text=text[:start]+text[end:]
text=text.replace('\n\t\tbackground.Wait()','')
server.write_text(text,encoding='utf-8',newline='\n')
tcp=root/'tcp.go'
text=tcp.read_text()
start=text.index('\tcase "/matchmaking.Matchmaking/StartUnityMatchmaking":')
end=text.index('\tcase "/profile.Profile/QueryCurrency"',start)
text=text[:start]+'''    case "/matchmaking.Matchmaking/StartUnityMatchmaking",
        "/matchmaking.Matchmaking/QueryUnityMatchmaking",
        "/matchmaking.Matchmaking/StopUnityMatchmaking":
        // Native online matchmaking is exclusively initiated by MatchLobby.
        // Preserve the known nonzero RPC failure envelope, never an empty
        // success or a compatibility ticket that can reserve a Game Server.
        response.ErrorCode = rpcUnknownError
        response.Message = EncodeStatusMessage(rpcUnknownError)
'''+text[end:]
tcp.write_text(text,encoding='utf-8',newline='\n')

# Keep real loadout/archive isolation/concurrency coverage; replace the old
# successful scheduler assertions with immutable-state retirement assertions.
test=root/'repository_integration_test.go'
text=test.read_text().replace('\n\t"io"','').replace('\n\t"log/slog"','')
text=text.replace('TestRepositoryIsolationAndConcurrentSchedulingAgainstPostgreSQL','TestRepositoryIsolationAndRetiredMatchmakingAgainstPostgreSQL')
start=text.index('\n\tpartyTicket, err := repository.CreateTicket(')
end=text.index('\n}\n\nfunc metaErrorCode',start)
text=text[:start]+'''
    if _, err := repository.CreateTicket(ctx, playerIDs[0], party.ID, "default", "hgh", "1.1.0", 1, time.Minute); metaErrorCode(err) != "META_MATCHMAKING_RETIRED" {
        t.Fatalf("legacy creation did not reject: %v", err)
    }
    if _, err := repository.GetTicket(ctx, "old-ticket", playerIDs[0]); metaErrorCode(err) != "META_MATCHMAKING_RETIRED" {
        t.Fatalf("legacy polling did not reject: %v", err)
    }
    if err := repository.CancelTicket(ctx, "old-ticket", playerIDs[0]); metaErrorCode(err) != "META_MATCHMAKING_RETIRED" {
        t.Fatalf("legacy cancellation did not reject: %v", err)
    }
    principal := GameServerPrincipal{ServerID:serverIDs[0]}
    if err := repository.MarkMatchPlayerConnected(ctx, principal, "old-match", playerIDs[0]); metaErrorCode(err) != "META_MATCHMAKING_RETIRED" {
        t.Fatalf("legacy connected write did not reject: %v", err)
    }
    if err := repository.CompleteMatch(ctx, principal, "old-match", nil); metaErrorCode(err) != "META_MATCHMAKING_RETIRED" {
        t.Fatalf("legacy completion did not reject: %v", err)
    }
    var tickets, matches, readyServers int
    if err := pool.QueryRow(ctx, `SELECT
        (SELECT count(*) FROM meta_match_tickets WHERE player_id = ANY($1)),
        (SELECT count(*) FROM meta_matches WHERE game_server_id = ANY($2)),
        (SELECT count(*) FROM game_servers WHERE id = ANY($2) AND state = 'READY')`,
        playerIDs,serverIDs).Scan(&tickets,&matches,&readyServers); err != nil {
        t.Fatal(err)
    }
    if tickets != 0 || matches != 0 || readyServers != 2 {
        t.Fatalf("retired paths changed database state: tickets=%d matches=%d ready=%d",tickets,matches,readyServers)
    }
    t.Log("retired Meta ticket/connected/completed calls rejected; zero tickets, zero allocations, original server states intact")
'''+text[end:]
test.write_text(text,encoding='utf-8',newline='\n')
print('Retired old Meta writers and scheduler wiring; preserved profile/archive code and replaced incompatible legacy scheduling tests')
