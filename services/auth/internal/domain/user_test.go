package domain

import (
	"strings"
	"testing"
)

func TestEmailAndPasswordPolicy(t *testing.T) {
	if got, err := Email("  STAFF@Example.Test "); err != nil || got != "staff@example.test" {
		t.Fatal(got, err)
	}
	for _, v := range []string{"not-email", "Name <user@example.test>", "a@b\ncc@example.test"} {
		if _, err := Email(v); err == nil {
			t.Errorf("accepted email %q", v)
		}
	}
	for _, v := range []string{"short", "123456789012345", strings.Repeat("a", 20), "a good\nlong passphrase", strings.Repeat("word", 257)} {
		if ValidatePassword(v) == nil {
			t.Errorf("accepted weak/invalid password")
		}
	}
	for _, v := range []string{"a very long passphrase", strings.Repeat("correct horse ", 50), "Хороший длинный пароль"} {
		if err := ValidatePassword(v); err != nil {
			t.Error(err)
		}
	}
}
func TestBindingsRequireExplicitBusinessIdentities(t *testing.T) {
	valid := User{ID: 10, Subject: "opaque", Roles: []string{"restaurant_staff"}, Restaurants: []int64{1}}
	if err := valid.ValidateBindings(); err != nil {
		t.Fatal(err)
	}
	for _, u := range []User{{Subject: "opaque", Roles: []string{"root"}}, {Subject: "opaque", Roles: []string{"customer"}}, {Subject: "opaque", Roles: []string{"courier"}}, {Subject: "opaque", Restaurants: []int64{1}}, {Subject: "opaque", Roles: []string{"admin"}, Restaurants: []int64{-1}}} {
		if u.ValidateBindings() == nil {
			t.Fatal("invalid identity accepted")
		}
	}
}
