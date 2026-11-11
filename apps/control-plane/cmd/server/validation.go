package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

var tenantSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func validateTenantRequest(req createTenantRequest) error {
	if len(strings.TrimSpace(req.Name)) == 0 || len(strings.TrimSpace(req.Name)) > 256 {
		return errors.New("name must be between 1 and 256 characters")
	}
	if !tenantSlugPattern.MatchString(strings.TrimSpace(req.Slug)) {
		return errors.New("slug must match ^[a-z0-9][a-z0-9-]{1,62}$")
	}
	if len(strings.TrimSpace(req.Environment)) > 64 {
		return errors.New("environment must be at most 64 characters")
	}
	return nil
}

func validateModelRequest(req createModelRequest) error {
	if !oneOf(strings.TrimSpace(req.Provider), "stub", "openai-compatible", "groq") {
		return errors.New("provider must be one of stub, openai-compatible, or groq")
	}
	if len(strings.TrimSpace(req.ExternalName)) == 0 || len(strings.TrimSpace(req.ExternalName)) > 256 || len(strings.TrimSpace(req.ModelName)) == 0 || len(strings.TrimSpace(req.ModelName)) > 256 {
		return errors.New("externalName and modelName must be between 1 and 256 characters")
	}
	if len(strings.TrimSpace(req.UpstreamModel)) > 256 {
		return errors.New("upstreamModel must be at most 256 characters")
	}
	if err := validateAbsoluteHTTPURL(req.UpstreamBaseURL, true); err != nil {
		return fmt.Errorf("upstreamBaseUrl: %w", err)
	}
	if req.ContextWindow < 0 || req.ContextWindow > 100_000_000 {
		return errors.New("contextWindow must be between 0 and 100000000")
	}
	if req.MaxRPM < 0 || req.MaxTPM < 0 {
		return errors.New("maxRpm and maxTpm must be greater than or equal to 0")
	}
	return nil
}

func validateToolRequest(req createToolRequest) error {
	if len(strings.TrimSpace(req.Name)) == 0 || len(strings.TrimSpace(req.Name)) > 256 || len(strings.TrimSpace(req.Description)) == 0 || len(strings.TrimSpace(req.Description)) > 4096 {
		return errors.New("name must be 1-256 characters and description must be 1-4096 characters")
	}
	if !oneOf(strings.TrimSpace(req.ExecutionMode), "stub", "http") {
		return errors.New("executionMode must be stub or http")
	}
	if strings.TrimSpace(req.ExecutionMode) == "http" {
		if err := validateAbsoluteHTTPURL(req.TargetURL, false); err != nil {
			return fmt.Errorf("targetUrl: %w", err)
		}
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > 60000 {
		return errors.New("timeoutMs must be between 0 and 60000")
	}
	if !oneOf(strings.TrimSpace(req.RiskTier), "low", "standard", "high") {
		return errors.New("riskTier must be low, standard, or high")
	}
	if err := validateSchema(req.InputSchema, "inputSchema"); err != nil {
		return err
	}
	if err := validateSchema(req.OutputSchema, "outputSchema"); err != nil {
		return err
	}
	if err := validateHeaders(req.HTTPHeaders); err != nil {
		return err
	}
	return nil
}

func validateAccessRequest(req createAccessRequestRequest) error {
	if !oneOf(strings.TrimSpace(req.ResourceType), "model", "tool") {
		return errors.New("resourceType must be model or tool")
	}
	if !validLength(req.Subject, 1, 256) || !validLength(req.ResourceName, 1, 256) || !validLength(req.Justification, 1, 4096) {
		return errors.New("subject and resourceName must be 1-256 characters and justification must be 1-4096 characters")
	}
	return nil
}

func validateAbsoluteHTTPURL(raw string, optional bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" && optional {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("must be an absolute http(s) URL")
	}
	return nil
}

func validateSchema(raw json.RawMessage, field string) error {
	if len(raw) == 0 {
		return fmt.Errorf("%s is required", field)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return fmt.Errorf("%s must be valid JSON Schema", field)
	}
	if _, err := gojsonschema.NewSchema(gojsonschema.NewBytesLoader(raw)); err != nil {
		return fmt.Errorf("%s must be valid JSON Schema: %w", field, err)
	}
	return nil
}

func validateHeaders(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("httpHeaders is required")
	}
	var headers map[string]string
	if err := json.Unmarshal(raw, &headers); err != nil || headers == nil {
		return errors.New("httpHeaders must be an object with string values")
	}
	for key := range headers {
		if strings.TrimSpace(key) == "" {
			return errors.New("httpHeaders cannot contain an empty header name")
		}
	}
	return nil
}

func redactToolHeaders(tool toolRecord) toolRecord {
	var headers map[string]string
	if json.Unmarshal(tool.HTTPHeaders, &headers) != nil {
		tool.HTTPHeaders = json.RawMessage(`{}`)
		return tool
	}
	for key := range headers {
		headers[key] = "***"
	}
	if redacted, err := json.Marshal(headers); err == nil {
		tool.HTTPHeaders = redacted
	}
	return tool
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validLength(value string, minimum, maximum int) bool {
	n := len(strings.TrimSpace(value))
	return n >= minimum && n <= maximum
}
