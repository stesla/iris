// Package secret encrypts values, such as upstream passwords, that Iris must
// store in the database but be able to read back.
package secret

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/chacha20poly1305"
)

const KeySize = chacha20poly1305.KeySize

// version is the first byte of every sealed value, so the format can change
// later without a schema change.
const version byte = 1

var (
	ErrDecrypt            = errors.New("secret: unable to decrypt value")
	ErrUnsupportedVersion = errors.New("secret: unsupported version")
)

// Box seals and opens values with a single key. Every value is bound to the
// ID of the row it belongs to, so it can't be opened if copied to another row.
type Box struct {
	aead cipher.AEAD
}

func NewBox(key []byte) (*Box, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("secret: %w", err)
	}
	return &Box{aead: aead}, nil
}

// LoadOrCreateKey reads the key at path, creating it with a new random key if
// it doesn't exist. The key file holds the key hex-encoded, so it can be
// copied into a password manager, and must only be accessible by its owner.
func LoadOrCreateKey(path string) (*Box, error) {
	key, err := readKey(path)
	if errors.Is(err, fs.ErrNotExist) {
		key, err = createKey(path)
	}
	if err != nil {
		return nil, err
	}
	return NewBox(key)
}

func readKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secret: key file %s has mode %v, must not be accessible by group or others", path, info.Mode().Perm())
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// the decode error isn't wrapped because it would include part of the key
	key, err := hex.DecodeString(string(bytes.TrimSpace(encoded)))
	if err != nil || len(key) != KeySize {
		return nil, fmt.Errorf("secret: key file %s must contain a %d-byte key as %d hex digits", path, KeySize, 2*KeySize)
	}
	return key, nil
}

func createKey(path string) ([]byte, error) {
	key := make([]byte, KeySize)
	rand.Read(key)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(f, "%x\n", key); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	// sync the file and its directory before anything is sealed with the key,
	// so a crash can't leave ciphertexts in the database and the key file
	// empty or missing
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		os.Remove(path)
		return nil, err
	}
	return key, nil
}

// syncDir makes a new directory entry durable. Windows can't sync a
// directory, and doesn't need to.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

// Seal encrypts plaintext for the row with the given ID. The result is the
// version byte, then the nonce, then the ciphertext.
func (b *Box) Seal(id int64, plaintext []byte) []byte {
	out := make([]byte, 1+b.aead.NonceSize(), 1+b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	out[0] = version
	rand.Read(out[1:])
	return b.aead.Seal(out, out[1:], plaintext, additionalData(version, id))
}

// Open decrypts a value sealed for the row with the given ID.
func (b *Box) Open(id int64, sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, ErrDecrypt
	}
	v := sealed[0]
	if v != version {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVersion, v)
	}
	sealed = sealed[1:]
	if len(sealed) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, ErrDecrypt
	}
	nonce, ciphertext := sealed[:b.aead.NonceSize()], sealed[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, additionalData(v, id))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// additionalData covers the version byte as well as the ID, so changing the
// version byte of a sealed value also makes it fail to open. It takes the
// version of the value being sealed or opened, not the current version, so
// older values still open once there is more than one.
func additionalData(v byte, id int64) []byte {
	ad := make([]byte, 9)
	ad[0] = v
	binary.BigEndian.PutUint64(ad[1:], uint64(id))
	return ad
}
