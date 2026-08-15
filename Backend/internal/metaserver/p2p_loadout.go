package metaserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	metaprotocol "github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver/protocol"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	p2pRoomLoadoutSchemaVersion    = 1
	p2pRoomLoadoutResponseMaxBytes = 512 * 1024
)

var (
	p2pWeaponOrnamentScopesOnce sync.Once
	p2pWeaponOrnamentScopes     map[string]map[string]struct{}
)

type validatedP2PRoomLoadout struct {
	loadout   Loadout
	weaponIDs []string
}

func (s *Service) CurrentUserLoadouts(
	ctx context.Context,
	playerID string,
) ([]CurrentUserRoleLoadout, error) {
	if s.definitions == nil {
		return nil, internalError(nil)
	}
	stored, err := s.repository.ListLoadouts(ctx, playerID)
	if err != nil {
		return nil, internalError(err)
	}
	valid, allWeaponIDs := validateP2PRoomLoadouts(s.definitions, stored)
	archives, err := s.repository.GetWeaponArchives(ctx, playerID, allWeaponIDs)
	if err != nil {
		return nil, err
	}
	return buildCurrentUserRoleLoadouts(s.definitions, valid, archives), nil
}

func (s *Service) P2PRoomMemberLoadouts(
	ctx context.Context,
	requesterPlayerID, roomID, targetPlayerID string,
) (P2PRoomMemberLoadouts, error) {
	if !metaLabelPattern.MatchString(roomID) || !metaLabelPattern.MatchString(targetPlayerID) {
		return P2PRoomMemberLoadouts{}, invalid(map[string]any{
			"room_id":   "room_id and player_id must use supported identifier characters",
			"player_id": "room_id and player_id must use supported identifier characters",
		})
	}
	if err := s.repository.AuthorizeP2PRoomLoadoutRead(
		ctx, requesterPlayerID, roomID, targetPlayerID,
	); err != nil {
		return P2PRoomMemberLoadouts{}, err
	}
	if s.definitions == nil {
		return P2PRoomMemberLoadouts{}, internalError(nil)
	}

	stored, err := s.repository.ListLoadouts(ctx, targetPlayerID)
	if err != nil {
		return P2PRoomMemberLoadouts{}, internalError(err)
	}
	valid, allWeaponIDs := validateP2PRoomLoadouts(s.definitions, stored)
	archives, err := s.repository.GetWeaponArchives(ctx, targetPlayerID, allWeaponIDs)
	if err != nil {
		return P2PRoomMemberLoadouts{}, err
	}

	result := P2PRoomMemberLoadouts{
		SchemaVersion: p2pRoomLoadoutSchemaVersion,
		RoomID:        roomID,
		PlayerID:      targetPlayerID,
		Loadouts:      buildP2PRoomRoleLoadouts(s.definitions, valid, archives),
	}
	return result, nil
}

func buildP2PRoomRoleLoadouts(
	definitions *DefinitionIndex,
	valid []validatedP2PRoomLoadout,
	archives map[string][]byte,
) []P2PRoomRoleLoadout {
	result := make([]P2PRoomRoleLoadout, 0, len(valid))
	for _, item := range valid {
		configs, ok := buildStructuredWeaponConfigs(
			definitions, item.weaponIDs, archives,
		)
		if !ok {
			continue
		}
		result = append(result, P2PRoomRoleLoadout{
			RoleID: item.loadout.RoleID, Revision: item.loadout.Revision,
			Snapshot:      append(json.RawMessage(nil), item.loadout.Snapshot...),
			WeaponConfigs: configs,
		})
	}
	return result
}

func buildCurrentUserRoleLoadouts(
	definitions *DefinitionIndex,
	valid []validatedP2PRoomLoadout,
	archives map[string][]byte,
) []CurrentUserRoleLoadout {
	result := make([]CurrentUserRoleLoadout, 0, len(valid))
	for _, item := range valid {
		configs, ok := buildStructuredWeaponConfigs(
			definitions, item.weaponIDs, archives,
		)
		if !ok {
			continue
		}
		result = append(result, CurrentUserRoleLoadout{
			PlayerID: item.loadout.PlayerID, RoleID: item.loadout.RoleID,
			Snapshot: append(json.RawMessage(nil), item.loadout.Snapshot...),
			Revision: item.loadout.Revision, UpdatedAt: item.loadout.UpdatedAt,
			WeaponConfigs: configs,
		})
	}
	return result
}

func buildStructuredWeaponConfigs(
	definitions *DefinitionIndex,
	weaponIDs []string,
	archives map[string][]byte,
) (map[string]json.RawMessage, bool) {
	configs := make(map[string]json.RawMessage, len(weaponIDs))
	for _, weaponID := range weaponIDs {
		config, ok := p2pStructuredWeaponConfig(
			definitions, weaponID, archives[weaponID],
		)
		if !ok {
			return nil, false
		}
		configs[weaponID] = config
	}
	return configs, true
}

func validateP2PRoomLoadouts(
	definitions *DefinitionIndex,
	stored []Loadout,
) ([]validatedP2PRoomLoadout, []string) {
	valid := make([]validatedP2PRoomLoadout, 0, len(stored))
	allWeaponIDs := make([]string, 0, len(stored)*2)
	seenWeapons := make(map[string]struct{}, len(stored)*2)
	for _, item := range stored {
		if !definitions.HasRole(item.RoleID) || item.Revision <= 0 {
			continue
		}
		var snapshot map[string]any
		if err := json.Unmarshal(item.Snapshot, &snapshot); err != nil || snapshot == nil {
			continue
		}
		if err := definitions.ValidateLoadoutSnapshot(item.RoleID, snapshot); err != nil {
			continue
		}
		weaponIDs, ok := p2pReferencedWeaponIDs(definitions, item.RoleID, snapshot)
		if !ok {
			continue
		}
		stripP2PEmbeddedWeaponConfigs(snapshot)
		sanitizeP2PSnapshotCosmetics(definitions, snapshot)
		cleanSnapshot, err := json.Marshal(snapshot)
		if err != nil {
			continue
		}
		item.Snapshot = cleanSnapshot
		valid = append(valid, validatedP2PRoomLoadout{loadout: item, weaponIDs: weaponIDs})
		for _, weaponID := range weaponIDs {
			if _, duplicate := seenWeapons[weaponID]; duplicate {
				continue
			}
			seenWeapons[weaponID] = struct{}{}
			allWeaponIDs = append(allWeaponIDs, weaponID)
		}
	}
	return valid, allWeaponIDs
}

func sanitizeP2PSnapshotCosmetics(definitions *DefinitionIndex, object map[string]any) {
	for key, value := range object {
		normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
		switch normalized {
		case "skinid":
			// The pinned definitions expose no distinct melee/launcher skin
			// category or relationship. Do not project an unprovable FName.
			delete(object, key)
			continue
		case "skinidarray", "skinclassarray":
			// Structured character skin arrays require a class-to-resource
			// relationship that is not present in the pinned export. The
			// validated flat skinModel/skinPaint fields remain supported.
			delete(object, key)
			continue
		case "skinmodel":
			if !p2pSnapshotCosmeticHasType(
				definitions, value, "EPBItemType::SpaceSuit",
			) {
				delete(object, key)
				continue
			}
		case "skinpaint", "skinpaintingid":
			if !p2pSnapshotCosmeticHasType(
				definitions, value, "EPBItemType::CharacterSuitePainting",
			) {
				delete(object, key)
				continue
			}
		}
		switch nested := value.(type) {
		case map[string]any:
			sanitizeP2PSnapshotCosmetics(definitions, nested)
		case []any:
			for _, entry := range nested {
				if child, ok := entry.(map[string]any); ok {
					sanitizeP2PSnapshotCosmetics(definitions, child)
				}
			}
		}
	}
}

func p2pSnapshotCosmeticHasType(
	definitions *DefinitionIndex,
	value any,
	expectedType string,
) bool {
	id, ok := value.(string)
	return ok && id != "" && definitions.ItemType(id) == expectedType
}

func stripP2PEmbeddedWeaponConfigs(object map[string]any) {
	for key, value := range object {
		normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
		switch normalized {
		case "weaponarchives", "weaponarchiveraw", "weaponconfigs":
			delete(object, key)
			continue
		}
		switch nested := value.(type) {
		case map[string]any:
			stripP2PEmbeddedWeaponConfigs(nested)
		case []any:
			for _, entry := range nested {
				if child, ok := entry.(map[string]any); ok {
					stripP2PEmbeddedWeaponConfigs(child)
				}
			}
		}
	}
}

func p2pReferencedWeaponIDs(
	definitions *DefinitionIndex,
	roleID string,
	snapshot map[string]any,
) ([]string, bool) {
	result := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, weaponID := range []string{
		p2pSnapshotWeaponID(snapshot, 1, "primaryWeapon", "primary_weapon"),
		p2pSnapshotWeaponID(snapshot, 2, "secondaryWeapon", "secondary_weapon", "secondWeapon", "second_weapon"),
	} {
		weaponID = strings.TrimSpace(weaponID)
		if weaponID == "" || strings.EqualFold(weaponID, "none") {
			continue
		}
		if !definitions.HasWeapon(weaponID) || !definitions.ItemAllowedForRole(roleID, weaponID) {
			return nil, false
		}
		if _, duplicate := seen[weaponID]; duplicate {
			continue
		}
		seen[weaponID] = struct{}{}
		result = append(result, weaponID)
	}
	return result, true
}

func p2pSnapshotWeaponID(snapshot map[string]any, slot int, keys ...string) string {
	if value := nativeSnapshotString(snapshot, keys...); value != "" {
		return value
	}
	if value := nativeSnapshotResponseItem(snapshot, keys[0], slot); value != "" {
		return value
	}
	inventory, _ := snapshot["inventory"].(map[string]any)
	slots, _ := inventory["slots"].([]any)
	for _, value := range slots {
		entry, _ := value.(map[string]any)
		slotValue, ok := entry["slotType"]
		if !ok {
			slotValue = entry["slot_type"]
		}
		var slotNumber int
		switch typed := slotValue.(type) {
		case float64:
			slotNumber = int(typed)
		case int:
			slotNumber = typed
		}
		if slotNumber != slot {
			continue
		}
		for _, itemKey := range []string{"itemId", "item_id"} {
			if itemID, ok := definitionID(entry[itemKey]); ok {
				return itemID
			}
		}
	}
	return ""
}

func p2pStructuredWeaponConfig(
	definitions *DefinitionIndex,
	weaponID string,
	raw []byte,
) (json.RawMessage, bool) {
	var archive *metaprotocol.WeaponArchiveV2
	if len(raw) > 0 {
		candidate := new(metaprotocol.WeaponArchiveV2)
		if proto.Unmarshal(raw, candidate) == nil &&
			candidate.GetWeaponId() == weaponID &&
			p2pWeaponArchiveIsValid(definitions, candidate) {
			archive, _ = p2pCompleteWeaponArchive(definitions, weaponID, candidate)
		}
	}
	if archive == nil {
		var ok bool
		archive, ok = nativeDefaultWeaponArchive(definitions, weaponID)
		if !ok {
			return nil, false
		}
	}
	encoded, err := (protojson.MarshalOptions{
		UseProtoNames: true, EmitUnpopulated: true,
	}).Marshal(archive)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(encoded), true
}

func p2pCompleteWeaponArchive(
	definitions *DefinitionIndex,
	weaponID string,
	candidate *metaprotocol.WeaponArchiveV2,
) (*metaprotocol.WeaponArchiveV2, bool) {
	complete, ok := nativeDefaultWeaponArchive(definitions, weaponID)
	if !ok {
		return nil, false
	}
	if candidate == nil {
		return complete, true
	}

	// A native archive can contain only the slots changed by the player. Build
	// a complete config from the pinned default before applying those entries;
	// otherwise Payload's InitWeapon would interpret omitted mandatory slots as
	// removals rather than inheritance.
	indices := make(map[int32]int, len(complete.GetParts()))
	for index, part := range complete.GetParts() {
		indices[part.GetSlotId()] = index
	}
	for _, part := range candidate.GetParts() {
		index, exists := indices[part.GetSlotId()]
		if !exists {
			return nil, false
		}
		complete.Parts[index] = proto.Clone(part).(*metaprotocol.PartSlot)
	}

	if skin := candidate.GetSkin(); skin != nil {
		if info := skin.GetSkinInfo(); info != nil &&
			(info.GetType() != "" || info.GetId() != "") {
			complete.Skin.SkinInfo = proto.Clone(info).(*metaprotocol.OrnamentInfo)
		}
		if ornamentID := skin.GetWeaponOrnament(); ornamentID != "" {
			complete.Skin.WeaponOrnament = ornamentID
		}
	}
	return complete, true
}

func p2pWeaponArchiveIsValid(
	definitions *DefinitionIndex,
	archive *metaprotocol.WeaponArchiveV2,
) bool {
	if archive == nil || !definitions.HasWeapon(archive.GetWeaponId()) {
		return false
	}
	baseWeaponID := definitions.roleToBaseWeapon[archive.GetWeaponId()]
	if baseWeaponID == "" {
		baseWeaponID = archive.GetWeaponId()
	}
	weapon, ok := definitions.weapons[baseWeaponID]
	if !ok ||
		len(archive.GetParts()) > len(nativeWeaponArchiveSlotScopes) ||
		!p2pWeaponArchiveHasEffectiveDelta(archive) ||
		!p2pWeaponSkinIsValid(definitions, baseWeaponID, weapon, archive.GetSkin()) {
		return false
	}
	seenSlots := make(map[int32]struct{}, len(archive.GetParts()))
	for _, part := range archive.GetParts() {
		if part == nil {
			return false
		}
		slotID := part.GetSlotId()
		if slotID < 1 || slotID > int32(len(nativeWeaponArchiveSlotScopes)) ||
			!p2pWeaponPartIsAllowedForSlot(weapon, slotID, part.GetPartId()) {
			return false
		}
		if _, duplicate := seenSlots[slotID]; duplicate {
			return false
		}
		if ornament := part.GetOrnament(); ornament != nil &&
			!p2pWeaponPartOrnamentIsValid(
				definitions, slotID, ornament.GetInfo(),
			) {
			return false
		}
		seenSlots[slotID] = struct{}{}
	}
	return true
}

func p2pWeaponPartOrnamentIsValid(
	definitions *DefinitionIndex,
	slotID int32,
	info *metaprotocol.OrnamentInfo,
) bool {
	if info == nil {
		return true
	}
	typeID, itemID := info.GetType(), info.GetId()
	if typeID == "" && itemID == "" {
		return true
	}
	// The pinned native client does not always serialize original part
	// cosmetics as a complete inventory-backed pair. A newly selected part can
	// carry the built-in PartOri suite without a painting ID, while the fire
	// mode/receiver slot serializes its built-in painting as bare PTOriginal.
	// These are native reset sentinels, not arbitrary half-pairs.
	if typeID == "PartOri" && itemID == "" {
		return true
	}
	if slotID == 4 && typeID == "" && itemID == "PTOriginal" {
		return true
	}
	return p2pCosmeticPairIsValid(
		definitions, info,
		"EPBItemType::WeaponPartSkin",
		"EPBItemType::WeaponSlotPainting",
	)
}

func p2pWeaponArchiveHasEffectiveDelta(archive *metaprotocol.WeaponArchiveV2) bool {
	if archive == nil {
		return false
	}
	if len(archive.GetParts()) > 0 {
		return true
	}
	skin := archive.GetSkin()
	if skin == nil {
		return false
	}
	if info := skin.GetSkinInfo(); info != nil &&
		(info.GetType() != "" || info.GetId() != "") {
		return true
	}
	// WO-NONE is a meaningful explicit reset, so any non-empty ornament ID is
	// an effective skin-only update as well.
	return skin.GetWeaponOrnament() != ""
}

func p2pWeaponPartIsAllowedForSlot(
	weapon definitionWeapon,
	slotID int32,
	partID string,
) bool {
	scope := weapon.slotScopes[nativeWeaponArchiveSlotScopes[slotID-1]]
	if partID == "" {
		// An empty part is valid only for a definition slot that has no
		// selectable part. Otherwise an archive could silently remove a
		// mandatory/default component.
		for _, candidate := range scope {
			if candidate != "" && !strings.EqualFold(candidate, "none") {
				return false
			}
		}
		return true
	}
	for _, candidate := range scope {
		if candidate == partID {
			return true
		}
	}
	return false
}

func p2pWeaponSkinIsValid(
	definitions *DefinitionIndex,
	baseWeaponID string,
	weapon definitionWeapon,
	skin *metaprotocol.WeaponSkin,
) bool {
	if skin == nil {
		return true
	}
	info := skin.GetSkinInfo()
	if !p2pCosmeticPairIsValid(
		definitions, info,
		"EPBItemType::WeaponSuit",
		"EPBItemType::WeaponSuitePainting",
	) {
		return false
	}
	if info != nil && info.GetType() != "" &&
		!p2pStringInDefinitionScope(weapon.suitScope, info.GetType()) {
		return false
	}
	ornamentID := skin.GetWeaponOrnament()
	return ornamentID == "" ||
		definitions.ItemType(ornamentID) == "EPBItemType::WeaponOrnament" &&
			p2pWeaponOrnamentIsAllowed(baseWeaponID, ornamentID)
}

func p2pWeaponOrnamentIsAllowed(baseWeaponID string, ornamentID string) bool {
	p2pWeaponOrnamentScopesOnce.Do(func() {
		p2pWeaponOrnamentScopes = make(map[string]map[string]struct{})
		raw, err := definitionFiles.ReadFile(
			"assets/definitions/DT_WeaponDefinition.json",
		)
		if err != nil {
			return
		}
		var documents []struct {
			Rows map[string]struct {
				CustomScope struct {
					OrnamentScope []string `json:"OrnamentScope"`
				} `json:"CustomScope"`
			} `json:"Rows"`
		}
		if json.Unmarshal(raw, &documents) != nil || len(documents) == 0 {
			return
		}
		for weaponID, definition := range documents[0].Rows {
			scope := make(map[string]struct{}, len(definition.CustomScope.OrnamentScope))
			for _, allowed := range definition.CustomScope.OrnamentScope {
				scope[allowed] = struct{}{}
			}
			p2pWeaponOrnamentScopes[weaponID] = scope
		}
	})
	scope, ok := p2pWeaponOrnamentScopes[baseWeaponID]
	if !ok {
		return false
	}
	_, ok = scope[ornamentID]
	return ok
}

func p2pCosmeticPairIsValid(
	definitions *DefinitionIndex,
	info *metaprotocol.OrnamentInfo,
	expectedTypeType string,
	expectedIDType string,
) bool {
	if info == nil {
		return true
	}
	typeID, itemID := info.GetType(), info.GetId()
	if typeID == "" || itemID == "" {
		return typeID == "" && itemID == ""
	}
	return definitions.ItemType(typeID) == expectedTypeType &&
		definitions.ItemType(itemID) == expectedIDType
}

func p2pStringInDefinitionScope(scope []string, value string) bool {
	for _, candidate := range scope {
		if candidate == value {
			return true
		}
	}
	return false
}

func (h *HTTPHandler) P2PRoomMemberLoadouts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(
			w, r, http.StatusUnauthorized, auth.CodeUnauthorized,
			"Authentication is required.", nil,
		)
		return
	}
	item, err := h.service.P2PRoomMemberLoadouts(
		r.Context(), principal.Player.ID,
		chi.URLParam(r, "room_id"), chi.URLParam(r, "player_id"),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	encoded, err := marshalP2PRoomLoadoutEnvelope(r, item)
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	if len(encoded) > p2pRoomLoadoutResponseMaxBytes {
		h.writeError(w, r, &ServiceError{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    "META_P2P_LOADOUT_RESPONSE_TOO_LARGE",
			Message: "The room member loadout response exceeds the supported size limit.",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func marshalP2PRoomLoadoutEnvelope(
	r *http.Request,
	item P2PRoomMemberLoadouts,
) ([]byte, error) {
	return json.Marshal(api.SuccessEnvelope{
		Data: item, RequestID: requestctx.RequestID(r.Context()),
	})
}
