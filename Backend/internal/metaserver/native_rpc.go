package metaserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	metaprotocol "github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver/protocol"
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
	if err := s.ensureNativeDefaultLoadouts(ctx, session.PlayerID); err != nil {
		return nil, err
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
	for _, requestedRoleID := range roleIDs {
		roleID, ok := s.service.definitions.CanonicalRoleID(requestedRoleID)
		if !ok {
			continue
		}
		snapshot := map[string]any{}
		if loadout, ok := byRole[roleID]; ok {
			_ = json.Unmarshal(loadout.Snapshot, &snapshot)
		}
		role := &metaprotocol.PlayerRoleData{
			RoleId:         requestedRoleID,
			LeftPylon:      nativeSnapshotResponseItem(snapshot, "leftPylon", 3),
			RightPylon:     nativeSnapshotResponseItem(snapshot, "rightPylon", 4),
			MobilityModule: nativeSnapshotResponseItem(snapshot, "mobilityModule", 6),
			MeleeWeapon:    nativeSnapshotResponseItem(snapshot, "meleeWeapon", 5),
			PrimaryWeapon:  nativeSnapshotResponseItem(snapshot, "primaryWeapon", 1),
			SecondWeapon:   nativeSnapshotResponseItem(snapshot, "secondaryWeapon", 2),
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
		role.SkinConfig = nativeSkinConfig(snapshot)
		role.WeaponConfig = nativeWeaponConfig(weaponIDs, archives)
		role.SkinPaint = nativeSnapshotString(snapshot, "skinPaint", "_SkinPaint", "_skinPaint")
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
	roleID, ok := s.service.definitions.CanonicalRoleID(request.GetRoleId())
	itemID := request.GetItemId()
	if !ok ||
		(itemID != "" && !s.service.definitions.HasItem(itemID)) {
		return nil, invalid(map[string]any{"message": "role or item is absent from pinned definitions"})
	}
	if !nativeSupportedOperation(request.GetOperation()) {
		return nil, invalid(map[string]any{"message": "role archive slot is unsupported"})
	}
	var skin metaprotocol.SkinPayload
	if len(request.GetSkinData()) > 0 {
		if err := proto.Unmarshal(request.GetSkinData(), &skin); err != nil {
			return nil, invalid(map[string]any{"message": "invalid role skin payload"})
		}
	}
	err := s.mutateNativeLoadout(ctx, session.PlayerID, roleID, func(snapshot map[string]any) {
		switch request.GetOperation() {
		case 1, 2, 3, 4, 5, 6:
			snapshot[nativeOperationSlot(request.GetOperation())] = nativeEquippedItem(itemID)
		case 7:
			if value := skin.GetSkinModel(); value != "" {
				snapshot["skinModel"] = value
			} else if itemID != "" {
				snapshot["skinModel"] = itemID
			}
			if value := skin.GetSkinPaint(); value != "" {
				snapshot["skinPaint"] = value
			}
		case 9:
			snapshot["armBadge"] = nativeEquippedItem(itemID)
		case 10:
			snapshot["headOrnament"] = nativeEquippedItem(itemID)
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
	_, ok := s.service.definitions.CanonicalRoleID(request.GetRoleId())
	if !ok ||
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

var nativeDefaultRoleOrder = []string{
	"PEACE", "PROBE", "SNIPER", "FORT", "FIXER", "SPIKE",
}

var nativeDefaultLoadouts = map[string]map[string]string{
	"PEACE": {
		"primaryWeapon": "PEACE_RU-AKM", "secondaryWeapon": "PEACE_RU-APS",
		"leftPylon": "PEACE_TAC-EMP", "mobilityModule": "PEACE_FCM-GRAPPLE",
		"skinModel": "PEACE_ORIGINAL", "skinPaint": "PEACE_ORIGINAL_PTOriginal",
		"armBadge": "ABGOrlanDefault",
	},
	"PROBE": {
		"primaryWeapon": "PROBE_GSW-DMR", "secondaryWeapon": "PROBE_GSW-FOP",
		"leftPylon": "PROBE_MISSILE-HIVE", "mobilityModule": "PROBE_FCM-GRAPPLE",
		"skinModel": "PROBE_ORIGINAL", "skinPaint": "PROBE_ORIGINAL_PTOriginal",
		"armBadge": "ABGProbeDefault",
	},
	"SNIPER": {
		"primaryWeapon": "SNIPER_GSW-PSR", "secondaryWeapon": "SNIPER_GSW-CDP",
		"leftPylon": "SNIPER_INFO-SNAPSHOT", "mobilityModule": "SNIPER_FCM-GRAPPLE",
		"skinModel": "SNIPER_ORIGINAL", "skinPaint": "SNIPER_ORIGINAL_PTOriginal",
		"armBadge": "ABGYangDefault",
	},
	"FORT": {
		"primaryWeapon": "FORT_GSW-MG", "secondaryWeapon": "FORT_GSW-IDW",
		"leftPylon": "FORT_TAC-ADS", "mobilityModule": "FORT_FCM-GRAPPLE",
		"skinModel": "FORT_ORIGINAL", "skinPaint": "FORT_ORIGINAL_PTOriginal",
		"armBadge": "ABGFortDefault",
	},
	"FIXER": {
		"primaryWeapon": "FIXER_GSW-PCC", "secondaryWeapon": "FIXER_GSW-FOP",
		"leftPylon": "FIXER_TAC-MED", "mobilityModule": "FIXER_FCM-GRAPPLE",
		"skinModel": "FIXER_ORIGINAL", "skinPaint": "FIXER_ORIGINAL_PTOriginal",
		"armBadge": "ABGDocDefault",
	},
	"SPIKE": {
		"primaryWeapon": "SPIKE_GSW-SG", "secondaryWeapon": "SPIKE_GSW-IDW",
		"leftPylon": "SPIKE_SMK-SQUID", "mobilityModule": "SPIKE_FCM-GRAPPLE",
		"skinModel": "SPIKE_ORIGINAL", "skinPaint": "SPIKE_ORIGINAL_PTOriginal",
		"armBadge": "ABGSpikeDefault",
	},
}

func (s *TCPServer) ensureNativeDefaultLoadouts(ctx context.Context, playerID string) error {
	loadouts, err := s.service.ListLoadouts(ctx, playerID)
	if err != nil {
		return err
	}
	existing := make(map[string]struct{}, len(loadouts))
	for _, loadout := range loadouts {
		existing[loadout.RoleID] = struct{}{}
	}
	for _, roleID := range nativeDefaultRoleOrder {
		if _, ok := existing[roleID]; ok || !s.service.definitions.HasRole(roleID) {
			continue
		}
		if err := s.mutateNativeLoadout(ctx, playerID, roleID, func(map[string]any) {}); err != nil {
			return err
		}
	}
	return nil
}

func defaultNativeLoadoutSnapshot(roleID string) map[string]any {
	snapshot := map[string]any{
		"roleId": roleID, "primaryWeapon": "None", "secondaryWeapon": "None",
		"leftPylon": "None", "rightPylon": "None", "mobilityModule": "None",
		"meleeWeapon": "None",
	}
	defaults, ok := nativeDefaultLoadouts[roleID]
	if !ok {
		return snapshot
	}
	snapshot["meleeWeapon"] = "MELEE-KNIFE"
	snapshot["headOrnament"] = "HONONE"
	for key, value := range defaults {
		snapshot[key] = value
	}
	return snapshot
}

func (s *TCPServer) mutateNativeLoadout(
	ctx context.Context,
	playerID, roleID string,
	mutate func(map[string]any),
) error {
	for range 3 {
		revision := int64(0)
		snapshot := defaultNativeLoadoutSnapshot(roleID)
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

func nativeSnapshotResponseItem(snapshot map[string]any, key string, slot int) string {
	if value := nativeSnapshotString(snapshot, key); value != "" {
		return nativeResponseItem(value)
	}
	inventory, _ := snapshot["inventory"].(map[string]any)
	slots, _ := inventory["slots"].([]any)
	for _, value := range slots {
		entry, _ := value.(map[string]any)
		slotValue, _ := entry["slotType"].(float64)
		if int(slotValue) == slot {
			if itemID, _ := entry["itemId"].(string); itemID != "" {
				return nativeResponseItem(itemID)
			}
		}
	}
	return ""
}

func nativeResponseItem(itemID string) string {
	// The native client treats a literal "None" as an invalid item and restores
	// the role default. An empty proto3 string omits the field and preserves the
	// intentionally empty slot.
	if strings.EqualFold(itemID, "None") {
		return ""
	}
	return itemID
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
	case 1:
		return "primaryWeapon"
	case 2:
		return "secondaryWeapon"
	case 3:
		return "meleeWeapon"
	case 4:
		return "mobilityModule"
	case 5:
		return "leftPylon"
	case 6:
		return "rightPylon"
	default:
		return ""
	}
}

func nativeWeaponConfig(weaponIDs []string, archives map[string][]byte) []byte {
	var output []byte
	seen := make(map[string]struct{}, len(weaponIDs))
	for _, weaponID := range weaponIDs {
		if _, ok := seen[weaponID]; ok {
			continue
		}
		seen[weaponID] = struct{}{}
		raw := archives[weaponID]
		if len(raw) == 0 {
			continue
		}
		output = protowire.AppendTag(output, 1, protowire.BytesType)
		output = protowire.AppendBytes(output, raw)
	}
	return output
}

func nativeSkinConfig(snapshot map[string]any) []byte {
	model := nativeSnapshotString(snapshot, "skinModel", "_SkinBase", "_skinBase", "_skinToken", "skinToken")
	paint := nativeSnapshotString(snapshot, "skinPaint", "_SkinPaint", "_skinPaint")
	badge := nativeSnapshotString(snapshot, "armBadge", "_Cosmetic9", "_cosmetic9")
	ornament := nativeSnapshotString(
		snapshot, "headOrnament", "_Cosmetic10", "_cosmetic10", "_ornamentId", "ornamentId",
	)
	var config []byte
	if model != "" || paint != "" {
		var suit []byte
		suit = appendNativeString(suit, 1, model)
		suit = appendNativeString(suit, 2, paint)
		config = protowire.AppendTag(config, 1, protowire.BytesType)
		config = protowire.AppendBytes(config, suit)
	}
	config = appendNativeString(config, 2, badge)
	config = appendNativeString(config, 3, ornament)
	return config
}

func appendNativeString(output []byte, field protowire.Number, value string) []byte {
	if value == "" || strings.EqualFold(value, "none") {
		return output
	}
	output = protowire.AppendTag(output, field, protowire.BytesType)
	return protowire.AppendString(output, value)
}

func nativeEquippedItem(itemID string) string {
	if itemID == "" {
		return "None"
	}
	return itemID
}

func nativeSupportedOperation(operation int32) bool {
	return operation >= 1 && operation <= 7 || operation == 9 || operation == 10
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
