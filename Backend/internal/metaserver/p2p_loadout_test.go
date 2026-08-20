package metaserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metaprotocol "github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestP2PRoomLoadoutsProjectOnlyReferencedStructuredWeaponConfigs(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	stored := []Loadout{{
		RoleID: "PEACE", Revision: 7,
		Snapshot: json.RawMessage(`{
			"roleId":"PEACE",
			"primaryWeapon":"PEACE_RU-AKM",
			"secondaryWeapon":"PEACE_RU-APS",
			"meleeWeapon":"MELEE-KNIFE",
			"_weaponArchiveRaw":"deadbeef",
			"weaponArchives":{"PEACE_RU-AKM":"cafebabe"}
		}`),
	}}
	valid, weaponIDs := validateP2PRoomLoadouts(definitions, stored)
	if len(valid) != 1 || len(weaponIDs) != 2 ||
		weaponIDs[0] != "PEACE_RU-AKM" || weaponIDs[1] != "PEACE_RU-APS" {
		t.Fatalf("validated loadouts=%#v weapon IDs=%#v", valid, weaponIDs)
	}

	customArchive := &metaprotocol.WeaponArchiveV2{
		WeaponId: "PEACE_RU-AKM",
		Parts: []*metaprotocol.PartSlot{{
			SlotId: 1, PartId: "RU-AKM_MZL-STD",
			Ornament: &metaprotocol.PartOrnament{
				Info: &metaprotocol.OrnamentInfo{
					Type: "PartOri", Id: "RU-AKM_Muzzle_PTOriginal",
				},
			},
		}},
		Skin: &metaprotocol.WeaponSkin{
			SkinInfo: &metaprotocol.OrnamentInfo{
				Type: "SkinAKMOriginal", Id: "RU-AKM_Original_PTOriginal",
			},
			WeaponOrnament: "WO-NONE",
		},
	}
	raw, err := proto.Marshal(customArchive)
	if err != nil {
		t.Fatal(err)
	}
	projected := buildP2PRoomRoleLoadouts(
		definitions, valid, map[string][]byte{"PEACE_RU-AKM": raw},
	)
	if len(projected) != 1 || projected[0].RoleID != "PEACE" || projected[0].Revision != 7 {
		t.Fatalf("projected loadouts=%#v", projected)
	}
	configs := projected[0].WeaponConfigs
	if len(configs) != 2 {
		t.Fatalf("weapon configs=%s", mustJSON(t, configs))
	}
	var primary metaprotocol.WeaponArchiveV2
	if err := protojson.Unmarshal(configs["PEACE_RU-AKM"], &primary); err != nil {
		t.Fatal(err)
	}
	if primary.GetWeaponId() != "PEACE_RU-AKM" || len(primary.GetParts()) != 10 ||
		primary.GetParts()[0].GetOrnament().GetInfo().GetType() != "PartOri" ||
		primary.GetParts()[0].GetOrnament().GetInfo().GetId() != "RU-AKM_Muzzle_PTOriginal" ||
		primary.GetParts()[1].GetPartId() != "RU-AKM_BRL-AK-STD" ||
		primary.GetSkin().GetSkinInfo().GetType() != "SkinAKMOriginal" ||
		primary.GetSkin().GetSkinInfo().GetId() != "RU-AKM_Original_PTOriginal" ||
		primary.GetSkin().GetWeaponOrnament() != "WO-NONE" {
		t.Fatalf("primary config=%#v", &primary)
	}
	var secondary map[string]any
	if err := json.Unmarshal(configs["PEACE_RU-APS"], &secondary); err != nil {
		t.Fatal(err)
	}
	if secondary["weapon_id"] != "PEACE_RU-APS" || secondary["parts"] == nil || secondary["skin"] == nil {
		t.Fatalf("default secondary config=%#v", secondary)
	}
	encoded := string(mustJSON(t, projected[0]))
	for _, forbidden := range []string{
		"raw_protobuf", "weapon_archive_raw", "_weaponArchiveRaw",
		"weaponArchives", "deadbeef", "cafebabe", "MELEE-KNIFE\":",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("projected response exposed non-weapon archive data %q: %s", forbidden, encoded)
		}
	}
}

func TestCurrentUserLoadoutsProjectValidatedWeaponConfigs(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	stored := []Loadout{{
		PlayerID: "p_current", RoleID: "PEACE", Revision: 7,
		Snapshot: json.RawMessage(`{
			"primaryWeapon":"PEACE_RU-AKM",
			"secondaryWeapon":"PEACE_RU-APS"
		}`),
	}}
	valid, weaponIDs := validateP2PRoomLoadouts(definitions, stored)
	if len(valid) != 1 || len(weaponIDs) != 2 {
		t.Fatalf("validated loadouts=%#v weapon IDs=%#v", valid, weaponIDs)
	}
	projected := buildCurrentUserRoleLoadouts(definitions, valid, nil)
	if len(projected) != 1 || projected[0].PlayerID != "p_current" ||
		projected[0].RoleID != "PEACE" || projected[0].Revision != 7 {
		t.Fatalf("projected current-user loadouts=%#v", projected)
	}
	if len(projected[0].WeaponConfigs) != 2 {
		t.Fatalf("weapon configs=%s", mustJSON(t, projected[0].WeaponConfigs))
	}
	for _, weaponID := range []string{"PEACE_RU-AKM", "PEACE_RU-APS"} {
		var archive metaprotocol.WeaponArchiveV2
		if err := protojson.Unmarshal(projected[0].WeaponConfigs[weaponID], &archive); err != nil {
			t.Fatal(err)
		}
		if archive.GetWeaponId() != weaponID || len(archive.GetParts()) != 10 {
			t.Fatalf("archive %s=%#v", weaponID, &archive)
		}
	}
	encoded := string(mustJSON(t, projected[0]))
	if !strings.Contains(encoded, `"weapon_configs"`) ||
		strings.Contains(encoded, "weapon_archive_raw") {
		t.Fatalf("current-user projection=%s", encoded)
	}
}

func TestP2PRoomLoadoutsSkipInvalidOrRoleIncompatibleSnapshots(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	stored := []Loadout{
		{RoleID: "UNKNOWN", Revision: 1, Snapshot: json.RawMessage(`{}`)},
		{RoleID: "PEACE", Revision: 1, Snapshot: json.RawMessage(`not-json`)},
		{RoleID: "PEACE", Revision: 1, Snapshot: json.RawMessage(`{"primaryWeapon":"unknown-weapon"}`)},
		{RoleID: "PEACE", Revision: 1, Snapshot: json.RawMessage(`{"primaryWeapon":"SNIPER_GSW-PSR"}`)},
		{RoleID: "PEACE", Revision: 1, Snapshot: json.RawMessage(`{"primaryWeapon":"None"}`)},
	}
	valid, weaponIDs := validateP2PRoomLoadouts(definitions, stored)
	if len(valid) != 1 || len(weaponIDs) != 0 || valid[0].loadout.RoleID != "PEACE" {
		t.Fatalf("valid=%#v weapon IDs=%#v", valid, weaponIDs)
	}
}

func TestP2PRoomLoadoutsReadStructuredInventorySlots(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	stored := []Loadout{{
		RoleID: "PEACE", Revision: 2,
		Snapshot: json.RawMessage(`{
			"inventory":{"slots":[
				{"slot_type":2,"item_id":"PEACE_RU-APS"},
				{"slotType":1,"itemId":{"itemId":"PEACE_RU-AKM"}},
				{"slotType":5,"itemId":"MELEE-KNIFE"}
			]}
		}`),
	}}
	valid, weaponIDs := validateP2PRoomLoadouts(definitions, stored)
	if len(valid) != 1 || len(weaponIDs) != 2 ||
		weaponIDs[0] != "PEACE_RU-AKM" || weaponIDs[1] != "PEACE_RU-APS" {
		t.Fatalf("valid=%#v weapon IDs=%#v", valid, weaponIDs)
	}
}

func TestP2PRoomLoadoutsSanitizeUnverifiableSnapshotCosmetics(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	stored := []Loadout{{
		RoleID: "PEACE", Revision: 3,
		Snapshot: json.RawMessage(`{
			"roleId":"PEACE",
			"primaryWeapon":"PEACE_RU-AKM",
			"meleeWeapon":{"id":"MELEE-KNIFE","skinId":"arbitrary-fname"},
			"leftLauncher":{"id":"PEACE_ATK-HE","skin_id":"WO-NONE"},
			"characterData":{
				"skinClassArray":[1],
				"skinIdArray":["WO-NONE"],
				"skinPaintingId":"WO-NONE"
			},
			"skinModel":"PEACE_ORIGINAL",
			"skinPaint":"PEACE_ORIGINAL_PTOriginal",
			"headOrnament":"HONONE",
			"armBadge":"ABGOrlanDefault",
			"legacy":{"skin_model":"WO-NONE","skin_paint":"PartOri"}
		}`),
	}}
	valid, _ := validateP2PRoomLoadouts(definitions, stored)
	if len(valid) != 1 {
		t.Fatalf("valid loadouts=%#v", valid)
	}
	var cleaned map[string]any
	if err := json.Unmarshal(valid[0].loadout.Snapshot, &cleaned); err != nil {
		t.Fatal(err)
	}
	if cleaned["skinModel"] != "PEACE_ORIGINAL" ||
		cleaned["skinPaint"] != "PEACE_ORIGINAL_PTOriginal" ||
		cleaned["headOrnament"] != "HONONE" ||
		cleaned["armBadge"] != "ABGOrlanDefault" {
		t.Fatalf("validated flat character cosmetics were removed: %#v", cleaned)
	}
	melee := cleaned["meleeWeapon"].(map[string]any)
	launcher := cleaned["leftLauncher"].(map[string]any)
	character := cleaned["characterData"].(map[string]any)
	legacy := cleaned["legacy"].(map[string]any)
	if _, exists := melee["skinId"]; exists || melee["id"] != "MELEE-KNIFE" {
		t.Fatalf("melee cosmetic was not safely stripped: %#v", melee)
	}
	if _, exists := launcher["skin_id"]; exists || launcher["id"] != "PEACE_ATK-HE" {
		t.Fatalf("launcher cosmetic was not safely stripped: %#v", launcher)
	}
	if len(character) != 0 || len(legacy) != 0 {
		t.Fatalf("unverifiable character cosmetics survived: character=%#v legacy=%#v", character, legacy)
	}
}

func TestP2PRoomLoadoutsStripWrongCharacterAppearanceTypes(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	stored := []Loadout{{
		RoleID: "PEACE", Revision: 1,
		Snapshot: json.RawMessage(`{
			"roleId":"PEACE",
			"primaryWeapon":"PEACE_RU-AKM",
			"headOrnament":"ABGOrlanDefault",
			"armBadge":"HONONE"
		}`),
	}}
	valid, _ := validateP2PRoomLoadouts(definitions, stored)
	if len(valid) != 1 {
		t.Fatalf("valid loadouts=%#v", valid)
	}
	var cleaned map[string]any
	if err := json.Unmarshal(valid[0].loadout.Snapshot, &cleaned); err != nil {
		t.Fatal(err)
	}
	if _, exists := cleaned["headOrnament"]; exists {
		t.Fatalf("arm badge survived as head ornament: %#v", cleaned)
	}
	if _, exists := cleaned["armBadge"]; exists {
		t.Fatalf("head ornament survived as arm badge: %#v", cleaned)
	}
}

func TestP2PRoomLoadoutsAcceptPinnedNativeDefaultsForEveryRole(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, roleID := range nativeDefaultRoleOrder {
		snapshot := defaultNativeLoadoutSnapshot(roleID)
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		valid, weaponIDs := validateP2PRoomLoadouts(definitions, []Loadout{{
			RoleID: roleID, Revision: 1, Snapshot: raw,
		}})
		if len(valid) != 1 || len(weaponIDs) != 2 {
			t.Errorf("role %s: valid=%#v weapon IDs=%#v", roleID, valid, weaponIDs)
		}
		for _, weaponID := range weaponIDs {
			archive, ok := nativeDefaultWeaponArchive(definitions, weaponID)
			if !ok || !p2pWeaponArchiveIsValid(definitions, archive) {
				t.Errorf("role %s weapon %s: pinned default archive was rejected", roleID, weaponID)
			}
		}
	}
}

func TestP2PWeaponArchiveAcceptsPinnedNativeOriginalSentinels(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	archive := &metaprotocol.WeaponArchiveV2{
		WeaponId: "PEACE_GSW-AR",
		Parts: []*metaprotocol.PartSlot{
			{SlotId: 1, PartId: "GSW-AR_MZL-BRAKE", Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{Type: "PartOri"}}},
			{SlotId: 2, PartId: "GSW-AR_BRL-AR-STD", Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{Type: "PartOri", Id: "GSW-AR_Barrel_PTOriginal"}}},
			{SlotId: 3, PartId: "GSW-AR_HGD-STD", Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{Type: "PartOri", Id: "GSW-AR_HandGuard_PTOriginal"}}},
			{SlotId: 4, PartId: "GSW-AR_FRM-FULLAUTO", Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{Id: "PTOriginal"}}},
			{SlotId: 5, PartId: "GSW-AR_GRP-GSW-STD", Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{Type: "PartOri", Id: "GSW-AR_Grip_PTOriginal"}}},
			{SlotId: 6, PartId: "GSW-AR_SGT-IRON-FI-MID", Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{Type: "PartOri", Id: "GSW-AR_SightOptical_PTOriginal"}}},
			{SlotId: 7, Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{}}},
			{SlotId: 8, Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{}}},
			{SlotId: 9, PartId: "GSW-AR_MAG-AR-STD", Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{Type: "PartOri", Id: "GSW-AR_AmmoStorageDevice_PTOriginal"}}},
			{SlotId: 10, Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{}}},
		},
		Skin: &metaprotocol.WeaponSkin{
			SkinInfo: &metaprotocol.OrnamentInfo{
				Type: "SkinAROriginal", Id: "GSW-AR_Original_PTOriginal",
			},
			WeaponOrnament: "WO-NONE",
		},
	}
	if !p2pWeaponArchiveIsValid(definitions, archive) {
		t.Fatal("pinned native GSW-AR archive was rejected")
	}

	invalidTypeOnly := proto.Clone(archive).(*metaprotocol.WeaponArchiveV2)
	invalidTypeOnly.Parts[0].Ornament.Info.Type = "WO-NONE"
	if p2pWeaponArchiveIsValid(definitions, invalidTypeOnly) {
		t.Fatal("arbitrary type-only part cosmetic was accepted")
	}
	invalidIDOnly := proto.Clone(archive).(*metaprotocol.WeaponArchiveV2)
	invalidIDOnly.Parts[3].Ornament.Info.Id = "GSW-AR_Muzzle_PTOriginal"
	if p2pWeaponArchiveIsValid(definitions, invalidIDOnly) {
		t.Fatal("arbitrary ID-only part cosmetic was accepted")
	}
}

func TestP2PStructuredWeaponConfigFallsBackForInvalidArchives(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	valid := validP2PAKMArchive()
	mustMarshal := func(archive *metaprotocol.WeaponArchiveV2) []byte {
		t.Helper()
		raw, marshalErr := proto.Marshal(archive)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return raw
	}
	mutated := func(change func(*metaprotocol.WeaponArchiveV2)) []byte {
		archive := proto.Clone(valid).(*metaprotocol.WeaponArchiveV2)
		change(archive)
		return mustMarshal(archive)
	}

	cases := map[string][]byte{
		"corrupt":    {0xff, 0x00},
		"mismatched": mustMarshal(&metaprotocol.WeaponArchiveV2{WeaponId: "PEACE_RU-APS"}),
		"missing":    nil,
		"invalid-slot": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts[0].SlotId = 12
		}),
		"wrong-slot-part": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts[0].PartId = "RU-AKM_BRL-AK-STD"
		}),
		"other-weapon-part": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts[0].PartId = "GSW-DMR_MZL-STD"
		}),
		"empty-required-part": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts[0].PartId = ""
		}),
		"duplicate-slot": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts = append(
				archive.Parts,
				proto.Clone(archive.Parts[0]).(*metaprotocol.PartSlot),
			)
		}),
		"empty-parts": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts = nil
		}),
		"nil-part": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts = []*metaprotocol.PartSlot{nil}
		}),
		"disallowed-suite": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Skin.SkinInfo = &metaprotocol.OrnamentInfo{
				Type: "SkinDMROriginal", Id: "GSW-DMR_Original_PTOriginal",
			}
		}),
		"suite-wrong-item-type": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Skin.SkinInfo.Type = "WO-NONE"
		}),
		"suite-painting-wrong-item-type": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Skin.SkinInfo.Id = "WO-NONE"
		}),
		"suite-half-pair": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Skin.SkinInfo.Id = ""
		}),
		"weapon-ornament-wrong-item-type": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Skin.WeaponOrnament = "PartOri"
		}),
		"weapon-ornament-outside-scope": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Skin.WeaponOrnament = "WO-PEACEDOLL"
		}),
		"part-skin-wrong-item-type": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts[0].Ornament.Info.Type = "WO-NONE"
		}),
		"part-painting-wrong-item-type": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts[0].Ornament.Info.Id = "RU-AKM_Original_PTOriginal"
		}),
		"part-cosmetic-arbitrary-half-pair": mutated(func(archive *metaprotocol.WeaponArchiveV2) {
			archive.Parts[0].Ornament.Info.Type = "WO-NONE"
			archive.Parts[0].Ornament.Info.Id = ""
		}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			config, ok := p2pStructuredWeaponConfig(definitions, "PEACE_RU-AKM", raw)
			if !ok {
				t.Fatal("default config was not produced")
			}
			var decoded metaprotocol.WeaponArchiveV2
			if err := protojson.Unmarshal(config, &decoded); err != nil {
				t.Fatalf("structured config is not protojson: %v", err)
			}
			if decoded.GetWeaponId() != "PEACE_RU-AKM" || len(decoded.GetParts()) != 10 ||
				decoded.GetParts()[0].GetPartId() != "RU-AKM_MZL-STD" ||
				decoded.GetSkin().GetSkinInfo().GetType() != "SkinAKMOriginal" {
				t.Fatalf("archive did not fall back to pinned defaults: %s", config)
			}
		})
	}
}

func TestP2PStructuredWeaponConfigCompletesSkinOnlyPartial(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	partial := &metaprotocol.WeaponArchiveV2{
		WeaponId: "PEACE_RU-AKM",
		Skin: &metaprotocol.WeaponSkin{
			SkinInfo: &metaprotocol.OrnamentInfo{
				Type: "SkinAKMTiger", Id: "SkinAKMTiger_PTTiger",
			},
			WeaponOrnament: "WO-SUN",
		},
	}
	raw, err := proto.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := p2pStructuredWeaponConfig(definitions, "PEACE_RU-AKM", raw)
	if !ok {
		t.Fatal("skin-only partial was not projected")
	}
	var complete metaprotocol.WeaponArchiveV2
	if err := protojson.Unmarshal(config, &complete); err != nil {
		t.Fatal(err)
	}
	if len(complete.GetParts()) != 10 ||
		complete.GetParts()[0].GetPartId() != "RU-AKM_MZL-STD" ||
		complete.GetSkin().GetSkinInfo().GetType() != "SkinAKMTiger" ||
		complete.GetSkin().GetSkinInfo().GetId() != "SkinAKMTiger_PTTiger" ||
		complete.GetSkin().GetWeaponOrnament() != "WO-SUN" {
		t.Fatalf("skin-only partial was not completed correctly: %s", config)
	}
}

func TestP2PStructuredWeaponConfigDefaultsArchiveWithNoEffectiveDelta(t *testing.T) {
	definitions, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(&metaprotocol.WeaponArchiveV2{WeaponId: "PEACE_RU-AKM"})
	if err != nil {
		t.Fatal(err)
	}
	config, ok := p2pStructuredWeaponConfig(definitions, "PEACE_RU-AKM", raw)
	if !ok {
		t.Fatal("pinned default was not projected")
	}
	var complete metaprotocol.WeaponArchiveV2
	if err := protojson.Unmarshal(config, &complete); err != nil {
		t.Fatal(err)
	}
	if len(complete.GetParts()) != 10 ||
		complete.GetSkin().GetSkinInfo().GetType() != "SkinAKMOriginal" ||
		complete.GetSkin().GetWeaponOrnament() != "WO-NONE" {
		t.Fatalf("archive without an effective delta did not fall back: %s", config)
	}
}

func validP2PAKMArchive() *metaprotocol.WeaponArchiveV2 {
	return &metaprotocol.WeaponArchiveV2{
		WeaponId: "PEACE_RU-AKM",
		Parts: []*metaprotocol.PartSlot{{
			SlotId: 1, PartId: "RU-AKM_MZL-STD",
			Ornament: &metaprotocol.PartOrnament{Info: &metaprotocol.OrnamentInfo{
				Type: "PartOri", Id: "RU-AKM_Muzzle_PTOriginal",
			}},
		}},
		Skin: &metaprotocol.WeaponSkin{
			SkinInfo: &metaprotocol.OrnamentInfo{
				Type: "SkinAKMOriginal", Id: "RU-AKM_Original_PTOriginal",
			},
			WeaponOrnament: "WO-NONE",
		},
	}
}

func TestP2PRoomLoadoutEnvelopeSizeLimit(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/meta/p2p-rooms/room/members/player/loadouts", nil)
	small, err := marshalP2PRoomLoadoutEnvelope(request, P2PRoomMemberLoadouts{
		SchemaVersion: 1, RoomID: "room", PlayerID: "player", Loadouts: []P2PRoomRoleLoadout{},
	})
	if err != nil || len(small) >= p2pRoomLoadoutResponseMaxBytes {
		t.Fatalf("small envelope size=%d err=%v", len(small), err)
	}
	large, err := marshalP2PRoomLoadoutEnvelope(request, P2PRoomMemberLoadouts{
		SchemaVersion: 1, RoomID: "room", PlayerID: "player",
		Loadouts: []P2PRoomRoleLoadout{{
			RoleID: "PEACE", Revision: 1,
			Snapshot:      json.RawMessage(`{"padding":"` + strings.Repeat("x", p2pRoomLoadoutResponseMaxBytes) + `"}`),
			WeaponConfigs: map[string]json.RawMessage{},
		}},
	})
	if err != nil || len(large) <= p2pRoomLoadoutResponseMaxBytes {
		t.Fatalf("large envelope size=%d err=%v", len(large), err)
	}
}

func TestP2PRoomLoadoutHandlerRequiresAuthenticatedPrincipal(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet, "/v1/meta/p2p-rooms/room/members/player/loadouts", nil,
	)
	(&HTTPHandler{}).P2PRoomMemberLoadouts(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
