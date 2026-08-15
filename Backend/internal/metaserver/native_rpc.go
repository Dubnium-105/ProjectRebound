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
		response.PlayerRoleDatas = append(
			response.PlayerRoleDatas,
			nativePlayerRoleData(requestedRoleID, snapshot),
		)
	}
	// QueryAssets establishes ownership, while the native archive level controls
	// the progression filters used during FieldMod/armory initialization. Keep
	// the value build-configurable and within the one-byte range validated by the
	// controlled Frida A/B probe.
	response.PlayerLevel = int32(s.config.NativePlayerLevel)
	return proto.Marshal(response)
}

// getDataStatisticsInfo returns the exact progression keys consumed by
// UPBCareerManager in the pinned Steam executable. Static analysis of
// EXE+0x16E5720 and EXE+0x16E3BC0 confirms the client passes Level/Exp as the
// first Win64 variadic argument to the %s_%s formatter, followed by Player or
// the character FName. The resulting keys are therefore Metric_Scope, not the
// visually tempting reverse order. Sniper retains mixed casing in this build.
func (s *TCPServer) getDataStatisticsInfo() ([]byte, error) {
	response := &metaprotocol.GetDataStatisticsInfoResponse{
		Datapoints: []*metaprotocol.PlayerDatapoint{
			{Key: "Level_Player", Value: int32(s.config.NativePlayerLevel)},
			{Key: "Exp_Player", Value: 0},
		},
	}
	for _, characterID := range []string{
		"PEACE", "PROBE", "Sniper", "FORT", "FIXER", "SPIKE",
	} {
		response.Datapoints = append(response.Datapoints,
			&metaprotocol.PlayerDatapoint{
				Key: "Level_" + characterID, Value: int32(s.config.NativeCharacterLevel),
			},
			&metaprotocol.PlayerDatapoint{Key: "Exp_" + characterID, Value: 0},
		)
	}
	return proto.Marshal(response)
}

func nativePlayerRoleData(
	requestedRoleID string,
	snapshot map[string]any,
) *metaprotocol.PlayerRoleData {
	return &metaprotocol.PlayerRoleData{
		RoleId:         requestedRoleID,
		LeftPylon:      nativeSnapshotResponseItem(snapshot, "leftPylon", 3),
		RightPylon:     nativeSnapshotResponseItem(snapshot, "rightPylon", 4),
		MobilityModule: nativeSnapshotResponseItem(snapshot, "mobilityModule", 6),
		MeleeWeapon:    nativeSnapshotResponseItem(snapshot, "meleeWeapon", 5),
		PrimaryWeapon:  nativeSnapshotResponseItem(snapshot, "primaryWeapon", 1),
		SecondWeapon:   nativeSnapshotResponseItem(snapshot, "secondaryWeapon", 2),
	}
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
	var skin metaprotocol.SkinPayload
	if len(request.GetSkinData()) > 0 {
		if err := proto.Unmarshal(request.GetSkinData(), &skin); err != nil {
			return nil, invalid(map[string]any{"message": "invalid role skin payload"})
		}
	}
	err := s.mutateNativeLoadout(ctx, session.PlayerID, roleID, func(snapshot map[string]any) {
		applyNativeRoleUpdate(s.service.definitions, snapshot, &request, &skin)
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
	if err := validateNativeWeaponArchiveUpdate(
		s.service.definitions, request.GetRoleId(), archive,
	); err != nil {
		return nil, err
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

func validateNativeWeaponArchiveUpdate(
	definitions *DefinitionIndex,
	roleID string,
	archive *metaprotocol.WeaponArchiveV2,
) error {
	if definitions == nil {
		return internalError(nil)
	}
	if _, ok := definitions.CanonicalRoleID(roleID); !ok ||
		archive == nil || !definitions.HasWeapon(archive.GetWeaponId()) {
		return invalid(map[string]any{
			"message": "role or weapon is absent from pinned definitions",
		})
	}
	if !definitions.ItemAllowedForRole(roleID, archive.GetWeaponId()) {
		return invalid(map[string]any{
			"message": "weapon is not available to the requested role",
		})
	}
	// This is the same definition-backed validation used by the host loadout
	// projection: slots must be unique and in range, every instantiated part
	// must belong to that exact weapon slot, and cosmetic identifiers must have
	// the expected types/scopes. It also accepts a skin-only partial archive.
	if !p2pWeaponArchiveIsValid(definitions, archive) {
		return invalid(map[string]any{
			"message": "weapon archive is incompatible with the weapon definition",
		})
	}
	return nil
}

func (s *TCPServer) queryAssets() ([]byte, error) {
	s.queryAssetsOnce.Do(func() {
		mode := strings.ToLower(strings.TrimSpace(s.config.NativeOwnershipMode))
		if mode == "" {
			mode = "full"
		}
		itemIDs := s.service.definitions.NativeOwnershipItemIDs(mode == "full")
		response := &metaprotocol.QueryAssetsResponse{
			// Despite the upstream name, field 1 is a native result/status value,
			// not the repeated-row count. PBArmoryManager rejects values above 299
			// before it examines ItemDatas; the captured success value is one.
			ItemCount: 1,
			ItemDatas: make([]*metaprotocol.ItemData, 0, len(itemIDs)),
		}
		for _, itemID := range itemIDs {
			response.ItemDatas = append(response.ItemDatas, &metaprotocol.ItemData{
				// The pinned client completion handler copies only ItemId into the
				// native ownership array. Leaving the three unused scalar fields at
				// their protobuf defaults saves six wire bytes per row without changing
				// ownership semantics.
				ItemId: itemID,
			})
		}
		s.queryAssetsRowCount = len(itemIDs)
		s.queryAssetsSetHash = nativeStringSetDigest(itemIDs)
		s.queryAssetsMode = mode
		s.queryAssetsPayload, s.queryAssetsErr = proto.Marshal(response)
	})
	return s.queryAssetsPayload, s.queryAssetsErr
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
	// "None" is the native protocol sentinel for an intentionally empty slot.
	// Omitting the proto3 string makes the client treat the value as absent and
	// restore the role default during archive initialization instead.
	if strings.EqualFold(itemID, "None") {
		return "None"
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
	case 2:
		return "leftPylon"
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

func applyNativeRoleUpdate(
	definitions *DefinitionIndex,
	snapshot map[string]any,
	request *metaprotocol.UpdateRoleArchiveV2Request,
	skin *metaprotocol.SkinPayload,
) {
	if value := skin.GetTokenId(); value != "" {
		snapshot["skinModel"] = value
	}
	if value := skin.GetOrnamentId(); value != "" {
		snapshot["skinPaint"] = value
	}
	itemID := request.GetItemId()
	if itemID == "" || strings.EqualFold(itemID, "None") {
		if skin.GetTokenId() == "" && skin.GetOrnamentId() == "" {
			snapshot[nativeOperationSlot(request.GetOperation())] = "None"
		}
		return
	}
	if !definitions.ItemAllowedForRole(request.GetRoleId(), itemID) {
		return
	}
	switch definitions.ItemType(itemID) {
	case "EPBItemType::Weapon":
		if request.GetOperation() == 2 || request.GetOperation() == 7 {
			snapshot["secondaryWeapon"] = itemID
		} else {
			snapshot["primaryWeapon"] = itemID
		}
	case "EPBItemType::Pod":
		switch request.GetOperation() {
		case 3, 6, 7:
			snapshot["rightPylon"] = itemID
		case 1, 2, 5:
			snapshot["leftPylon"] = itemID
		default:
			if nativeSnapshotString(snapshot, "leftPylon") == "" ||
				strings.EqualFold(nativeSnapshotString(snapshot, "leftPylon"), "None") {
				snapshot["leftPylon"] = itemID
			} else {
				snapshot["rightPylon"] = itemID
			}
		}
	case "EPBItemType::Mobility":
		snapshot["mobilityModule"] = itemID
	case "EPBItemType::MeleeWeapon":
		snapshot["meleeWeapon"] = itemID
	case "EPBItemType::ArmBadge":
		snapshot["armBadge"] = itemID
	case "EPBItemType::HeadAccessory":
		snapshot["headOrnament"] = itemID
	}
}

func nativeDefaultWeaponArchive(
	definitions *DefinitionIndex,
	weaponID string,
) (*metaprotocol.WeaponArchiveV2, bool) {
	definition, ok := definitions.DefaultWeaponArchive(weaponID)
	if !ok {
		return nil, false
	}
	archive := &metaprotocol.WeaponArchiveV2{
		WeaponId: weaponID,
		Parts:    make([]*metaprotocol.PartSlot, 0, len(definition.Parts)),
		Skin: &metaprotocol.WeaponSkin{
			SkinInfo: &metaprotocol.OrnamentInfo{
				Type: definition.SkinType,
				Id:   definition.SkinID,
			},
			WeaponOrnament: "WO-NONE",
		},
	}
	for _, part := range definition.Parts {
		archive.Parts = append(archive.Parts, &metaprotocol.PartSlot{
			SlotId: part.SlotID,
			PartId: part.PartID,
			Ornament: &metaprotocol.PartOrnament{
				Info: &metaprotocol.OrnamentInfo{},
			},
		})
	}
	return archive, true
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
