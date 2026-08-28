package main

import (
	"strings"
	"testing"
)

// The two production gates (default-secret boot refusal, cleartext /ws
// registration) both used to compare os.Getenv("YFI_ENV") == "prod" raw, so
// "Prod"/"PROD"/"production"/" prod " all silently meant dev and the gates
// failed OPEN. These pin the normalization.
func TestEnvModePredicates(t *testing.T) {
	cases := []struct {
		env    string
		isProd bool
		isDev  bool
	}{
		{"prod", true, false},
		{"PROD", true, false},
		{"Prod", true, false},
		{"production", true, false},
		{"Production", true, false},
		{" prod ", true, false},
		{"prod\t", true, false},
		{"dev", false, true},
		{"DEV", false, true},
		{" dev ", false, true},
		{"", false, true},    // unset MUST stay dev (compose default, dev-up.sh)
		{"   ", false, true}, // whitespace-only is still "unset"
		{"staging", false, false},
		{"prodX", false, false},
	}
	for _, c := range cases {
		if got := isProdEnv(c.env); got != c.isProd {
			t.Errorf("isProdEnv(%q) = %v, want %v", c.env, got, c.isProd)
		}
		if got := isDevEnv(c.env); got != c.isDev {
			t.Errorf("isDevEnv(%q) = %v, want %v", c.env, got, c.isDev)
		}
		if isProdEnv(c.env) && isDevEnv(c.env) {
			t.Errorf("%q classified as both prod and dev", c.env)
		}
	}
}

// isProdEnv/isDevEnv are pure and pinned above, but modeName/isProd/isDev are
// the os.Getenv-wrapped versions that actually run at boot and produce the
// first boot-log line. A case-sensitive or untrimmed compare there ("Prod",
// " prod ") silently disarmed both the default-secret guard and the /ws gate
// while the pure predicates stayed green — so this pins the wrappers too.
func TestEnvModeWrappers(t *testing.T) {
	cases := []struct {
		env      string
		wantProd bool
		wantDev  bool
		wantMode string
	}{
		{"", false, true, "dev"},
		{"dev", false, true, "dev"},
		{"prod", true, false, "prod"},
		{"production", true, false, "prod"},
		{"Prod", true, false, "prod"},
		{"PROD", true, false, "prod"},
		{"  prod  ", true, false, "prod"},
		{"staging", false, false, "non-dev"},
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("YFI_ENV", c.env)
			if got := isProd(); got != c.wantProd {
				t.Errorf("isProd() with YFI_ENV=%q = %v, want %v", c.env, got, c.wantProd)
			}
			if got := isDev(); got != c.wantDev {
				t.Errorf("isDev() with YFI_ENV=%q = %v, want %v", c.env, got, c.wantDev)
			}
			if got := modeName(); got != c.wantMode {
				t.Errorf("modeName() with YFI_ENV=%q = %q, want %q", c.env, got, c.wantMode)
			}
		})
	}
}

// The /ws decision matrix: YFI_DEV_WS x YFI_INSECURE_TRANSPORT x prod.
// Registration requires BOTH opt-ins and is independent of YFI_ENV; a declined
// route must always say why.
func TestDecideWS(t *testing.T) {
	cases := []struct {
		name       string
		devWS      string
		ack        string
		prod       bool
		register   bool
		wantMsg    bool
		msgHasWarn bool
	}{
		{name: "no vars", register: false, wantMsg: false},
		{name: "ack only", ack: "1", register: false, wantMsg: false},
		{name: "ack only in prod", ack: "1", prod: true, register: false, wantMsg: false},
		{name: "devws only", devWS: "1", register: false, wantMsg: true},
		{name: "devws only in prod", devWS: "1", prod: true, register: false, wantMsg: true},
		{name: "both in dev", devWS: "1", ack: "1", register: true, wantMsg: true},
		{name: "both in prod", devWS: "1", ack: "1", prod: true, register: true, wantMsg: true, msgHasWarn: true},
		{name: "devws not 1", devWS: "true", ack: "1", register: false, wantMsg: false},
		{name: "ack not 1", devWS: "1", ack: "yes", register: false, wantMsg: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideWS(c.devWS, c.ack, c.prod)
			if got.register != c.register {
				t.Errorf("decideWS(%q,%q,%v).register = %v, want %v", c.devWS, c.ack, c.prod, got.register, c.register)
			}
			if (got.msg != "") != c.wantMsg {
				t.Errorf("decideWS(%q,%q,%v).msg = %q, want message: %v", c.devWS, c.ack, c.prod, got.msg, c.wantMsg)
			}
			if c.msgHasWarn && !strings.Contains(got.msg, "WARNING") {
				t.Errorf("prod registration must log a WARNING, got %q", got.msg)
			}
			// A declined route with YFI_DEV_WS=1 must name the missing var, or it
			// is indistinguishable from a broken build.
			if c.devWS == "1" && !c.register && !strings.Contains(got.msg, "YFI_INSECURE_TRANSPORT") {
				t.Errorf("declined route must name YFI_INSECURE_TRANSPORT, got %q", got.msg)
			}
		})
	}
}
