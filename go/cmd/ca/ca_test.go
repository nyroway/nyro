package ca

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/nyroway/nyro/go/internal/configsync/pki"
)

func runCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewCmd()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestInit_GeneratesCA(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCmd(t, "init", "--dir", dir); err != nil {
		t.Fatalf("ca init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.pem")); err != nil {
		t.Fatalf("ca.pem not written: %v", err)
	}
}

func TestInit_ReuseWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCmd(t, "init", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	before, err := pki.LoadCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, "init", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	after, err := pki.LoadCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before.Cert.SerialNumber.Cmp(after.Cert.SerialNumber) != 0 {
		t.Fatal("expected `ca init` without --force to reuse the existing CA")
	}
}

func TestInit_Force(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCmd(t, "init", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	before, err := pki.LoadCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, "init", "--dir", dir, "--force"); err != nil {
		t.Fatal(err)
	}
	after, err := pki.LoadCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before.Cert.SerialNumber.Cmp(after.Cert.SerialNumber) == 0 {
		t.Fatal("expected --force to regenerate the CA (different serial)")
	}
}

func TestSignAdmin_WritesCertWithAdminIdentity(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCmd(t, "init", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCmd(t, "sign-admin", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if stdout == "" {
		t.Fatal("expected sign-admin to report the identity it signed")
	}
	certPath := filepath.Join(dir, "admin.pem")
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("admin.pem not written: %v", err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := pki.VerifyAdminIdentity(cert); err != nil {
		t.Errorf("VerifyAdminIdentity: %v", err)
	}
}

func TestSignAdmin_RequiresExistingCA(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCmd(t, "sign-admin", "--dir", dir); err == nil {
		t.Fatal("expected error signing without a CA present")
	}
}

func TestSignGateway_RandomNodeIDWhenUnset(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCmd(t, "init", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCmd(t, "sign-gateway", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	if stdout == "" {
		t.Fatal("expected sign-gateway to report the generated node-id")
	}
	certPath := filepath.Join(dir, "gateway.pem")
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("gateway.pem not written: %v", err)
	}
}

func TestSignGateway_CustomOutName(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCmd(t, "init", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, "sign-gateway", "--dir", dir, "--node-id", "node-a", "--out", "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node-a.pem")); err != nil {
		t.Fatalf("node-a.pem not written: %v", err)
	}
}
