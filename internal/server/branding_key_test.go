package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrandingSecretKeyAliasesPreserveStoredKey(t *testing.T) {
	for _, tc := range []struct {
		name, canonical, legacy, explicit, want string
		conflict                                bool
	}{
		{name: "legacy", legacy: "synthetic-legacy-key", want: "synthetic-legacy-key"},
		{name: "canonical", canonical: "synthetic-new-key", want: "synthetic-new-key"},
		{name: "blank canonical", canonical: " ", legacy: "synthetic-legacy-key", want: "synthetic-legacy-key"},
		{name: "equal", canonical: " synthetic-same-key ", legacy: "synthetic-same-key", want: "synthetic-same-key"},
		{name: "conflict", canonical: "synthetic-new-key", legacy: "synthetic-old-key", conflict: true},
		{name: "explicit", canonical: "synthetic-new-key", legacy: "synthetic-old-key", explicit: "synthetic-explicit-key", want: "synthetic-explicit-key"},
		{name: "stored", want: "synthetic-stored-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LINKSPHERE_SECRET_KEY", tc.canonical)
			t.Setenv("GOSSHD_SECRET_KEY", tc.legacy)
			path := filepath.Join(t.TempDir(), "gosshd.db")
			stored := []byte("synthetic-stored-key")
			if err := os.WriteFile(path+".secret-key", stored, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := (&App{cfg: Config{DatabasePath: path, SecretKey: tc.explicit}}).loadSecretKey()
			if tc.conflict {
				if err == nil || got != nil {
					t.Fatal("conflicting aliases accepted")
				}
				if strings.Contains(err.Error(), tc.canonical) || strings.Contains(err.Error(), tc.legacy) {
					t.Fatal("key exposed in error")
				}
			} else if err != nil || string(got) != tc.want {
				t.Fatalf("unexpected key resolution: %v", err)
			}
			after, readErr := os.ReadFile(path + ".secret-key")
			if readErr != nil || !bytes.Equal(after, stored) {
				t.Fatal("stored key changed")
			}
		})
	}
}

func TestBrandingSecretConflictDoesNotGenerateKey(t *testing.T) {
	t.Setenv("LINKSPHERE_SECRET_KEY", "synthetic-new-key")
	t.Setenv("GOSSHD_SECRET_KEY", "synthetic-old-key")
	path := filepath.Join(t.TempDir(), "gosshd.db")
	if key, err := (&App{cfg: Config{DatabasePath: path}}).loadSecretKey(); err == nil || key != nil {
		t.Fatal("conflict accepted")
	}
	if _, err := os.Stat(path + ".secret-key"); !os.IsNotExist(err) {
		t.Fatal("replacement key generated")
	}
}
