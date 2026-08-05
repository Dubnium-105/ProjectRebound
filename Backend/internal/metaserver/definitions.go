package metaserver

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

const (
	DefinitionsUpstreamCommit  = "d68e717267abf14e32d4e39618f9b7680ed93046"
	DefinitionsAggregateSHA256 = "20393e344e14935535c0eac6815ad82ca051f33caf199281ace4d4bb58391c49"
)

//go:embed assets/definitions/*.json assets/definitions/MANIFEST.sha256
var definitionFiles embed.FS

type DefinitionIndex struct {
	Roles            map[string]struct{}
	Items            map[string]string
	Weapons          map[string]struct{}
	Parts            map[string]struct{}
	roleItems        map[string]map[string]struct{}
	weapons          map[string]definitionWeapon
	roleToBaseWeapon map[string]string
	fileCount        int
}

type definitionWeapon struct {
	slotScopes map[string][]string
	suitScope  []string
}

type DefaultWeaponPart struct {
	SlotID int32
	PartID string
}

type DefaultWeaponArchive struct {
	Parts    []DefaultWeaponPart
	SkinType string
	SkinID   string
}

func LoadDefinitionIndex() (*DefinitionIndex, error) {
	manifest, err := fs.ReadFile(definitionFiles, "assets/definitions/MANIFEST.sha256")
	if err != nil {
		return nil, fmt.Errorf("read embedded definition manifest: %w", err)
	}
	manifest = canonicalSourceBytes(manifest)
	sum := sha256.Sum256(manifest)
	if hex.EncodeToString(sum[:]) != DefinitionsAggregateSHA256 {
		return nil, errors.New("embedded definition aggregate hash does not match provenance")
	}
	expected, err := parseDefinitionManifest(string(manifest))
	if err != nil {
		return nil, err
	}
	index := &DefinitionIndex{
		Roles: make(map[string]struct{}), Items: make(map[string]string),
		Weapons: make(map[string]struct{}), Parts: make(map[string]struct{}),
		roleItems:        make(map[string]map[string]struct{}),
		weapons:          make(map[string]definitionWeapon),
		roleToBaseWeapon: make(map[string]string),
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := fs.ReadFile(definitionFiles, "assets/definitions/"+name)
		if err != nil {
			return nil, fmt.Errorf("read embedded definition %s: %w", name, err)
		}
		raw = canonicalSourceBytes(raw)
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != expected[name] {
			return nil, fmt.Errorf("embedded definition %s failed SHA-256 verification", name)
		}
		if err := index.add(name, raw); err != nil {
			return nil, err
		}
		index.fileCount++
	}
	if index.fileCount != 13 || len(index.Roles) == 0 || len(index.Items) == 0 {
		return nil, errors.New("embedded definition index is incomplete")
	}
	return index, nil
}

func canonicalSourceBytes(raw []byte) []byte {
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}

func parseDefinitionManifest(contents string) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 || !strings.HasSuffix(fields[1], ".json") {
			return nil, errors.New("invalid embedded definition manifest")
		}
		result[fields[1]] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (d *DefinitionIndex) add(name string, raw []byte) error {
	var documents []struct {
		Rows map[string]json.RawMessage `json:"Rows"`
	}
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) == 0 {
		return fmt.Errorf("parse embedded definition %s: %w", name, err)
	}
	rows := documents[0].Rows
	switch name {
	case "DT_CharacterDefinition.json":
		for id, rawRole := range rows {
			d.Roles[id] = struct{}{}
			var role struct {
				CustomScope map[string]json.RawMessage `json:"CustomScope"`
			}
			if json.Unmarshal(rawRole, &role) != nil {
				continue
			}
			allowed := make(map[string]struct{})
			for _, scope := range role.CustomScope {
				var itemIDs []string
				if json.Unmarshal(scope, &itemIDs) != nil {
					continue
				}
				for _, itemID := range itemIDs {
					allowed[itemID] = struct{}{}
				}
			}
			d.roleItems[id] = allowed
		}
	case "DT_WeaponDefinition.json":
		for id, rawWeapon := range rows {
			d.Weapons[id] = struct{}{}
			var weapon struct {
				CustomScope map[string]json.RawMessage `json:"CustomScope"`
			}
			if json.Unmarshal(rawWeapon, &weapon) != nil {
				continue
			}
			definition := definitionWeapon{slotScopes: make(map[string][]string)}
			for _, scopeName := range nativeWeaponArchiveSlotScopes {
				var values []string
				_ = json.Unmarshal(weapon.CustomScope[scopeName], &values)
				definition.slotScopes[scopeName] = values
			}
			_ = json.Unmarshal(weapon.CustomScope["SuitScope"], &definition.suitScope)
			d.weapons[id] = definition
		}
	case "DT_WeaponPartDefinition.json":
		for id := range rows {
			d.Parts[id] = struct{}{}
		}
	case "DT_RawItemType.json":
		for baseWeaponID, rawItem := range rows {
			var item struct {
				Redirections []string `json:"RedirectionItemArray"`
			}
			if json.Unmarshal(rawItem, &item) != nil {
				continue
			}
			for _, roleWeaponID := range item.Redirections {
				d.roleToBaseWeapon[roleWeaponID] = baseWeaponID
			}
		}
	case "DT_ItemType.json":
		for id, rawItem := range rows {
			var item struct {
				Type string `json:"Type"`
			}
			if json.Unmarshal(rawItem, &item) == nil && item.Type != "" {
				d.Items[id] = item.Type
			}
		}
	}
	return nil
}

func (d *DefinitionIndex) HasRole(roleID string) bool {
	_, ok := d.Roles[roleID]
	return ok
}

func (d *DefinitionIndex) CanonicalRoleID(roleID string) (string, bool) {
	if d.HasRole(roleID) {
		return roleID, true
	}
	for candidate := range d.Roles {
		if strings.EqualFold(candidate, roleID) {
			return candidate, true
		}
	}
	return "", false
}

func (d *DefinitionIndex) HasItem(itemID string) bool {
	if itemID == "" || strings.EqualFold(itemID, "none") {
		return true
	}
	if _, ok := d.Items[itemID]; ok {
		return true
	}
	if _, ok := d.Weapons[itemID]; ok {
		return true
	}
	_, ok := d.Parts[itemID]
	return ok
}

func (d *DefinitionIndex) HasWeapon(weaponID string) bool {
	_, ok := d.Weapons[weaponID]
	if ok {
		return true
	}
	itemType := strings.ToLower(d.Items[weaponID])
	return strings.Contains(itemType, "weapon") &&
		!strings.Contains(itemType, "melee")
}

func (d *DefinitionIndex) HasPart(partID string) bool {
	if partID == "" {
		return true
	}
	if _, ok := d.Parts[partID]; ok {
		return true
	}
	// DT_WeaponPartDefinition is keyed by the reusable part definition
	// (for example MZL-STD), while WeaponArchiveV2 and each weapon's slot
	// scopes use the instantiated item ID (for example RU-AKM_MZL-STD).
	// Both are legitimate weapon-part identifiers in their respective native
	// messages. Relationship validation still has to be performed against the
	// owning weapon and slot; HasPart only answers whether the identifier is a
	// known weapon-part item at all.
	return d.ItemType(partID) == "EPBItemType::WeaponPart"
}

func (d *DefinitionIndex) ItemType(itemID string) string {
	return d.Items[itemID]
}

func (d *DefinitionIndex) ItemAllowedForRole(roleID, itemID string) bool {
	allowed := d.roleItems[roleID]
	if len(allowed) == 0 {
		if canonical, ok := d.CanonicalRoleID(roleID); ok {
			allowed = d.roleItems[canonical]
		}
	}
	if len(allowed) == 0 {
		return false
	}
	_, ok := allowed[itemID]
	return ok
}

var nativeWeaponArchiveSlotScopes = []string{
	"MuzzleScope", "BarrelScope", "HandGuardScope", "ReceiverUpperScope",
	"GripScope", "SightOpticalScope", "PointerScope", "SightIronScope",
	"AmmoStorageDeviceScope", "StockScope",
}

func (d *DefinitionIndex) DefaultWeaponArchive(weaponID string) (DefaultWeaponArchive, bool) {
	baseWeaponID := d.roleToBaseWeapon[weaponID]
	if baseWeaponID == "" {
		baseWeaponID = weaponID
	}
	weapon, ok := d.weapons[baseWeaponID]
	if !ok {
		return DefaultWeaponArchive{}, false
	}
	result := DefaultWeaponArchive{Parts: make([]DefaultWeaponPart, 0, len(nativeWeaponArchiveSlotScopes))}
	for index, scopeName := range nativeWeaponArchiveSlotScopes {
		partID := ""
		if values := weapon.slotScopes[scopeName]; len(values) > 0 && !strings.EqualFold(values[0], "none") {
			partID = values[0]
		}
		result.Parts = append(result.Parts, DefaultWeaponPart{
			SlotID: int32(index + 1), PartID: partID,
		})
	}
	if len(weapon.suitScope) > 0 && !strings.EqualFold(weapon.suitScope[0], "none") {
		result.SkinType = weapon.suitScope[0]
		result.SkinID = baseWeaponID + "_Original_PTOriginal"
	}
	return result, true
}

func (d *DefinitionIndex) ItemIDs() []string {
	result := make([]string, 0, len(d.Items))
	for itemID := range d.Items {
		result = append(result, itemID)
	}
	sort.Strings(result)
	return result
}

func (d *DefinitionIndex) ValidateLoadoutSnapshot(
	roleID string,
	snapshot map[string]any,
) error {
	if !d.HasRole(roleID) {
		return errors.New("role is absent from the pinned definition set")
	}
	itemFields := map[string]struct{}{
		"itemid": {}, "primaryweapon": {}, "secondaryweapon": {}, "secondweapon": {},
		"leftpylon": {}, "rightpylon": {}, "leftpod": {}, "rightpod": {},
		"leftlauncher": {}, "rightlauncher": {}, "mobilitymodule": {},
		"meleeweapon": {}, "weaponid": {}, "partid": {}, "skinmodel": {},
		"skinpaint": {}, "armbadge": {}, "headornament": {},
	}
	var visit func(map[string]any) error
	visit = func(object map[string]any) error {
		for key, value := range object {
			normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
			switch normalized {
			case "roleid":
				if valueRole, ok := value.(string); ok && valueRole != "" && valueRole != roleID {
					return errors.New("role_id does not match the resource role")
				}
			case "weaponarchives":
				archives, ok := value.(map[string]any)
				if !ok {
					return errors.New("weapon archives must be an object")
				}
				for weaponID := range archives {
					if !d.HasWeapon(weaponID) {
						return errors.New("snapshot contains an unknown weapon archive")
					}
				}
			default:
				if _, checked := itemFields[normalized]; checked {
					itemID, present := definitionID(value)
					if present && !d.HasItem(itemID) {
						return errors.New("snapshot contains an item absent from pinned definitions")
					}
				}
			}
			switch nested := value.(type) {
			case map[string]any:
				if err := visit(nested); err != nil {
					return err
				}
			case []any:
				for _, entry := range nested {
					if object, ok := entry.(map[string]any); ok {
						if err := visit(object); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	return visit(snapshot)
}
