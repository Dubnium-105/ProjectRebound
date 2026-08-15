package metaserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	metaprotocol "github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver/protocol"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestQueryAssetsUsesPinnedDefinitions(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	server := NewTCPServer(
		config.MetaServerConfig{}, &Service{definitions: definitions}, nil, nil, nil,
	)
	if len(server.queryAssetsPayload) == 0 || server.queryAssetsRowCount != 40462 {
		t.Fatal("query assets response was not prepared during server construction")
	}
	raw, err := server.queryAssets()
	if err != nil {
		t.Fatal(err)
	}
	var response metaprotocol.QueryAssetsResponse
	if err := proto.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.GetItemCount() != 1 {
		t.Fatalf("native query result=%d, want captured success value 1",
			response.GetItemCount())
	}
	if len(response.GetItemDatas()) != len(definitions.NativeOwnershipItemIDs(true)) {
		t.Fatalf("item data count=%d full definitions=%d",
			len(response.GetItemDatas()), len(definitions.NativeOwnershipItemIDs(true)))
	}
	seen := make(map[string]struct{}, len(response.GetItemDatas()))
	for _, item := range response.GetItemDatas() {
		if item.GetItemId() == "" {
			t.Fatal("query assets included an empty item ID")
		}
		if _, duplicate := seen[item.GetItemId()]; duplicate {
			t.Fatalf("query assets duplicated %q", item.GetItemId())
		}
		seen[item.GetItemId()] = struct{}{}
		if item.GetUnknown_1() != 0 || item.GetUnknown_2() != 0 || item.GetUnknown_3() != 0 {
			t.Fatalf("query assets emitted unused scalar fields for %q: %#v", item.GetItemId(), item)
		}
	}
	for _, itemID := range definitions.NativeOwnershipItemIDs(true) {
		if _, ok := seen[itemID]; !ok {
			t.Fatalf("query assets omitted pinned item %q", itemID)
		}
	}
	if len(response.GetItemDatas()) != 40462 {
		t.Fatalf("full ownership row count drifted: got %d, want 40462",
			len(response.GetItemDatas()))
	}
	if len(raw) != 1372853 {
		t.Fatalf("full QueryAssets payload size drifted: got %d, want %d", len(raw), 1372853)
	}
	digest := sha256.Sum256(raw)
	const capturedSchemaGolden = "d3aa4e84d75689e42ecc54f9735b6842762c56b5814e61ef8b2c5e01b4e31531"
	if got := hex.EncodeToString(digest[:]); got != capturedSchemaGolden {
		t.Fatalf("QueryAssets optimized-schema protobuf drifted: got %s, want %s",
			got, capturedSchemaGolden)
	}
	cached, err := server.queryAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) == 0 || &cached[0] != &raw[0] {
		t.Fatal("query assets did not reuse its immutable serialized response")
	}
	if server.queryAssetsRowCount != 40462 || server.queryAssetsSetHash == "" ||
		server.queryAssetsMode != "full" {
		t.Fatalf("query assets summary was not cached: rows=%d hash=%q",
			server.queryAssetsRowCount, server.queryAssetsSetHash)
	}
	for index := 1; index < len(response.GetItemDatas()); index++ {
		if response.GetItemDatas()[index-1].GetItemId() >=
			response.GetItemDatas()[index].GetItemId() {
			t.Fatalf("query assets is not strictly FName-sorted at row %d", index)
		}
	}
}

func TestQueryAssetsCompactDiagnosticMode(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	server := NewTCPServer(
		config.MetaServerConfig{NativeOwnershipMode: "compact"},
		&Service{definitions: definitions}, nil, nil, nil,
	)
	raw, err := server.queryAssets()
	if err != nil {
		t.Fatal(err)
	}
	var response metaprotocol.QueryAssetsResponse
	if err := proto.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.GetItemDatas()) != 2741 || server.queryAssetsMode != "compact" {
		t.Fatalf("unexpected compact ownership response: rows=%d mode=%q",
			len(response.GetItemDatas()), server.queryAssetsMode)
	}
}

func TestNativeProgressionStatisticsUseExactClientKeys(t *testing.T) {
	server := &TCPServer{config: config.MetaServerConfig{
		NativePlayerLevel: 70, NativeCharacterLevel: 30,
	}}
	raw, err := server.getDataStatisticsInfo()
	if err != nil {
		t.Fatal(err)
	}
	var response metaprotocol.GetDataStatisticsInfoResponse
	if err := proto.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		key   string
		value int32
	}{
		{"Level_Player", 70}, {"Exp_Player", 0},
		{"Level_PEACE", 30}, {"Exp_PEACE", 0},
		{"Level_PROBE", 30}, {"Exp_PROBE", 0},
		{"Level_Sniper", 30}, {"Exp_Sniper", 0},
		{"Level_FORT", 30}, {"Exp_FORT", 0},
		{"Level_FIXER", 30}, {"Exp_FIXER", 0},
		{"Level_SPIKE", 30}, {"Exp_SPIKE", 0},
	}
	if response.GetStatusCode() != 0 || len(response.GetDatapoints()) != len(want) {
		t.Fatalf("unexpected progression response: %#v", &response)
	}
	for index, expected := range want {
		actual := response.GetDatapoints()[index]
		if actual.GetKey() != expected.key || actual.GetValue() != expected.value {
			t.Fatalf("datapoint %d=(%q,%d), want (%q,%d)", index,
				actual.GetKey(), actual.GetValue(), expected.key, expected.value)
		}
	}
	digest := sha256.Sum256(raw)
	const reverseEngineeredWireGolden = "6938b07f1ddc2d334d89468c14eff10e10c9c629645269df62f5c2089d567fae"
	if got := hex.EncodeToString(digest[:]); got != reverseEngineeredWireGolden {
		t.Fatalf("progression protobuf drifted: got %s, want %s",
			got, reverseEngineeredWireGolden)
	}
}

func TestNativeIdentityMismatch(t *testing.T) {
	raw, err := proto.Marshal(&metaprotocol.StartMatchmakingRequest{
		Payload: &metaprotocol.MatchmakingPayload{
			MatchmakingRequestorUserId: "player_other",
		},
		GameMode: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := nativeIdentityMatches(raw, "player_expected"); err == nil {
		t.Fatal("expected claimed player mismatch to fail")
	}
}

func TestNativeWeaponArchiveBundleRoundTripsSelectedArchive(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	archive := validP2PAKMArchive()
	raw, err := proto.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	bundle := nativeWeaponArchiveBundle(
		definitions,
		"PEACE",
		[]string{archive.GetWeaponId(), archive.GetWeaponId()},
		map[string][]byte{archive.GetWeaponId(): raw},
	)
	decoded := nativeSnapshotWeaponArchives(
		definitions,
		"PEACE",
		map[string]any{"_weaponArchiveRaw": hex.EncodeToString(bundle)},
		[]string{archive.GetWeaponId()},
	)
	var readBack metaprotocol.WeaponArchiveV2
	if err := proto.Unmarshal(decoded[archive.GetWeaponId()], &readBack); err != nil {
		t.Fatalf("bundled weapon archive was not decoded: %v", err)
	}
	expected, ok := p2pCompleteWeaponArchive(definitions, archive.GetWeaponId(), archive)
	if !ok || !proto.Equal(expected, &readBack) {
		t.Fatalf("bundled archive was not completed: got %#v want %#v", &readBack, expected)
	}
}

func TestNativeDefaultWeaponArchiveUsesPinnedScopes(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	archive, ok := nativeDefaultWeaponArchive(definitions, "PEACE_RU-AKM")
	if !ok {
		t.Fatal("default AKM archive was not generated")
	}
	if archive.GetWeaponId() != "PEACE_RU-AKM" || len(archive.GetParts()) != 10 {
		t.Fatalf("unexpected default AKM archive: %#v", archive)
	}
	if archive.GetParts()[0].GetPartId() != "RU-AKM_MZL-STD" ||
		archive.GetParts()[1].GetPartId() != "RU-AKM_BRL-AK-STD" ||
		archive.GetParts()[2].GetPartId() != "" {
		t.Fatalf("unexpected default AKM parts: %#v", archive.GetParts())
	}
	if archive.GetSkin().GetSkinInfo().GetType() != "SkinAKMOriginal" ||
		archive.GetSkin().GetSkinInfo().GetId() != "RU-AKM_Original_PTOriginal" ||
		archive.GetSkin().GetWeaponOrnament() != "WO-NONE" {
		t.Fatalf("unexpected default AKM skin: %#v", archive.GetSkin())
	}
}

func TestNativeWeaponArchiveUpdateValidatesInstantiatedPartsAndRoleScope(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	if !definitions.HasPart("RU-AKM_MZL-STD") {
		t.Fatal("instantiated weapon-part item was not recognized")
	}
	if !definitions.HasPart("MZL-STD") {
		t.Fatal("reusable weapon-part definition was not recognized")
	}

	valid := validP2PAKMArchive()
	if err := validateNativeWeaponArchiveUpdate(definitions, "PEACE", valid); err != nil {
		t.Fatalf("valid native archive was rejected: %v", err)
	}

	wrongRole := proto.Clone(valid).(*metaprotocol.WeaponArchiveV2)
	if err := validateNativeWeaponArchiveUpdate(definitions, "PROBE", wrongRole); err == nil {
		t.Fatal("role-incompatible weapon archive was accepted")
	}

	wrongSlot := proto.Clone(valid).(*metaprotocol.WeaponArchiveV2)
	wrongSlot.Parts[0].PartId = "RU-AKM_BRL-AK-STD"
	if err := validateNativeWeaponArchiveUpdate(definitions, "PEACE", wrongSlot); err == nil {
		t.Fatal("weapon part from the wrong slot was accepted")
	}

	otherWeapon := proto.Clone(valid).(*metaprotocol.WeaponArchiveV2)
	otherWeapon.Parts[0].PartId = "GSW-DMR_MZL-STD"
	if err := validateNativeWeaponArchiveUpdate(definitions, "PEACE", otherWeapon); err == nil {
		t.Fatal("part from another weapon was accepted")
	}
}

func TestNativeWeaponArchiveUpdateAcceptsSkinOnlyPartial(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	archive := &metaprotocol.WeaponArchiveV2{
		WeaponId: "PEACE_RU-AKM",
		Skin: &metaprotocol.WeaponSkin{
			SkinInfo: &metaprotocol.OrnamentInfo{
				Type: "SkinAKMTiger", Id: "SkinAKMTiger_PTTiger",
			},
			WeaponOrnament: "WO-SUN",
		},
	}
	if err := validateNativeWeaponArchiveUpdate(definitions, "PEACE", archive); err != nil {
		t.Fatalf("skin-only native archive was rejected: %v", err)
	}
	if err := validateNativeWeaponArchiveUpdate(
		definitions, "PEACE", &metaprotocol.WeaponArchiveV2{WeaponId: "PEACE_RU-AKM"},
	); err == nil {
		t.Fatal("archive with no effective part or skin update was accepted")
	}
}

func TestNativeRoleUpdateRoutesByItemType(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{
		"primaryWeapon": "old-primary", "secondaryWeapon": "old-secondary",
		"leftPylon": "old-left", "rightPylon": "old-right",
	}
	applyNativeRoleUpdate(definitions, snapshot, &metaprotocol.UpdateRoleArchiveV2Request{
		Operation: 6, RoleId: "PEACE", ItemId: "PEACE_RU-AKM",
	}, &metaprotocol.SkinPayload{})
	if snapshot["primaryWeapon"] != "PEACE_RU-AKM" || snapshot["rightPylon"] != "old-right" {
		t.Fatalf("op 6 weapon routed incorrectly: %#v", snapshot)
	}
	applyNativeRoleUpdate(definitions, snapshot, &metaprotocol.UpdateRoleArchiveV2Request{
		Operation: 7, RoleId: "PEACE", ItemId: "PEACE_RU-APS",
	}, &metaprotocol.SkinPayload{})
	if snapshot["secondaryWeapon"] != "PEACE_RU-APS" {
		t.Fatalf("op 7 weapon routed incorrectly: %#v", snapshot)
	}
	applyNativeRoleUpdate(definitions, snapshot, &metaprotocol.UpdateRoleArchiveV2Request{
		Operation: 6, RoleId: "PEACE", ItemId: "PEACE_TAC-EMP",
	}, &metaprotocol.SkinPayload{})
	if snapshot["rightPylon"] != "PEACE_TAC-EMP" || snapshot["primaryWeapon"] != "PEACE_RU-AKM" {
		t.Fatalf("op 6 pod routed incorrectly: %#v", snapshot)
	}
}

func TestNativeSkinOnlyUpdateDoesNotClearSlot(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{"primaryWeapon": "PEACE_RU-AKM"}
	applyNativeRoleUpdate(definitions, snapshot, &metaprotocol.UpdateRoleArchiveV2Request{
		Operation: 6,
	}, &metaprotocol.SkinPayload{TokenId: "skin-a", OrnamentId: "paint-a"})
	if snapshot["primaryWeapon"] != "PEACE_RU-AKM" {
		t.Fatalf("skin-only update cleared primary weapon: %#v", snapshot)
	}
	if snapshot["skinModel"] != "skin-a" || snapshot["skinPaint"] != "paint-a" {
		t.Fatalf("skin-only update was not persisted: %#v", snapshot)
	}
}

func TestNativeSlotMappingAndDefaults(t *testing.T) {
	want := map[int32]string{
		1: "primaryWeapon", 2: "leftPylon", 3: "rightPylon",
		4: "mobilityModule", 5: "meleeWeapon", 6: "primaryWeapon",
		7: "secondaryWeapon",
	}
	for operation, key := range want {
		if got := nativeOperationSlot(operation); got != key {
			t.Fatalf("operation %d maps to %q, want %q", operation, got, key)
		}
	}
	defaults := defaultNativeLoadoutSnapshot("SNIPER")
	if defaults["primaryWeapon"] != "SNIPER_GSW-PSR" ||
		defaults["meleeWeapon"] != "MELEE-KNIFE" ||
		defaults["headOrnament"] != "HONONE" {
		t.Fatalf("unexpected SNIPER defaults: %#v", defaults)
	}
}

func TestNativePlayerArchivePreservesNoneItems(t *testing.T) {
	role := &metaprotocol.PlayerRoleData{
		RoleId: "PEACE",
		PrimaryWeapon: nativeSnapshotResponseItem(map[string]any{
			"primaryWeapon": "None",
		}, "primaryWeapon", 1),
		SecondWeapon: nativeSnapshotResponseItem(map[string]any{
			"secondaryWeapon": "PEACE_RU-APS",
		}, "secondaryWeapon", 2),
	}
	raw, err := proto.Marshal(role)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := consumeStringField(raw, 6); !ok || value != "None" {
		t.Fatalf("primary weapon=%q ok=%v, want native None sentinel", value, ok)
	}
	if value, ok := consumeStringField(raw, 7); !ok || value != "PEACE_RU-APS" {
		t.Fatalf("second weapon=%q ok=%v", value, ok)
	}
}

func TestNativePlayerArchivePreservesNestedNoneItems(t *testing.T) {
	snapshot := map[string]any{
		"inventory": map[string]any{
			"slots": []any{
				map[string]any{"slotType": float64(1), "itemId": "None"},
			},
		},
	}
	if got := nativeSnapshotResponseItem(snapshot, "primaryWeapon", 1); got != "None" {
		t.Fatalf("primary weapon=%q, want native None sentinel", got)
	}
}

func TestNativePlayerArchiveProjectsSlotsWeaponArchivesAndCosmetics(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, canonicalRoleID := range nativeDefaultRoleOrder {
		t.Run(canonicalRoleID, func(t *testing.T) {
			snapshot := defaultNativeLoadoutSnapshot(canonicalRoleID)
			requestedRoleID := strings.ToLower(canonicalRoleID)
			role := nativePlayerRoleData(requestedRoleID, snapshot)
			if role.GetRoleId() != requestedRoleID {
				t.Fatalf("role casing=%q, want requested casing %q",
					role.GetRoleId(), requestedRoleID)
			}
			wantSlots := []string{
				nativeSnapshotResponseItem(snapshot, "leftPylon", 3),
				nativeSnapshotResponseItem(snapshot, "rightPylon", 4),
				nativeSnapshotResponseItem(snapshot, "mobilityModule", 6),
				nativeSnapshotResponseItem(snapshot, "meleeWeapon", 5),
				nativeSnapshotResponseItem(snapshot, "primaryWeapon", 1),
				nativeSnapshotResponseItem(snapshot, "secondaryWeapon", 2),
			}
			gotSlots := []string{
				role.GetLeftPylon(), role.GetRightPylon(), role.GetMobilityModule(),
				role.GetMeleeWeapon(), role.GetPrimaryWeapon(), role.GetSecondWeapon(),
			}
			for index := range wantSlots {
				if gotSlots[index] != wantSlots[index] {
					t.Fatalf("slot %d=%q, want %q", index, gotSlots[index], wantSlots[index])
				}
			}

			weaponIDs := nativePlayerRoleWeaponIDs(role)
			attachNativePlayerRoleArchive(
				definitions, canonicalRoleID, snapshot, weaponIDs, nil, role,
			)
			if role.GetWeaponArchiveRaw() == "" {
				t.Fatal("field 8 weapon archive was omitted")
			}
			if role.GetSkinToken() != nativeSnapshotString(snapshot, "skinModel") ||
				role.GetOrnamentId() != nativeSnapshotString(snapshot, "skinPaint") {
				t.Fatalf("field 9/10 cosmetics drifted: %#v", role)
			}
			raw, err := proto.Marshal(role)
			if err != nil {
				t.Fatal(err)
			}
			seen := make(map[protowire.Number]bool)
			for len(raw) > 0 {
				number, wireType, consumed := protowire.ConsumeTag(raw)
				if consumed < 0 || number < 1 || number > 10 {
					t.Fatalf("unexpected PlayerRoleData field %d", number)
				}
				seen[number] = true
				raw = raw[consumed:]
				consumed = protowire.ConsumeFieldValue(number, wireType, raw)
				if consumed < 0 {
					t.Fatalf("invalid PlayerRoleData field %d", number)
				}
				raw = raw[consumed:]
			}
			for _, number := range []protowire.Number{8, 9, 10} {
				if !seen[number] {
					t.Fatalf("PlayerRoleData field %d was not serialized", number)
				}
			}
		})
	}
}

func TestNativeDefaultLoadoutsUsePinnedDefinitions(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, roleID := range nativeDefaultRoleOrder {
		canonical, ok := definitions.CanonicalRoleID(strings.ToLower(roleID))
		if !ok || canonical != roleID {
			t.Fatalf("canonical role for %q=%q ok=%v", roleID, canonical, ok)
		}
		if err := definitions.ValidateLoadoutSnapshot(
			roleID, defaultNativeLoadoutSnapshot(roleID),
		); err != nil {
			t.Fatalf("invalid defaults for %s: %v", roleID, err)
		}
	}
}

func TestLoadoutSnapshotDefinitionValidation(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	var roleID, itemID string
	for candidate := range definitions.Roles {
		roleID = candidate
		break
	}
	for candidate := range definitions.Items {
		itemID = candidate
		break
	}
	service := &Service{definitions: definitions}
	valid := map[string]any{"roleId": roleID, "primaryWeapon": itemID}
	if err := service.validateLoadoutSnapshot(roleID, valid); err != nil {
		t.Fatalf("valid pinned item rejected: %v", err)
	}
	invalid := map[string]any{"roleId": roleID, "primaryWeapon": "not-in-definitions"}
	if err := service.validateLoadoutSnapshot(roleID, invalid); err == nil {
		t.Fatal("unknown item accepted")
	}
	if _, err := json.Marshal(valid); err != nil {
		t.Fatal(err)
	}
}
