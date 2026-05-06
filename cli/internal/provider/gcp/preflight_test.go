package gcp

import (
	"context"
	"strings"
	"testing"
)

func TestPreflightAllOK(t *testing.T) {
	c := &captureExec{
		resp: []captureCall{
			// First call: gcloud auth list
			{exitCode: 0, stdout: []byte("onyrix.ai@gmail.com\n")},
			// Second call: gcloud services list
			{exitCode: 0, stdout: []byte("compute.googleapis.com\n")},
		},
	}
	p := newProvider(t, c)
	if err := p.Preflight(context.Background()); err != nil {
		t.Errorf("Preflight: %v", err)
	}
	if len(c.calls) != 2 {
		t.Errorf("expected 2 gcloud calls, got %d", len(c.calls))
	}
}

func TestPreflightAuthMissing(t *testing.T) {
	c := &captureExec{
		resp: []captureCall{
			{exitCode: 0, stdout: []byte("")}, // no active account
			{exitCode: 0, stdout: []byte("compute.googleapis.com\n")},
		},
	}
	p := newProvider(t, c)
	err := p.Preflight(context.Background())
	if err == nil {
		t.Fatal("expected error when no active gcloud account")
	}
	if !strings.Contains(err.Error(), "gcloud auth login") {
		t.Errorf("err message should hint at `gcloud auth login`, got: %v", err)
	}
}

func TestPreflightAPIDisabled(t *testing.T) {
	c := &captureExec{
		resp: []captureCall{
			{exitCode: 0, stdout: []byte("u@g.com\n")},
			// services list returns empty: API not enabled
			{exitCode: 0, stdout: []byte("")},
		},
	}
	p := newProvider(t, c)
	err := p.Preflight(context.Background())
	if err == nil {
		t.Fatal("expected error when Compute API not enabled")
	}
	for _, want := range []string{
		"Compute Engine API not enabled",
		"gcloud services enable compute.googleapis.com",
		"--project=example-project",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err should contain %q\nfull: %v", want, err)
		}
	}
}

func TestPreflightBothFail(t *testing.T) {
	c := &captureExec{
		resp: []captureCall{
			{exitCode: 0, stdout: []byte("")},
			{exitCode: 0, stdout: []byte("")},
		},
	}
	p := newProvider(t, c)
	err := p.Preflight(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	// Aggregated error mentions both problems on separate lines.
	if !strings.Contains(err.Error(), "gcloud auth login") {
		t.Errorf("missing auth hint: %v", err)
	}
	if !strings.Contains(err.Error(), "compute.googleapis.com") {
		t.Errorf("missing API hint: %v", err)
	}
}

func TestPreflightServicesListError(t *testing.T) {
	// The services list call itself errors (e.g., gcloud not installed).
	c := &captureExec{
		resp: []captureCall{
			{exitCode: 0, stdout: []byte("u@g.com\n")},
			{exitCode: 1, stderr: []byte("permission denied")},
		},
	}
	p := newProvider(t, c)
	err := p.Preflight(context.Background())
	if err == nil {
		t.Fatal("expected error when services list fails")
	}
	if !strings.Contains(err.Error(), "compute.googleapis.com") {
		// Even on a hard error, we should suggest enabling.
		t.Errorf("err should mention API: %v", err)
	}
}
