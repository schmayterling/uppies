package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"uppies/internal/store"
)

// Argon2id cost: 19 MiB of memory, 2 passes, 1 thread, the OWASP minimum.
const (
	argonMemoryKiB = 19456
	argonPasses    = 2
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16
)

var errBadHash = errors.New("stored password hash is malformed")

// hashPassword returns "argon2id$<memory>$<passes>$<threads>$<salt>$<key>".
// The cost is stored with the hash so it can be raised later.
func hashPassword(password string) string {
	salt := make([]byte, argonSaltLen)
	rand.Read(salt) // never fails: crypto/rand.Read aborts the program instead of returning an error
	key := argon2.IDKey([]byte(password), salt, argonPasses, argonMemoryKiB, argonThreads, argonKeyLen)
	encode := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s", argonMemoryKiB, argonPasses, argonThreads, encode(salt), encode(key))
}

// verifyPassword reports whether password matches a hash from hashPassword.
func verifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false, errBadHash
	}
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[1]+" "+parts[2]+" "+parts[3], "%d %d %d", &memory, &passes, &threads); err != nil {
		return false, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, passes, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ResetPassword gives an account a new random password and signs it out on
// every device. It is for the command line, for someone who cannot sign in to
// change the password. It returns the new password, or store.ErrNotFound when
// no account has the name.
func ResetPassword(ctx context.Context, st *store.Store, username string) (string, error) {
	userID, _, err := st.UserPasswordHash(ctx, username)
	if err != nil {
		return "", err
	}
	// 26 characters from crypto/rand, well over minPasswordLen.
	password := rand.Text()
	if err := st.SetPassword(ctx, userID, hashPassword(password), ""); err != nil {
		return "", err
	}
	return password, nil
}
