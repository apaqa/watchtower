package config

import "testing"

func TestValidateCatchesBadPort(t *testing.T) {
	cfg := Default()
	cfg.Server.IngestPort = 70000

	report := Validate(cfg)
	if !report.HasErrors() {
		t.Fatal("expected validation errors for invalid port")
	}
}

func TestValidateCatchesMissingEndpointFields(t *testing.T) {
	cfg := Default()
	cfg.Endpoints = []EndpointConfig{{
		URL: "http://localhost:9090/health",
	}}

	report := Validate(cfg)
	if !report.HasErrors() {
		t.Fatal("expected validation errors for missing endpoint name")
	}
}

func TestValidateCatchesInvalidURL(t *testing.T) {
	cfg := Default()
	cfg.Endpoints = []EndpointConfig{{
		Name:            "bad",
		URL:             "://bad-url",
		Method:          "GET",
		ExpectedStatus:  200,
		IntervalSeconds: 10,
		TimeoutMs:       1000,
	}}

	report := Validate(cfg)
	if !report.HasErrors() {
		t.Fatal("expected validation errors for invalid URL")
	}
}

func TestValidatePassesValidConfig(t *testing.T) {
	cfg := Default()
	cfg.Endpoints = []EndpointConfig{{
		Name:            "health",
		URL:             "http://localhost:9090/health",
		Method:          "GET",
		ExpectedStatus:  200,
		IntervalSeconds: 30,
		TimeoutMs:       5000,
	}}

	report := Validate(cfg)
	if report.HasErrors() {
		t.Fatalf("expected valid config, got errors: %+v", report.Errors)
	}
}
