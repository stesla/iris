package secret

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestBox(t *testing.T) *Box {
	t.Helper()
	box, err := NewBox(bytes.Repeat([]byte{7}, KeySize))
	require.NoError(t, err)
	return box
}

func TestSealOpen(t *testing.T) {
	box := newTestBox(t)
	sealed := box.Seal(42, []byte("hunter2"))
	require.Equal(t, version, sealed[0])
	require.NotContains(t, string(sealed), "hunter2")

	plaintext, err := box.Open(42, sealed)
	require.NoError(t, err)
	require.Equal(t, "hunter2", string(plaintext))
}

func TestSealEmpty(t *testing.T) {
	box := newTestBox(t)
	plaintext, err := box.Open(1, box.Seal(1, nil))
	require.NoError(t, err)
	require.Empty(t, plaintext)
}

func TestSealUsesFreshNonce(t *testing.T) {
	box := newTestBox(t)
	require.NotEqual(t, box.Seal(1, []byte("same")), box.Seal(1, []byte("same")))
}

func TestOpenWrongID(t *testing.T) {
	box := newTestBox(t)
	_, err := box.Open(2, box.Seal(1, []byte("hunter2")))
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestOpenWrongKey(t *testing.T) {
	other, err := NewBox(bytes.Repeat([]byte{8}, KeySize))
	require.NoError(t, err)
	_, err = other.Open(1, newTestBox(t).Seal(1, []byte("hunter2")))
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestOpenTampered(t *testing.T) {
	box := newTestBox(t)
	sealed := box.Seal(1, []byte("hunter2"))
	// start after the version byte: changing it gives ErrUnsupportedVersion,
	// which TestOpenMalformed checks
	for i := 1; i < len(sealed); i++ {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 1
		_, err := box.Open(1, tampered)
		require.ErrorIs(t, err, ErrDecrypt, "byte %d", i)
	}
}

func TestOpenMalformed(t *testing.T) {
	box := newTestBox(t)
	sealed := box.Seal(1, []byte("hunter2"))

	_, err := box.Open(1, nil)
	require.ErrorIs(t, err, ErrDecrypt)

	_, err = box.Open(1, sealed[:1+box.aead.NonceSize()])
	require.ErrorIs(t, err, ErrDecrypt)

	bad := bytes.Clone(sealed)
	bad[0] = 99
	_, err = box.Open(1, bad)
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestNewBoxBadKey(t *testing.T) {
	_, err := NewBox(make([]byte, 16))
	require.Error(t, err)
}

func TestLoadOrCreateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iris.key")

	box, err := LoadOrCreateKey(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	encoded, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Regexp(t, `^[0-9a-f]{64}\n$`, string(encoded))

	sealed := box.Seal(1, []byte("hunter2"))
	reloaded, err := LoadOrCreateKey(path)
	require.NoError(t, err)
	plaintext, err := reloaded.Open(1, sealed)
	require.NoError(t, err)
	require.Equal(t, "hunter2", string(plaintext))
}

func TestLoadKeyBadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iris.key")
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(make([]byte, KeySize))), 0o644))
	_, err := LoadOrCreateKey(path)
	require.ErrorContains(t, err, "must not be accessible")
}

func TestLoadKeyReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iris.key")
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(make([]byte, KeySize))), 0o400))
	_, err := LoadOrCreateKey(path)
	require.NoError(t, err)
}

func TestLoadKeyWithoutNewline(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	path := filepath.Join(t.TempDir(), "iris.key")
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600))
	box, err := LoadOrCreateKey(path)
	require.NoError(t, err)
	plaintext, err := box.Open(1, newTestBox(t).Seal(1, []byte("hunter2")))
	require.NoError(t, err)
	require.Equal(t, "hunter2", string(plaintext))
}

func TestLoadKeyMalformed(t *testing.T) {
	for name, contents := range map[string]string{
		"short":  hex.EncodeToString(make([]byte, KeySize-1)),
		"long":   hex.EncodeToString(make([]byte, KeySize+1)),
		"raw":    string(make([]byte, KeySize)),
		"notHex": "zz" + hex.EncodeToString(make([]byte, KeySize-1)),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "iris.key")
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
			_, err := LoadOrCreateKey(path)
			require.ErrorContains(t, err, "must contain a 32-byte key as 64 hex digits")
		})
	}
}

func TestCreateKeyMissingDir(t *testing.T) {
	_, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "missing", "iris.key"))
	require.Error(t, err)
}
