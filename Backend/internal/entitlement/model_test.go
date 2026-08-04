package entitlement

import (
	"reflect"
	"testing"
)

func TestFromInvitePermissionsKeepsCapabilitiesIndependent(t *testing.T) {
	permissions := map[string]any{
		InviteAllowP2PRoom:    true,
		InviteAllowGameServer: false,
		InviteAllowVNTNode:    true,
	}
	want := []string{P2PRoomRegistration, VNTNodeRegistration}
	if got := FromInvitePermissions(permissions); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromInvitePermissions() = %v, want %v", got, want)
	}
}

func TestLegacyP2PPermissionStillUnlocksRoomRegistration(t *testing.T) {
	got := FromInvitePermissions(map[string]any{InviteAllowLegacyP2P: true})
	if !reflect.DeepEqual(got, []string{P2PRoomRegistration}) {
		t.Fatalf("legacy P2P grant = %v", got)
	}
}
