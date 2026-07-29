package metaserver

import (
	"encoding/json"
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

func TestNativeWeaponArchiveBundle(t *testing.T) {
	archive, err := proto.Marshal(&metaprotocol.WeaponArchiveV2{WeaponId: "weapon-a"})
	if err != nil {
		t.Fatal(err)
	}
	bundle := nativeWeaponArchiveBundle(
		"role-a", []string{"weapon-a"}, map[string][]byte{"weapon-a": archive},
	)
	roleID, ok := consumeStringField(bundle, 1)
	if !ok || roleID != "role-a" {
		t.Fatalf("role id=%q ok=%v", roleID, ok)
	}
	fields := 0
	for len(bundle) > 0 {
		number, typ, n := protowire.ConsumeTag(bundle)
		if n < 0 {
			t.Fatal("invalid bundle tag")
		}
		bundle = bundle[n:]
		if typ != protowire.BytesType {
			t.Fatal("unexpected wire type")
		}
		_, n = protowire.ConsumeBytes(bundle)
		if n < 0 {
			t.Fatal("invalid bundle value")
		}
		if number == 3 {
			fields++
		}
		bundle = bundle[n:]
	}
	if fields != 1 {
		t.Fatalf("weapon archive fields=%d", fields)
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
