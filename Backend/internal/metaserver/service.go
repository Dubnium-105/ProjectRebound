package metaserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var metaLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Service struct {
	repository       *Repository
	gates            *GateStore
	protocolVersion  int
	logicEndpoint    string
	matchTicketTTL   time.Duration
	relayFreshness   time.Duration
	maxSnapshotBytes int
	definitions      *DefinitionIndex
}

func NewService(
	repository *Repository,
	gates *GateStore,
	protocolVersion int,
	logicEndpoint string,
	matchTicketTTL, relayFreshness time.Duration,
	maxSnapshotBytes int,
	definitions *DefinitionIndex,
) *Service {
	return &Service{
		repository: repository, gates: gates, protocolVersion: protocolVersion,
		logicEndpoint: logicEndpoint, matchTicketTTL: matchTicketTTL,
		relayFreshness: relayFreshness, maxSnapshotBytes: maxSnapshotBytes,
		definitions: definitions,
	}
}

func (s *Service) IssueSession(
	ctx context.Context,
	playerID, authSessionID, clientVersion string,
	protocolVersion int,
) (string, error) {
	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" || !metaLabelPattern.MatchString(clientVersion) {
		return "", invalid(map[string]any{"client_version": "is required and contains unsupported characters"})
	}
	if protocolVersion != s.protocolVersion {
		return "", &ServiceError{
			Status: 409, Code: "META_PROTOCOL_VERSION_UNSUPPORTED",
			Message: "The client protocol version is not supported.",
			Details: map[string]any{"supported_protocol_version": s.protocolVersion},
		}
	}
	return s.gates.Issue(ctx, GateSession{
		PlayerID: playerID, AuthSessionID: authSessionID,
		ClientVersion: clientVersion, ProtocolVersion: protocolVersion,
	})
}

func (s *Service) Profile(ctx context.Context, playerID string) (Profile, error) {
	item, err := s.repository.GetOrCreateProfile(ctx, playerID)
	if err != nil {
		return Profile{}, internalError(err)
	}
	return item, nil
}

func (s *Service) ListLoadouts(ctx context.Context, playerID string) ([]Loadout, error) {
	items, err := s.repository.ListLoadouts(ctx, playerID)
	if err != nil {
		return nil, internalError(err)
	}
	return items, nil
}

func (s *Service) GetLoadout(ctx context.Context, playerID, roleID string) (Loadout, error) {
	if !metaLabelPattern.MatchString(roleID) {
		return Loadout{}, invalid(map[string]any{"role_id": "is invalid"})
	}
	if s.definitions == nil || !s.definitions.HasRole(roleID) {
		return Loadout{}, invalid(map[string]any{"role_id": "is not present in the pinned definition set"})
	}
	return s.repository.GetLoadout(ctx, playerID, roleID)
}

func (s *Service) PutLoadout(
	ctx context.Context,
	playerID, roleID string,
	snapshot json.RawMessage,
	revision int64,
) (Loadout, error) {
	canonical, digest, err := s.prepareLoadout(roleID, snapshot, revision)
	if err != nil {
		return Loadout{}, err
	}
	return s.repository.PutLoadout(
		ctx, playerID, roleID, canonical, digest, revision,
	)
}

func (s *Service) prepareLoadout(
	roleID string,
	snapshot json.RawMessage,
	revision int64,
) (json.RawMessage, []byte, error) {
	if !metaLabelPattern.MatchString(roleID) {
		return nil, nil, invalid(map[string]any{"role_id": "is invalid"})
	}
	if s.definitions == nil || !s.definitions.HasRole(roleID) {
		return nil, nil, invalid(map[string]any{"role_id": "is not present in the pinned definition set"})
	}
	if revision < 0 {
		return nil, nil, invalid(map[string]any{"revision": "must not be negative"})
	}
	if len(snapshot) == 0 || len(snapshot) > s.maxSnapshotBytes {
		return nil, nil, invalid(map[string]any{"snapshot": "must be a JSON object within the configured size limit"})
	}
	var object map[string]any
	if err := json.Unmarshal(snapshot, &object); err != nil || object == nil {
		return nil, nil, invalid(map[string]any{"snapshot": "must be a JSON object"})
	}
	if err := s.validateLoadoutSnapshot(roleID, object); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, nil, internalError(err)
	}
	digest := sha256.Sum256(canonical)
	return canonical, digest[:], nil
}

func (s *Service) CreateParty(
	ctx context.Context,
	playerID, mode, region, clientVersion string,
) (Party, error) {
	mode, region = normalizeQueueLabels(mode, region)
	if !metaLabelPattern.MatchString(mode) || !metaLabelPattern.MatchString(region) {
		return Party{}, invalid(map[string]any{"mode": "invalid mode or region"})
	}
	return s.repository.CreateParty(ctx, playerID, mode, region, clientVersion, s.protocolVersion)
}

func (s *Service) GetParty(ctx context.Context, partyID, playerID string) (Party, error) {
	allowed, err := s.repository.IsActivePartyMember(ctx, partyID, playerID)
	if err != nil {
		return Party{}, internalError(err)
	}
	if !allowed {
		return Party{}, notFound("META_PARTY_NOT_FOUND", "Party not found.")
	}
	return s.repository.GetParty(ctx, partyID)
}

func (s *Service) SetReady(ctx context.Context, partyID, playerID string, ready bool) (Party, error) {
	return s.repository.UpdatePartyMember(ctx, partyID, playerID, &ready, "")
}

func (s *Service) SetPresence(ctx context.Context, partyID, playerID, presence string) (Party, error) {
	presence = strings.ToUpper(strings.TrimSpace(presence))
	switch presence {
	case "ONLINE", "AWAY", "IN_GAME", "OFFLINE":
	default:
		return Party{}, invalid(map[string]any{"presence": "must be ONLINE, AWAY, IN_GAME, or OFFLINE"})
	}
	return s.repository.UpdatePartyMember(ctx, partyID, playerID, nil, presence)
}

func (s *Service) CreateMatchTicket(
	ctx context.Context,
	playerID, partyID, mode, region, clientVersion string,
) (MatchTicket, error) {
	mode, region = normalizeQueueLabels(mode, region)
	if !metaLabelPattern.MatchString(mode) || !metaLabelPattern.MatchString(region) ||
		!metaLabelPattern.MatchString(clientVersion) {
		return MatchTicket{}, invalid(map[string]any{"queue": "mode, region, or client version is invalid"})
	}
	if partyID != "" {
		leader, err := s.repository.IsPartyLeader(ctx, partyID, playerID)
		if err != nil {
			return MatchTicket{}, internalError(err)
		}
		if !leader {
			return MatchTicket{}, forbidden("META_PARTY_LEADER_REQUIRED", "Only the party leader can start matchmaking.")
		}
	}
	return s.repository.CreateTicket(
		ctx, playerID, partyID, mode, region, clientVersion,
		s.protocolVersion, s.matchTicketTTL,
	)
}

func normalizeQueueLabels(mode, region string) (string, string) {
	mode = strings.TrimSpace(mode)
	region = strings.TrimSpace(region)
	if mode == "" {
		mode = "default"
	}
	if region == "" {
		region = "auto"
	}
	return mode, region
}

func (s *Service) Regions(ctx context.Context) ([]Region, error) {
	items, err := s.repository.ListRegions(ctx, s.relayFreshness)
	if err != nil {
		return nil, internalError(err)
	}
	return items, nil
}

func (s *Service) Playlists(ctx context.Context) ([]Playlist, error) {
	items, err := s.repository.ListPlaylists(ctx)
	if err != nil {
		return nil, internalError(err)
	}
	return items, nil
}

func (s *Service) Notifications(ctx context.Context, locale string) ([]Notification, error) {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		locale = "en"
	}
	if len(locale) > 16 {
		return nil, invalid(map[string]any{"locale": "is too long"})
	}
	items, err := s.repository.ListNotifications(ctx, locale)
	if err != nil {
		return nil, internalError(err)
	}
	return items, nil
}

func (s *Service) LogicEndpoint() string { return s.logicEndpoint }
func (s *Service) ProtocolVersion() int  { return s.protocolVersion }
func (s *Service) GateTicketTTL() time.Duration {
	return s.gates.TTL()
}

func (s *Service) validateLoadoutSnapshot(roleID string, snapshot map[string]any) error {
	if err := s.definitions.ValidateLoadoutSnapshot(roleID, snapshot); err != nil {
		return invalid(map[string]any{"snapshot": err.Error()})
	}
	return nil
}

func definitionID(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, typed != ""
	case map[string]any:
		for _, key := range []string{"itemId", "ItemId", "item_id", "weaponId", "WeaponId", "partId", "PartId", "id", "Id"} {
			if id, ok := typed[key].(string); ok && id != "" {
				return id, true
			}
		}
	}
	return "", false
}
