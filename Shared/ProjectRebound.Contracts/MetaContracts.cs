using System.Text.Json;

namespace ProjectRebound.Contracts;

public sealed record MetaEndpoint(string Protocol, string Host, int Port);

public sealed record MetaRegion(
    string Id,
    string Name,
    IReadOnlyList<MetaEndpoint> QosEndpoints);

public sealed record MetaPlaylist(
    string Id,
    string Slug,
    string DisplayName,
    string Description,
    string Mode,
    JsonElement Definition,
    int SortOrder,
    DateTimeOffset UpdatedAt);

public sealed record MetaNotification(
    string Id,
    string Title,
    string Body,
    string Locale,
    int Priority,
    DateTimeOffset? StartsAt,
    DateTimeOffset? EndsAt,
    DateTimeOffset UpdatedAt);

public sealed record CreateMetaSessionRequest(
    string ClientVersion,
    int ProtocolVersion,
    string? Platform = null);

public sealed record MetaSession(
    string UserId,
    string GateTicket,
    string Endpoint,
    int ExpiresInSeconds,
    int ProtocolVersion);

public sealed record MetaProfile(
    string PlayerId,
    int Level,
    long Experience,
    JsonElement Currencies,
    JsonElement Statistics,
    long Revision,
    DateTimeOffset CreatedAt,
    DateTimeOffset UpdatedAt);

public sealed record MetaLoadout(
    string? PlayerId,
    string RoleId,
    JsonElement Snapshot,
    long Revision,
    DateTimeOffset UpdatedAt);

public sealed record UpdateMetaLoadoutRequest(JsonElement Snapshot, long Revision);

public sealed record CreateMetaPartyRequest(
    string Mode,
    string Region,
    string ClientVersion);

public sealed record MetaPartyMember(
    string PlayerId,
    string Role,
    bool Ready,
    string Presence,
    DateTimeOffset JoinedAt,
    DateTimeOffset UpdatedAt);

public sealed record MetaParty(
    string Id,
    string LeaderPlayerId,
    string State,
    string Mode,
    string Region,
    string ClientVersion,
    int ProtocolVersion,
    long Revision,
    IReadOnlyList<MetaPartyMember> Members,
    DateTimeOffset CreatedAt,
    DateTimeOffset UpdatedAt);

public sealed record SetMetaPartyReadyRequest(bool Ready);
public sealed record SetMetaPartyPresenceRequest(string Presence);

public sealed record CreateMetaMatchTicketRequest(
    string? PartyId,
    string Mode,
    string Region,
    string ClientVersion);

public sealed record MetaMatchTicket(
    string Id,
    string? PlayerId,
    string? PartyId,
    string Mode,
    string Region,
    string ClientVersion,
    int ProtocolVersion,
    string State,
    string? FailureCode,
    string? MatchId,
    string? Endpoint,
    DateTimeOffset ExpiresAt,
    DateTimeOffset CreatedAt,
    DateTimeOffset UpdatedAt,
    DateTimeOffset? CompletedAt);

public sealed record MetaMatchPlayerLoadout(
    string MatchId,
    string PlayerId,
    IReadOnlyList<MetaLoadout> Loadouts);

public sealed record CompleteMetaMatchRequest(JsonElement Result);

public sealed record AdminMetaOverview(
    long Profiles,
    long ActiveParties,
    long QueuedTickets,
    long ActiveMatches);

public sealed record AdminMetaLoadoutUpdateRequest(
    JsonElement Snapshot,
    long Revision,
    string Reason);

public sealed record AdminMetaPlaylistUpsertRequest(
    string DisplayName,
    string Description,
    string Mode,
    JsonElement Definition,
    bool Enabled,
    int SortOrder,
    string Reason);

public sealed record AdminMetaNotificationUpsertRequest(
    string Title,
    string Body,
    string Locale,
    int Priority,
    bool Enabled,
    DateTimeOffset? StartsAt,
    DateTimeOffset? EndsAt,
    string Reason);
