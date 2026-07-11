// Package sshkey manages umbra's dedicated ed25519 keypair for machine access.
package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

func Ensure(dir string) (pubLine, privPath string, err error) {
	privPath = filepath.Join(dir, "id_ed25519")
	pubPath := privPath + ".pub"
	if b, rerr := os.ReadFile(pubPath); rerr == nil {
		return strings.TrimSpace(string(b)), privPath, nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "umbra")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(privPath, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " umbra"
	if err := os.WriteFile(pubPath, []byte(line+"\n"), 0o644); err != nil {
		return "", "", err
	}
	return line, privPath, nil
}
