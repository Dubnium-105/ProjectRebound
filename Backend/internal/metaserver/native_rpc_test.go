package metaserver

import (
	"encoding/json"
	"strings"
	"testing"

	metaprotocol "github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver/protocol"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestQueryAssetsUsesPinnedDefinitions(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	server := &TCPServer{service: &Service{definitions: definitions}}
	raw, err := server.queryAssets()
	if err != nil {
		t.Fatal(err)
	}
	var response metaprotocol.QueryAssetsResponse
	if err := proto.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.GetItemCount() != int32(len(definitions.Items)) {
		t.Fatalf("item count=%d definitions=%d", response.GetItemCount(), len(definitions.Items))
	}
	if len(response.GetItemDatas()) == 0 ||
		response.GetItemDatas()[0].GetItemId() == "" {
		t.Fatal("query assets did not include pinned item IDs")
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

func TestNativeWeaponConfig(t *testing.T) {
	archive, err := proto.Marshal(&metaprotocol.WeaponArchiveV2{WeaponId: "weapon-a"})
	if err != nil {
		t.Fatal(err)
	}
	config := nativeWeaponConfig(
		[]string{"weapon-a", "weapon-a"}, map[string][]byte{"weapon-a": archive},
	)
	fields := 0
	for len(config) > 0 {
		number, typ, n := protowire.ConsumeTag(config)
		if n < 0 {
			t.Fatal("invalid weapon config tag")
		}
		config = config[n:]
		if number != 1 || typ != protowire.BytesType {
			t.Fatal("unexpected wire type")
		}
		value, n := protowire.ConsumeBytes(config)
		if n < 0 {
			t.Fatal("invalid weapon config value")
		}
		var decoded metaprotocol.WeaponArchiveV2
		if err := proto.Unmarshal(value, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.GetWeaponId() != "weapon-a" {
			t.Fatalf("weapon id=%q", decoded.GetWeaponId())
		}
		fields++
		config = config[n:]
	}
	if fields != 1 {
		t.Fatalf("weapon archive fields=%d", fields)
	}
}

func TestNativeSkinConfig(t *testing.T) {
	config := nativeSkinConfig(map[string]any{
		"skinModel": "skin-a", "skinPaint": "paint-a",
		"armBadge": "badge-a", "headOrnament": "ornament-a",
	})
	suitRaw, ok := consumeBytesField(config, 1)
	if !ok {
		t.Fatal("skin config omitted suit")
	}
	if model, ok := consumeStringField(suitRaw, 1); !ok || model != "skin-a" {
		t.Fatalf("skin model=%q ok=%v", model, ok)
	}
	if paint, ok := consumeStringField(suitRaw, 2); !ok || paint != "paint-a" {
		t.Fatalf("skin paint=%q ok=%v", paint, ok)
	}
	if badge, ok := consumeStringField(config, 2); !ok || badge != "badge-a" {
		t.Fatalf("arm badge=%q ok=%v", badge, ok)
	}
	if ornament, ok := consumeStringField(config, 3); !ok || ornament != "ornament-a" {
		t.Fatalf("head ornament=%q ok=%v", ornament, ok)
	}
}

func TestNativeSlotMappingAndDefaults(t *testing.T) {
	want := map[int32]string{
		1: "primaryWeapon", 2: "secondaryWeapon", 3: "meleeWeapon",
		4: "mobilityModule", 5: "leftPylon", 6: "rightPylon",
	}
	for operation, key := range want {
		if got := nativeOperationSlot(operation); got != key {
			t.Fatalf("operation %d maps to %q, want %q", operation, got, key)
		}
	}
	if got := nativeOperationSlot(7); got != "" {
		t.Fatalf("skin operation maps to inventory key %q", got)
	}
	defaults := defaultNativeLoadoutSnapshot("SNIPER")
	if defaults["primaryWeapon"] != "SNIPER_GSW-PSR" ||
		defaults["meleeWeapon"] != "MELEE-KNIFE" ||
		defaults["headOrnament"] != "HONONE" {
		t.Fatalf("unexpected SNIPER defaults: %#v", defaults)
	}
}

func TestNativePlayerArchiveOmitsNoneItems(t *testing.T) {
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
	if value, ok := consumeStringField(raw, 6); ok {
		t.Fatalf("primary weapon encoded as %q, want field omitted", value)
	}
	if value, ok := consumeStringField(raw, 7); !ok || value != "PEACE_RU-APS" {
		t.Fatalf("second weapon=%q ok=%v", value, ok)
	}
}

func TestNativePlayerArchiveOmitsNestedNoneItems(t *testing.T) {
	snapshot := map[string]any{
		"inventory": map[string]any{
			"slots": []any{
				map[string]any{"slotType": float64(1), "itemId": "None"},
			},
		},
	}
	if got := nativeSnapshotResponseItem(snapshot, "primaryWeapon", 1); got != "" {
		t.Fatalf("primary weapon=%q, want empty response field", got)
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

func consumeBytesField(data []byte, wanted protowire.Number) ([]byte, bool) {
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, false
		}
		data = data[n:]
		if typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(number, typ, data)
			if n < 0 {
				return nil, false
			}
			data = data[n:]
			continue
		}
		value, n := protowire.ConsumeBytes(data)
		if n < 0 {
			return nil, false
		}
		if number == wanted {
			return value, true
		}
		data = data[n:]
	}
	return nil, false
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
