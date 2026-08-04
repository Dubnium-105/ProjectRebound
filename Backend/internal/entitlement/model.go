package entitlement

import "time"

const (
	P2PRoomRegistration      = "p2p_room_registration"
	GameServerRegistration   = "game_server_registration"
	VNTNodeRegistration      = "vnt_node_registration"
	InviteAllowCreateAccount = "allow_create_account"
	InviteAllowP2PRoom       = "allow_p2p_room_registration"
	InviteAllowLegacyP2P     = "allow_p2p"
	InviteAllowGameServer    = "allow_game_server_registration"
	InviteAllowVNTNode       = "allow_vnt_node_registration"
)

var All = []string{
	P2PRoomRegistration,
	GameServerRegistration,
	VNTNodeRegistration,
}

type Grant struct {
	PlayerID          string
	Capability        string
	SourceInviteUseID string
	GrantedAt         time.Time
	ExpiresAt         *time.Time
}

func FromInvitePermissions(permissions map[string]any) []string {
	result := make([]string, 0, len(All))
	if permissionEnabled(permissions, InviteAllowP2PRoom) || permissionEnabled(permissions, InviteAllowLegacyP2P) {
		result = append(result, P2PRoomRegistration)
	}
	if permissionEnabled(permissions, InviteAllowGameServer) {
		result = append(result, GameServerRegistration)
	}
	if permissionEnabled(permissions, InviteAllowVNTNode) {
		result = append(result, VNTNodeRegistration)
	}
	return result
}

func AllowsAccountCreation(permissions map[string]any) bool {
	return permissionEnabled(permissions, InviteAllowCreateAccount)
}

func permissionEnabled(permissions map[string]any, key string) bool {
	value, ok := permissions[key]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}
