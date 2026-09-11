package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	for _, k := range []string{"ASC_KEY_ID", "ASC_ISSUER_ID", "ASC_PRIVATE_KEY_PATH", "ASC_APP_IDS",
		"GOOGLE_PLAY_SERVICE_ACCOUNT_JSON", "GOOGLE_PLAY_PACKAGES"} {
		t.Setenv(k, "")
	}
}

func TestUnconfiguredStoresAreSkipped(t *testing.T) {
	clearEnv(t)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"fetch"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
	if out.String() != "[]\n" {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "App Store not configured") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestExplicitStoreWithoutCredentialsFails(t *testing.T) {
	clearEnv(t)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"fetch", "--store", "googleplay"}, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
	if out.String() != "[]\n" || !strings.Contains(errOut.String(), "GOOGLE_PLAY_PACKAGES") {
		t.Errorf("stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestHalfConfiguredStoreFails(t *testing.T) {
	clearEnv(t)
	t.Setenv("ASC_KEY_ID", "K")
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"fetch"}, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "missing ASC_ISSUER_ID") {
		t.Errorf("stderr = %q", errOut.String())
	}
}
