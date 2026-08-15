package metaserver

import "testing"

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
