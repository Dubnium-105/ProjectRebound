package metaserver

import (
	"bufio"
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
	DefinitionsAggregateSHA256 = "f1ef4530e25c10f10a3ce735987e2c594c0b81852e63007c5e3ef4d8353f8e2a"
)

//go:embed assets/definitions/*.json assets/definitions/MANIFEST.sha256
var definitionFiles embed.FS

type DefinitionIndex struct {
	Roles     map[string]struct{}
	Items     map[string]string
	Weapons   map[string]struct{}
	Parts     map[string]struct{}
	fileCount int
}

func LoadDefinitionIndex() (*DefinitionIndex, error) {
	manifest, err := fs.ReadFile(definitionFiles, "assets/definitions/MANIFEST.sha256")
	if err != nil {
		return nil, fmt.Errorf("read embedded definition manifest: %w", err)
	}
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
		for id := range rows {
			d.Roles[id] = struct{}{}
		}
	case "DT_WeaponDefinition.json":
		for id := range rows {
			d.Weapons[id] = struct{}{}
		}
	case "DT_WeaponPartDefinition.json":
		for id := range rows {
			d.Parts[id] = struct{}{}
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
	_, ok := d.Parts[partID]
	return ok
}

func (d *DefinitionIndex) ItemType(itemID string) string {
	return d.Items[itemID]
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
		"meleeweapon": {}, "weaponid": {}, "partid": {},
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
