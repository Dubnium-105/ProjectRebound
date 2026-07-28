package metaserver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	metaprotocol "github.com/projectrebound/matchserver/internal/metaserver/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func (s *TCPServer) getPlayerArchive(
	ctx context.Context,
	session GateSession,
	message []byte,
) ([]byte, error) {
	var request metaprotocol.GetPlayerArchiveV2Request
	if err := proto.Unmarshal(message, &request); err != nil {
		return nil, invalid(map[string]any{"message": "invalid player archive request"})
	}
	loadouts, err := s.service.ListLoadouts(ctx, session.PlayerID)
	if err != nil {
		return nil, err
	}
	byRole := make(map[string]Loadout, len(loadouts))
	for _, loadout := range loadouts {
		byRole[loadout.RoleID] = loadout
	}
	roleIDs := request.GetRoleIds()
	if len(roleIDs) == 0 {
		roleIDs = make([]string, 0, len(loadouts))
		for _, loadout := range loadouts {
			roleIDs = append(roleIDs, loadout.RoleID)
		}
	}
	response := &metaprotocol.GetPlayerArchiveV2Response{
		PlayerRoleDatas: make([]*metaprotocol.PlayerRoleData, 0, len(roleIDs)),
	}
	for _, roleID := range roleIDs {
		if !s.service.definitions.HasRole(roleID) {
			continue
		}
		snapshot := map[string]any{}
		if loadout, ok := byRole[roleID]; ok {
			_ = json.Unmarshal(loadout.Snapshot, &snapshot)
		}
		role := &metaprotocol.PlayerRoleData{
			RoleId:         roleID,
			LeftPylon:      nativeSnapshotItem(snapshot, "leftPylon", 3),
			RightPylon:     nativeSnapshotItem(snapshot, "rightPylon", 4),
			MobilityModule: nativeSnapshotItem(snapshot, "mobilityModule", 6),
			MeleeWeapon:    nativeSnapshotItem(snapshot, "meleeWeapon", 5),
			PrimaryWeapon:  nativeSnapshotItem(snapshot, "primaryWeapon", 1),
			SecondWeapon:   nativeSnapshotItem(snapshot, "secondaryWeapon", 2),
		}
		weaponIDs := make([]string, 0, 2)
		for _, weaponID := range []string{role.SecondWeapon, role.PrimaryWeapon} {
			if weaponID != "None" && weaponID != "" {
				weaponIDs = append(weaponIDs, weaponID)
			}
		}
		archives, err := s.service.repository.GetWeaponArchives(ctx, session.PlayerID, weaponIDs)
		if err != nil {
			return nil, err
		}
		if bundle := nativeWeaponArchiveBundle(roleID, weaponIDs, archives); len(bundle) > 0 {
			value := hex.EncodeToString(bundle)
			role.WeaponArchiveRaw = &value
		}
		if value := nativeSnapshotString(snapshot, "_skinToken", "skinToken"); value != "" {
			role.SkinToken = &value
		}
		if value := nativeSnapshotString(snapshot, "_ornamentId", "ornamentId"); value != "" {
			role.OrnamentId = &value
		}
		response.PlayerRoleDatas = append(response.PlayerRoleDatas, role)
	}
	profile, err := s.service.Profile(ctx, session.PlayerID)
	if err != nil {
		return nil, err
	}
	response.PlayerLevel = int32(min(profile.Level, math.MaxInt32))
	return proto.Marshal(response)
}

func (s *TCPServer) updateRoleArchive(
	ctx context.Context,
	session GateSession,
	message []byte,
) ([]byte, error) {
	var request metaprotocol.UpdateRoleArchiveV2Request
	if err := proto.Unmarshal(message, &request); err != nil {
		return nil, invalid(map[string]any{"message": "invalid role archive update"})
	}
	roleID, itemID := request.GetRoleId(), request.GetItemId()
	if !s.service.definitions.HasRole(roleID) ||
		(itemID != "" && !s.service.definitions.HasItem(itemID)) {
		return nil, invalid(map[string]any{"message": "role or item is absent from pinned definitions"})
	}
	var skin metaprotocol.SkinPayload
	if len(request.GetSkinData()) > 0 {
		if err := proto.Unmarshal(request.GetSkinData(), &skin); err != nil {
			return nil, invalid(map[string]any{"message": "invalid role skin payload"})
		}
	}
	err := s.mutateNativeLoadout(ctx, session.PlayerID, roleID, func(snapshot map[string]any) {
		if skin.GetTokenId() != "" {
			snapshot["_skinToken"] = skin.GetTokenId()
		}
		if skin.GetOrnamentId() != "" {
			snapshot["_ornamentId"] = skin.GetOrnamentId()
		}
		if itemID == "" {
			if key := nativeOperationSlot(request.GetOperation()); key != "" {
				snapshot[key] = "None"
			}
			return
		}
		switch s.service.definitions.ItemType(itemID) {
		case "EPBItemType::Weapon":
			if request.GetOperation() == 2 || request.GetOperation() == 7 {
				snapshot["secondaryWeapon"] = itemID
			} else {
				snapshot["primaryWeapon"] = itemID
			}
		case "EPBItemType::Pod":
			if request.GetOperation() == 3 || request.GetOperation() == 6 || request.GetOperation() == 7 {
				snapshot["rightPylon"] = itemID
			} else {
				snapshot["leftPylon"] = itemID
			}
		case "EPBItemType::Mobility":
			snapshot["mobilityModule"] = itemID
		case "EPBItemType::MeleeWeapon":
			snapshot["meleeWeapon"] = itemID
		}
	})
	if err != nil {
		return nil, err
	}
	return EncodeStatusMessage(0), nil
}

func (s *TCPServer) updateWeaponArchive(
	ctx context.Context,
	session GateSession,
	message []byte,
) ([]byte, error) {
	var request metaprotocol.UpdateWeaponArchiveV2Request
	if err := proto.Unmarshal(message, &request); err != nil {
		return nil, invalid(map[string]any{"message": "invalid weapon archive update"})
	}
	archive := request.GetWeaponArchive()
	if !s.service.definitions.HasRole(request.GetRoleId()) ||
		archive == nil || !s.service.definitions.HasWeapon(archive.GetWeaponId()) {
		return nil, invalid(map[string]any{"message": "role or weapon is absent from pinned definitions"})
	}
	for _, part := range archive.GetParts() {
		if part.GetSlotId() < 0 || !s.service.definitions.HasPart(part.GetPartId()) {
			return nil, invalid(map[string]any{"message": "weapon archive contains an invalid part"})
		}
	}
	raw, err := proto.Marshal(archive)
	if err != nil {
		return nil, internalError(err)
	}
	decoded, err := protojson.MarshalOptions{
		UseProtoNames: true, EmitUnpopulated: true,
	}.Marshal(archive)
	if err != nil {
		return nil, internalError(err)
	}
	if err := s.service.repository.UpsertWeaponArchive(
		ctx, session.PlayerID, archive.GetWeaponId(), raw, decoded,
	); err != nil {
		return nil, err
	}
	return EncodeStatusMessage(0), nil
}

func (s *TCPServer) queryAssets() ([]byte, error) {
	itemIDs := s.service.definitions.ItemIDs()
	response := &metaprotocol.QueryAssetsResponse{
		ItemCount: int32(len(itemIDs)),
		ItemDatas: make([]*metaprotocol.ItemData, 0, len(itemIDs)),
	}
	for _, itemID := range itemIDs {
		response.ItemDatas = append(response.ItemDatas, &metaprotocol.ItemData{
			ItemId: itemID, Unknown_1: 1, Unknown_2: 1, Unknown_3: 1,
		})
	}
	return proto.Marshal(response)
}

func (s *TCPServer) queryCurrency(
	ctx context.Context,
	session GateSession,
) ([]byte, error) {
	profile, err := s.service.Profile(ctx, session.PlayerID)
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	_ = json.Unmarshal(profile.Currencies, &values)
	response := &metaprotocol.QueryCurrencyResponse{
		CurrencyA: nativeCurrency(values, "currency_a", "CurrencyA", "a"),
		CurrencyB: nativeCurrency(values, "currency_b", "CurrencyB", "b"),
		CurrencyC: nativeCurrency(values, "currency_c", "CurrencyC", "c"),
		CurrencyD: nativeCurrency(values, "currency_d", "CurrencyD", "d"),
		CurrencyE: nativeCurrency(values, "currency_e", "CurrencyE", "e"),
	}
	return proto.Marshal(response)
}

func (s *TCPServer) queryNotifications(
	ctx context.Context,
	message []byte,
) ([]byte, error) {
	var request metaprotocol.QueryNotificationRequest
	if err := proto.Unmarshal(message, &request); err != nil {
		return nil, invalid(map[string]any{"message": "invalid notification query"})
	}
	locale := strings.TrimSpace(request.GetLanguageCode())
	items, err := s.service.Notifications(ctx, locale)
	if err != nil {
		return nil, err
	}
	response := &metaprotocol.QueryNotificationResponse{
		Notifications: make([]*metaprotocol.NotificationEntity, 0, len(items)),
	}
	for _, item := range items {
		response.Notifications = append(response.Notifications, &metaprotocol.NotificationEntity{
			Id: item.ID, Title: item.Title, Content: item.Body,
			LanguageCode: item.Locale, Platform: request.GetPlatform(), Timezone: "UTC",
		})
	}
	return proto.Marshal(response)
}

func (s *TCPServer) queryPartyPresence(
	ctx context.Context,
	session GateSession,
) ([]byte, error) {
	partyID := activePartyID(ctx, s.service.repository, session.PlayerID)
	response := &metaprotocol.QueryPartyPresenceResponse{StatusCode: 0}
	if partyID == "" {
		return proto.Marshal(response)
	}
	party, err := s.service.GetParty(ctx, partyID, session.PlayerID)
	if err != nil {
		return nil, err
	}
	for _, member := range party.Members {
		response.PartyMembers = append(response.PartyMembers, &metaprotocol.PartyMemberPresence{
			UserId: member.PlayerID, Status: nativePresence(member.Presence, party.State),
		})
	}
	return proto.Marshal(response)
}

func (s *TCPServer) mutateNativeLoadout(
	ctx context.Context,
	playerID, roleID string,
	mutate func(map[string]any),
) error {
	for range 3 {
		revision := int64(0)
		snapshot := map[string]any{
			"roleId": roleID, "primaryWeapon": "None", "secondaryWeapon": "None",
			"leftPylon": "None", "rightPylon": "None", "mobilityModule": "None",
			"meleeWeapon": "None",
		}
		current, err := s.service.repository.GetLoadout(ctx, playerID, roleID)
		if err == nil {
			revision = current.Revision
			if err := json.Unmarshal(current.Snapshot, &snapshot); err != nil {
				return internalError(err)
			}
		} else {
			var serviceErr *ServiceError
			if !errors.As(err, &serviceErr) || serviceErr.Code != "META_LOADOUT_NOT_FOUND" {
				return err
			}
		}
		mutate(snapshot)
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return internalError(err)
		}
		if _, err := s.service.PutLoadout(ctx, playerID, roleID, raw, revision); err != nil {
			var serviceErr *ServiceError
			if errors.As(err, &serviceErr) && serviceErr.Code == "META_LOADOUT_REVISION_CONFLICT" {
				continue
			}
			return err
		}
		return nil
	}
	return conflict("META_LOADOUT_REVISION_CONFLICT", "The loadout was updated concurrently.")
}

func nativeSnapshotItem(snapshot map[string]any, key string, slot int) string {
	if value := nativeSnapshotString(snapshot, key); value != "" {
		return value
	}
	inventory, _ := snapshot["inventory"].(map[string]any)
	slots, _ := inventory["slots"].([]any)
	for _, value := range slots {
		entry, _ := value.(map[string]any)
		slotValue, _ := entry["slotType"].(float64)
		if int(slotValue) == slot {
			if itemID, _ := entry["itemId"].(string); itemID != "" {
				return itemID
			}
		}
	}
	return "None"
}

func nativeSnapshotString(snapshot map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := snapshot[key].(type) {
		case string:
			if value != "" {
				return value
			}
		case map[string]any:
			if id, ok := definitionID(value); ok {
				return id
			}
		}
	}
	return ""
}

func nativeOperationSlot(operation int32) string {
	switch operation {
	case 2:
		return "secondaryWeapon"
	case 3:
		return "rightPylon"
	case 4:
		return "mobilityModule"
	case 5:
		return "meleeWeapon"
	case 6:
		return "primaryWeapon"
	case 7:
		return "secondaryWeapon"
	default:
		return "primaryWeapon"
	}
}

func nativeWeaponArchiveBundle(
	roleID string,
	weaponIDs []string,
	archives map[string][]byte,
) []byte {
	output := protowire.AppendTag(nil, 1, protowire.BytesType)
	output = protowire.AppendString(output, roleID)
	appended := false
	for _, weaponID := range weaponIDs {
		raw := archives[weaponID]
		if len(raw) == 0 {
			continue
		}
		output = protowire.AppendTag(output, 3, protowire.BytesType)
		output = protowire.AppendBytes(output, raw)
		appended = true
	}
	if !appended {
		return nil
	}
	return output
}

func nativeCurrency(values map[string]any, keys ...string) int32 {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return int32(min(max(value, 0), math.MaxInt32))
		case json.Number:
			parsed, _ := value.Int64()
			return int32(min(max(parsed, int64(0)), int64(math.MaxInt32)))
		}
	}
	return 0
}

func nativePresence(presence, partyState string) string {
	if partyState == "MATCHMAKING" {
		return "InMatching"
	}
	switch presence {
	case "IN_GAME":
		return "InGame"
	case "AWAY":
		return "Away"
	case "OFFLINE":
		return "Offline"
	default:
		return "Online"
	}
}

func normalizedNativePresence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ingame", "in_game":
		return "IN_GAME"
	case "away":
		return "AWAY"
	case "offline":
		return "OFFLINE"
	default:
		return "ONLINE"
	}
}

func nativeIdentityMatches(message []byte, playerID string) error {
	var request metaprotocol.StartMatchmakingRequest
	if err := proto.Unmarshal(message, &request); err != nil {
		return err
	}
	if claimed := request.GetPayload().GetMatchmakingRequestorUserId(); claimed != "" && claimed != playerID {
		return fmt.Errorf("matchmaking player identity mismatch")
	}
	return nil
}
