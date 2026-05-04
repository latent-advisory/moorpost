package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunDoctorAllOK(t *testing.T) {
	checks := []Check{
		func(context.Context) CheckResult {
			return CheckResult{Name: "alpha", Severity: "ok", Detail: "found"}
		},
		func(context.Context) CheckResult {
			return CheckResult{Name: "beta", Severity: "ok", Detail: "found"}
		},
	}
	var out bytes.Buffer
	if err := RunDoctor(context.Background(), &out, checks); err != nil {
		t.Errorf("RunDoctor returned err on all-OK: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "[OK]") {
		t.Errorf("output missing OK marker: %q", s)
	}
	if !strings.Contains(s, "alpha") || !strings.Contains(s, "beta") {
		t.Errorf("output missing check names: %q", s)
	}
}

func TestRunDoctorFailReturnsError(t *testing.T) {
	checks := []Check{
		func(context.Context) CheckResult {
			return CheckResult{Name: "alpha", Severity: "fail", Detail: "missing", Hint: "install it"}
		},
	}
	var out bytes.Buffer
	err := RunDoctor(context.Background(), &out, checks)
	if err == nil {
		t.Error("RunDoctor did not return error on failing check")
	}
	s := out.String()
	if !strings.Contains(s, "[FAIL]") {
		t.Errorf("output missing FAIL marker: %q", s)
	}
	if !strings.Contains(s, "install it") {
		t.Errorf("output missing hint: %q", s)
	}
}

func TestRunDoctorWarnDoesntFail(t *testing.T) {
	checks := []Check{
		func(context.Context) CheckResult {
			return CheckResult{Name: "alpha", Severity: "warn", Detail: "old"}
		},
	}
	var out bytes.Buffer
	if err := RunDoctor(context.Background(), &out, checks); err != nil {
		t.Errorf("warn produced err: %v", err)
	}
	if !strings.Contains(out.String(), "[WARN]") {
		t.Errorf("output missing WARN marker: %q", out.String())
	}
}

func TestCheckBinaryAvailableMissing(t *testing.T) {
	check := checkBinaryAvailable("definitely-not-real-xyz", "install it")
	res := check(context.Background())
	if res.Severity != "fail" {
		t.Errorf("severity = %q, want fail", res.Severity)
	}
}

func TestCheckBinaryAvailablePresent(t *testing.T) {
	// `sh` is universally available on darwin/linux test runners.
	check := checkBinaryAvailable("sh", "install it")
	res := check(context.Background())
	if res.Severity != "ok" {
		t.Errorf("severity = %q, want ok", res.Severity)
	}
}
