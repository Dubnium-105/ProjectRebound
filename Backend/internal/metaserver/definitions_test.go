package metaserver

import (
	"sort"
	"testing"
)

func TestEmbeddedDefinitionsMatchProvenance(t *testing.T) {
	index, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Roles) == 0 || len(index.Items) == 0 || len(index.Weapons) == 0 || len(index.Parts) == 0 {
		t.Fatalf("incomplete index: roles=%d items=%d weapons=%d parts=%d",
			len(index.Roles), len(index.Items), len(index.Weapons), len(index.Parts))
	}
}

func TestNativeFNameTextValidation(t *testing.T) {
	for _, value := range []string{"PEACE_RU-AKM", "WeaponTest", "皮肤"} {
		if !nativeFNameText(value) {
			t.Fatalf("valid FName text %q was rejected", value)
		}
	}
	for _, value := range []string{"", "bad\x00name", string([]byte{0xff})} {
		if nativeFNameText(value) {
			t.Fatalf("invalid FName text %q was accepted", value)
		}
	}
}

func TestNativeOwnershipItemIDsExcludeOnlyGeneratedPaintApplications(t *testing.T) {
	index, err := LoadDefinitionIndex()
	if err != nil {
		t.Fatal(err)
	}
	compact := index.NativeOwnershipItemIDs(false)
	full := index.NativeOwnershipItemIDs(true)
	if len(compact) != 2741 || len(full) != 40462 {
		t.Fatalf("unexpected ownership sizes: compact=%d full=%d", len(compact), len(full))
	}
	for _, itemID := range compact {
		if _, deferred := nativeDeferredPaintingTypes[index.ItemType(itemID)]; deferred {
			t.Fatalf("compact ownership retained generated paint application %q", itemID)
		}
	}
	for _, required := range []string{
		"PEACE_GSW-AR", "PEACE_RU-AKM", "PEACE_RU-APS", "PEACE_TAC-EMP",
		"PEACE_FCM-GRAPPLE", "PEACE_ORIGINAL", "ABGOrlanDefault",
	} {
		if !containsSortedString(compact, required) {
			t.Fatalf("compact ownership omitted required loadout item %q", required)
		}
	}
}

func containsSortedString(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
