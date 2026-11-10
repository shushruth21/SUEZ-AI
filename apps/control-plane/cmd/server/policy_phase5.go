package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

type generatePolicyRequest struct {
	Name                string `json:"name"`
	Scope               string `json:"scope"`
	TargetName          string `json:"targetName"`
	NaturalLanguageRule string `json:"naturalLanguageRule"`
	RegoPackage         string `json:"regoPackage"`
}

type policyValidationInput struct {
	PolicyID     string
	Name         string
	Scope        string
	TargetName   string
	RegoPackage  string
	RegoSource   string
	TenantID     string
	TenantSlug   string
	TargetStatus string
}

func (a *controlPlaneApp) handlePolicyGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	identity := mustIdentity(r.Context())

	var req generatePolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Scope) == "" || strings.TrimSpace(req.NaturalLanguageRule) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, scope, and naturalLanguageRule are required"})
		return
	}
	if strings.TrimSpace(req.RegoPackage) == "" {
		req.RegoPackage = "suez.authz"
	}

	policy, err := a.generatePolicyDraft(r.Context(), identity, req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"identityContext": identity,
		"policy":          policy,
	})
}

func (a *controlPlaneApp) handlePolicyActions(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())

	trimmed := strings.TrimPrefix(path.Clean(r.URL.Path), "/api/v1/policies/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	switch {
	case r.Method == http.MethodGet && parts[1] == "versions":
		versions, err := a.listPolicyVersions(r.Context(), identity.TenantID, parts[0])
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"versions":        versions,
		})
	case r.Method == http.MethodGet && parts[1] == "tests":
		testCases, runs, err := a.listPolicyTests(r.Context(), identity.TenantID, parts[0])
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"testCases":       testCases,
			"testRuns":        runs,
		})
	// This must precede the generic POST /tests handler: the run path has
	// three parts and is a distinct action, not a malformed test-case create.
	case r.Method == http.MethodPost && isPolicyTestRunPath(parts):
		run, err := a.runPolicyTests(r.Context(), identity, parts[0])
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"testRun":         run,
		})
	case r.Method == http.MethodPost && parts[1] == "tests":
		if len(parts) != 2 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		var req createPolicyTestCaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		testCase, err := a.createPolicyTestCase(r.Context(), identity, parts[0], req)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"identityContext": identity,
			"testCase":        testCase,
		})
	case r.Method == http.MethodPost && parts[1] == "validate":
		policy, err := a.validateStoredPolicy(r.Context(), identity, parts[0])
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"policy":          policy,
		})
	case r.Method == http.MethodPost && parts[1] == "publish":
		policy, err := a.publishPolicy(r.Context(), identity, parts[0])
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"policy":          policy,
		})
	case r.Method == http.MethodPost && parts[1] == "rollback":
		policy, err := a.rollbackPolicy(r.Context(), identity, parts[0])
		if err != nil {
			writeAppError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"identityContext": identity,
			"policy":          policy,
		})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func isPolicyTestRunPath(parts []string) bool {
	return len(parts) == 3 && parts[0] != "" && parts[1] == "tests" && parts[2] == "run"
}

func (a *controlPlaneApp) generatePolicyDraft(ctx context.Context, identity identityContext, req generatePolicyRequest) (policyRecord, error) {
	tenantSlug, err := a.lookupTenantSlug(ctx, identity.TenantID)
	if err != nil {
		return policyRecord{}, err
	}

	version, parentPolicyID, err := a.nextPolicyVersion(ctx, identity.TenantID, strings.TrimSpace(req.Scope), strings.TrimSpace(req.TargetName))
	if err != nil {
		return policyRecord{}, err
	}

	policyID, err := a.newDraftPolicyID(ctx)
	if err != nil {
		return policyRecord{}, err
	}

	prompt := buildPolicyPrompt(policyID, identity.TenantID, tenantSlug, req)
	regoSource, generatorModel, err := a.generatePolicyModule(ctx, prompt, identity.TenantID, tenantSlug, policyID, req)
	if err != nil {
		return policyRecord{}, err
	}

	validationErrors, compiledAt, err := a.validatePolicyModule(ctx, policyValidationInput{
		PolicyID:    policyID,
		Name:        strings.TrimSpace(req.Name),
		Scope:       strings.TrimSpace(req.Scope),
		TargetName:  strings.TrimSpace(req.TargetName),
		RegoPackage: strings.TrimSpace(req.RegoPackage),
		RegoSource:  regoSource,
		TenantID:    identity.TenantID,
		TenantSlug:  tenantSlug,
	})
	if err != nil {
		return policyRecord{}, err
	}

	row := a.db.QueryRowContext(ctx, `
		insert into policies (
			id, tenant_id, parent_policy_id, name, scope, target_name, natural_language_rule,
			generation_prompt, generator_model, rego_package, rego_source, status, version,
			enabled, validation_errors, last_compiled_at, updated_at
		)
		values (
			$1, $2, nullif($3, '')::uuid, $4, $5, nullif($6, ''), $7,
			$8, $9, $10, $11, 'draft', $12,
			true, $13::jsonb, $14, now()
		)
		returning id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''), coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''), rego_package, rego_source, status, version, enabled, validation_errors, published_at, coalesce(published_by, ''), last_compiled_at, created_at, updated_at
	`, policyID, identity.TenantID, parentPolicyID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Scope), strings.TrimSpace(req.TargetName), strings.TrimSpace(req.NaturalLanguageRule), prompt, generatorModel, strings.TrimSpace(req.RegoPackage), regoSource, version, string(mustJSON(validationErrors)), compiledAt)

	policy, err := scanPolicy(row)
	if err != nil {
		return policyRecord{}, err
	}
	_ = a.insertAudit(ctx, identity, "policy.generate", "policy", policy.Name, "allow", map[string]any{
		"policyId":         policy.ID,
		"scope":            policy.Scope,
		"targetName":       policy.TargetName,
		"generatorModel":   policy.GeneratorModel,
		"validationErrors": policy.ValidationErrors,
		"version":          policy.Version,
	})
	return policy, nil
}

func (a *controlPlaneApp) validateStoredPolicy(ctx context.Context, identity identityContext, policyID string) (policyRecord, error) {
	policy, err := a.getPolicy(ctx, identity.TenantID, policyID)
	if err != nil {
		return policyRecord{}, err
	}
	tenantSlug, err := a.lookupTenantSlug(ctx, identity.TenantID)
	if err != nil {
		return policyRecord{}, err
	}
	validationErrors, compiledAt, err := a.validatePolicyModule(ctx, policyValidationInput{
		PolicyID:    policy.ID,
		Name:        policy.Name,
		Scope:       policy.Scope,
		TargetName:  policy.TargetName,
		RegoPackage: policy.RegoPackage,
		RegoSource:  policy.RegoSource,
		TenantID:    policy.TenantID,
		TenantSlug:  tenantSlug,
	})
	if err != nil {
		return policyRecord{}, err
	}

	row := a.db.QueryRowContext(ctx, `
		update policies
		set validation_errors = $1::jsonb, last_compiled_at = $2, updated_at = now()
		where id = $3 and tenant_id = $4
		returning id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''), coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''), rego_package, rego_source, status, version, enabled, validation_errors, published_at, coalesce(published_by, ''), last_compiled_at, created_at, updated_at
	`, string(mustJSON(validationErrors)), compiledAt, policy.ID, policy.TenantID)
	updated, err := scanPolicy(row)
	if err != nil {
		return policyRecord{}, err
	}
	decision := "allow"
	if len(updated.ValidationErrors) > 0 {
		decision = "deny"
	}
	_ = a.insertAudit(ctx, identity, "policy.validate", "policy", updated.Name, decision, map[string]any{
		"policyId":         updated.ID,
		"validationErrors": updated.ValidationErrors,
		"compiledAt":       updated.LastCompiledAt,
	})
	return updated, nil
}

// publishPolicy holds the policy lock (see lockPolicies) across the database
// transition and the OPA sync. OPA does not provide a multi-module transaction, so serialising
// writers is essential to keep the desired policy set deterministic.
func (a *controlPlaneApp) publishPolicy(ctx context.Context, identity identityContext, policyID string) (policyRecord, error) {
	unlock, err := a.lockPolicies(ctx)
	if err != nil {
		return policyRecord{}, err
	}
	defer unlock()
	return a.publishPolicyLocked(ctx, identity, policyID)
}

func (a *controlPlaneApp) publishPolicyLocked(ctx context.Context, identity identityContext, policyID string) (policyRecord, error) {
	policy, err := a.validateStoredPolicy(ctx, identity, policyID)
	if err != nil {
		return policyRecord{}, err
	}
	if len(policy.ValidationErrors) > 0 {
		return policyRecord{}, errors.New("policy validation failed; review the draft before publishing")
	}
	// Only enabled policies are loaded into OPA. Publishing a disabled one
	// would report success, supersede the live policy, and enforce nothing.
	if !policy.Enabled {
		return policyRecord{}, errors.New("policy is disabled; enable it before publishing")
	}
	testRun, err := a.runPolicyTests(ctx, identity, policy.ID)
	if err != nil {
		return policyRecord{}, err
	}
	if testRun.TotalCount == 0 {
		return policyRecord{}, errors.New("policy must include at least one test case before publish")
	}
	if testRun.FailedCount > 0 || testRun.Status != "passed" {
		return policyRecord{}, errors.New("policy tests must pass before publish")
	}
	if err := a.putOPAPolicy(ctx, policy.TenantID, policy.ID, policy.RegoSource); err != nil {
		return policyRecord{}, err
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return policyRecord{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		update policies
		set status = 'superseded', updated_at = now()
		where tenant_id = $1 and scope = $2 and coalesce(target_name, '') = coalesce($3, '') and id <> $4 and status = 'published'
	`, policy.TenantID, policy.Scope, policy.TargetName, policy.ID); err != nil {
		_ = a.syncPublishedPoliciesLocked(ctx)
		return policyRecord{}, err
	}

	row := tx.QueryRowContext(ctx, `
		update policies
		set status = 'published', published_at = now(), published_by = $1, validation_errors = '[]'::jsonb, last_compiled_at = now(), updated_at = now()
		where id = $2 and tenant_id = $3
		returning id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''), coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''), rego_package, rego_source, status, version, enabled, validation_errors, published_at, coalesce(published_by, ''), last_compiled_at, created_at, updated_at
	`, identity.Actor, policy.ID, policy.TenantID)
	updated, err := scanPolicy(row)
	if err != nil {
		_ = a.syncPublishedPoliciesLocked(ctx)
		return policyRecord{}, err
	}

	if err := tx.Commit(); err != nil {
		_ = a.syncPublishedPoliciesLocked(ctx)
		return policyRecord{}, err
	}
	if err := a.syncPublishedPoliciesLocked(ctx); err != nil {
		return policyRecord{}, err
	}

	_ = a.insertAudit(ctx, identity, "policy.publish", "policy", updated.Name, "allow", map[string]any{
		"policyId":    updated.ID,
		"scope":       updated.Scope,
		"targetName":  updated.TargetName,
		"version":     updated.Version,
		"testRunId":   testRun.ID,
		"testStatus":  testRun.Status,
		"failedCount": testRun.FailedCount,
		"publishedAt": updated.PublishedAt,
	})
	return updated, nil
}

func (a *controlPlaneApp) validatePolicyModule(ctx context.Context, input policyValidationInput) ([]string, time.Time, error) {
	source := normalizePolicySource(input.RegoSource)
	input.RegoSource = source

	errorsFound := make([]string, 0, 6)
	if !validPolicyScope(input.Scope) {
		errorsFound = append(errorsFound, "scope must be one of runtime, model, or tool")
	}
	if strings.TrimSpace(input.RegoPackage) != "suez.authz" {
		errorsFound = append(errorsFound, "regoPackage must be suez.authz")
	}
	if !strings.Contains(source, "package suez.authz") {
		errorsFound = append(errorsFound, "regoSource must declare package suez.authz")
	}
	if !strings.Contains(source, "published_policy_results") {
		errorsFound = append(errorsFound, "regoSource must define published_policy_results")
	}
	if input.PolicyID != "" && !strings.Contains(source, input.PolicyID) {
		errorsFound = append(errorsFound, "regoSource must reference the policy id in published_policy_results")
	}
	if input.TenantID != "" && !strings.Contains(source, input.TenantID) {
		errorsFound = append(errorsFound, "regoSource should scope decisions to the tenant id")
	}
	if scopeAction, ok := expectedScopeAction(input.Scope); ok && !strings.Contains(source, scopeAction) {
		errorsFound = append(errorsFound, fmt.Sprintf("regoSource should constrain input.action to %s for %s scope", scopeAction, input.Scope))
	}
	if trimmedTarget := strings.TrimSpace(input.TargetName); trimmedTarget != "" && trimmedTarget != "*" && !strings.Contains(source, trimmedTarget) {
		errorsFound = append(errorsFound, "regoSource should reference the targetName when publishing a specific target policy")
	}

	if opaErr := a.validateOPAModule(ctx, input.PolicyID, source); opaErr != "" {
		errorsFound = append(errorsFound, opaErr)
	}
	return errorsFound, time.Now().UTC(), nil
}

func (a *controlPlaneApp) generatePolicyModule(ctx context.Context, prompt, tenantID, tenantSlug, policyID string, req generatePolicyRequest) (string, string, error) {
	configuredModel := strings.TrimSpace(env("POLICY_LLM_MODEL", ""))
	modelName, err := a.choosePolicyModel(ctx)
	if err != nil || strings.TrimSpace(modelName) == "" {
		if configuredModel != "" {
			return "", "", fmt.Errorf("configured policy-llm model %q is not ready: %w", configuredModel, err)
		}
		return buildTemplatePolicy(policyID, tenantID, tenantSlug, req), "template-fallback", nil
	}

	body := map[string]any{
		"model":  modelName,
		"prompt": prompt,
		"stream": false,
		"options": map[string]any{
			"temperature": 0,
		},
	}
	payload, _ := json.Marshal(body)
	endpoint := strings.TrimRight(env("POLICY_LLM_BASE_URL", "http://policy-llm:11434"), "/") + "/api/generate"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := a.httpClient.Do(request)
	if err != nil {
		if configuredModel != "" {
			return "", "", fmt.Errorf("policy-llm generation failed for %q: %w", modelName, err)
		}
		return buildTemplatePolicy(policyID, tenantID, tenantSlug, req), "template-fallback", nil
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		if configuredModel != "" {
			payload, _ := io.ReadAll(response.Body)
			return "", "", fmt.Errorf("policy-llm generation failed for %q: %s", modelName, strings.TrimSpace(string(payload)))
		}
		return buildTemplatePolicy(policyID, tenantID, tenantSlug, req), "template-fallback", nil
	}

	var decoded struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(decoded.Response) == "" {
		if configuredModel != "" {
			return "", "", fmt.Errorf("policy-llm model %q returned an empty draft", modelName)
		}
		return buildTemplatePolicy(policyID, tenantID, tenantSlug, req), "template-fallback", nil
	}
	return normalizePolicySource(decoded.Response), modelName, nil
}

func (a *controlPlaneApp) choosePolicyModel(ctx context.Context) (string, error) {
	configured := strings.TrimSpace(env("POLICY_LLM_MODEL", ""))
	endpoint := strings.TrimRight(env("POLICY_LLM_BASE_URL", "http://policy-llm:11434"), "/") + "/api/tags"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return configured, err
	}
	response, err := a.httpClient.Do(request)
	if err != nil {
		return configured, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return configured, fmt.Errorf("policy-llm tags returned %s", response.Status)
	}

	var decoded struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return configured, err
	}
	if configured != "" {
		for _, model := range decoded.Models {
			if model.Name == configured {
				return configured, nil
			}
		}
		return "", fmt.Errorf("configured policy-llm model %q has not been pulled", configured)
	}
	if len(decoded.Models) > 0 {
		return decoded.Models[0].Name, nil
	}
	return configured, errors.New("no policy-llm models are available")
}

func (a *controlPlaneApp) nextPolicyVersion(ctx context.Context, tenantID, scope, targetName string) (int, string, error) {
	var (
		version        int
		parentPolicyID string
	)
	row := a.db.QueryRowContext(ctx, `
		select coalesce(max(version), 0), coalesce((
			select id::text
			from policies
			where tenant_id = $1 and scope = $2 and coalesce(target_name, '') = coalesce($3, '') and status = 'published'
			order by version desc, created_at desc
			limit 1
		), '')
		from policies
		where tenant_id = $1 and scope = $2 and coalesce(target_name, '') = coalesce($3, '')
	`, tenantID, scope, targetName)
	if err := row.Scan(&version, &parentPolicyID); err != nil {
		return 0, "", err
	}
	return version + 1, parentPolicyID, nil
}

func (a *controlPlaneApp) newDraftPolicyID(ctx context.Context) (string, error) {
	var id string
	if err := a.db.QueryRowContext(ctx, `select gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

func (a *controlPlaneApp) getPolicy(ctx context.Context, tenantID, policyID string) (policyRecord, error) {
	row := a.db.QueryRowContext(ctx, `
		select id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''), coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''), rego_package, rego_source, status, version, enabled, validation_errors, published_at, coalesce(published_by, ''), last_compiled_at, created_at, updated_at
		from policies
		where tenant_id = $1 and id = $2
		limit 1
	`, tenantID, policyID)
	policy, err := scanPolicy(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return policyRecord{}, errors.New("policy not found")
		}
		return policyRecord{}, err
	}
	return policy, nil
}

func (a *controlPlaneApp) lookupTenantSlug(ctx context.Context, tenantID string) (string, error) {
	var slug string
	if err := a.db.QueryRowContext(ctx, `select slug from tenants where id = $1 limit 1`, tenantID).Scan(&slug); err != nil {
		return "", err
	}
	return slug, nil
}

func (a *controlPlaneApp) syncPublishedPolicies(ctx context.Context, _ string) error {
	unlock, err := a.lockPolicies(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return a.syncPublishedPoliciesLocked(ctx)
}

// syncPublishedPoliciesLocked writes every desired module before it removes
// any stale one. The former delete-then-put behaviour created a period where
// live traffic had no tenant policy at all.
func (a *controlPlaneApp) syncPublishedPoliciesLocked(ctx context.Context) error {
	published, err := a.listPublishedPolicies(ctx)
	if err != nil {
		return err
	}
	desired := make(map[string]struct{}, len(published))
	for _, policy := range published {
		modulePath := opaPolicyPath(policy.ID)
		if err := a.putOPAPolicy(ctx, policy.TenantID, policy.ID, policy.RegoSource); err != nil {
			return err
		}
		desired[modulePath] = struct{}{}
	}
	existingIDs, err := a.listManagedOPAPolicies(ctx)
	if err != nil {
		return err
	}
	for _, existing := range existingIDs {
		if _, keep := desired[existing]; keep {
			continue
		}
		if err := a.deleteOPAPolicy(ctx, existing); err != nil {
			return err
		}
	}
	return nil
}

func (a *controlPlaneApp) listPublishedPolicies(ctx context.Context) ([]policyRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''), coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''), rego_package, rego_source, status, version, enabled, validation_errors, published_at, coalesce(published_by, ''), last_compiled_at, created_at, updated_at
		from policies
		where status = 'published' and enabled = true
		order by updated_at asc
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []policyRecord
	for rows.Next() {
		policy, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (a *controlPlaneApp) listManagedOPAPolicies(ctx context.Context) ([]string, error) {
	endpoint := strings.TrimRight(env("OPA_BASE_URL", "http://opa:8181"), "/") + "/v1/policies"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := a.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		payload, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("opa policy list failed: %s", strings.TrimSpace(string(payload)))
	}

	var decoded struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	var ids []string
	for _, item := range decoded.Result {
		if strings.HasPrefix(item.ID, "suez/generated/") {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}

func (a *controlPlaneApp) validateOPAModule(ctx context.Context, policyID, source string) string {
	key := newSandboxKey()
	moduleSource, err := sandboxModuleSource(key, source)
	if err != nil {
		return err.Error()
	}
	modulePath := opaSandboxPath(key)
	if err := a.putOPAModule(ctx, modulePath, sandboxPackage(key), moduleSource); err != nil {
		return err.Error()
	}
	defer func() { _ = a.deleteOPAPolicy(context.WithoutCancel(ctx), modulePath) }()
	return ""
}

// putOPAPolicy accepts only a draft source in the constrained source package.
// It rewrites that one package declaration into an isolated tenant/policy
// namespace before handing it to OPA.
func (a *controlPlaneApp) putOPAPolicy(ctx context.Context, tenantID, policyID, source string) error {
	moduleSource, err := opaModuleSource(tenantID, policyID, source)
	if err != nil {
		return err
	}
	return a.putOPAModule(ctx, opaPolicyPath(policyID), generatedPackage(tenantID, policyID), moduleSource)
}

// putOPAModule uploads a module and then checks the package OPA actually
// parsed. If it is not the expected one the module is removed at once; this
// backs up the lexical check in rewriteAuthzPackage.
func (a *controlPlaneApp) putOPAModule(ctx context.Context, modulePath, expectedPackage, source string) error {
	if err := a.uploadOPAModule(ctx, modulePath, source); err != nil {
		return err
	}
	actual, err := a.opaModulePackage(ctx, modulePath)
	if err != nil || actual != expectedPackage {
		_ = a.deleteOPAPolicy(context.WithoutCancel(ctx), modulePath)
		if err != nil {
			return fmt.Errorf("could not confirm the policy package: %w", err)
		}
		return fmt.Errorf("policy declares package %s; only package suez.authz is accepted", actual)
	}
	return nil
}

// opaModulePackage returns the package of a loaded module as a dotted path
// without the leading "data".
func (a *controlPlaneApp) opaModulePackage(ctx context.Context, modulePath string) (string, error) {
	endpoint := strings.TrimRight(env("OPA_BASE_URL", "http://opa:8181"), "/") + "/v1/policies/" + modulePath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	response, err := a.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return "", fmt.Errorf("opa returned %s", response.Status)
	}
	var decoded struct {
		Result struct {
			AST struct {
				Package struct {
					Path []struct {
						Value any `json:"value"`
					} `json:"path"`
				} `json:"package"`
			} `json:"ast"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&decoded); err != nil {
		return "", err
	}
	return packagePathString(decoded.Result.AST.Package.Path)
}

func packagePathString(path []struct {
	Value any `json:"value"`
}) (string, error) {
	if len(path) < 2 {
		return "", errors.New("module has no package")
	}
	parts := make([]string, 0, len(path)-1)
	for _, term := range path[1:] {
		value, ok := term.Value.(string)
		if !ok {
			return "", errors.New("unexpected package path term")
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "."), nil
}

func (a *controlPlaneApp) uploadOPAModule(ctx context.Context, modulePath, source string) error {
	endpoint := strings.TrimRight(env("OPA_BASE_URL", "http://opa:8181"), "/") + "/v1/policies/" + modulePath
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, strings.NewReader(source))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := a.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		payload, _ := io.ReadAll(response.Body)
		return fmt.Errorf("opa rejected policy publish: %s", strings.TrimSpace(string(payload)))
	}
	return nil
}

func (a *controlPlaneApp) deleteOPAPolicy(ctx context.Context, policyPath string) error {
	endpoint := strings.TrimRight(env("OPA_BASE_URL", "http://opa:8181"), "/") + "/v1/policies/" + policyPath
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := a.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 && response.StatusCode != http.StatusNotFound {
		payload, _ := io.ReadAll(response.Body)
		return fmt.Errorf("opa policy delete failed: %s", strings.TrimSpace(string(payload)))
	}
	return nil
}

func buildPolicyPrompt(policyID, tenantID, tenantSlug string, req generatePolicyRequest) string {
	scopeAction, _ := expectedScopeAction(req.Scope)
	targetConstraint := `input.resource.name == ` + regoString(strings.TrimSpace(req.TargetName))
	if strings.TrimSpace(req.TargetName) == "" || strings.TrimSpace(req.TargetName) == "*" {
		targetConstraint = "true"
	}

	return fmt.Sprintf(`Return only a Rego module. Do not use markdown fences.

Generate a policy module for:
- package: suez.authz
- policyId: %s
- tenantId: %s
- tenantSlug: %s
- scope: %s
- targetName: %s
- naturalLanguageRule: %s

Contract requirements:
1. The module MUST declare package suez.authz.
2. The module MUST define published_policy_results contains {"policyId": "%s", "allow": <bool>, "reason": "<short_reason>"} if { ... }.
3. The rule MUST scope itself to input.identity.tenantId == %s.
4. For this scope, constrain input.action == "%s".
5. For this target, include %s.
6. Use only input.identity, input.action, input.resource, and input.request from the current request.
7. Keep the rule conservative and deterministic.
8. Prefer explicit deny rules by emitting allow: false when the natural-language rule should block the request.
`, policyID, tenantID, tenantSlug, req.Scope, req.TargetName, req.NaturalLanguageRule, policyID, regoString(tenantID), scopeAction, targetConstraint)
}

func buildTemplatePolicy(policyID, tenantID, tenantSlug string, req generatePolicyRequest) string {
	lines := []string{
		"package suez.authz",
		"",
		fmt.Sprintf("# Generated fallback draft for tenant %s (%s).", regoComment(tenantSlug), regoComment(tenantID)),
		"published_policy_results contains {",
		fmt.Sprintf(`    "policyId": %s,`, regoString(policyID)),
		`    "allow": true,`,
		`    "reason": "draft_template_allow"`,
		"} if {",
		fmt.Sprintf(`    input.identity.tenantId == %s`, regoString(tenantID)),
	}
	if scopeAction, ok := expectedScopeAction(req.Scope); ok {
		lines = append(lines, fmt.Sprintf(`    input.action == %s`, regoString(scopeAction)))
	}
	switch strings.TrimSpace(req.Scope) {
	case "model":
		lines = append(lines, `    input.resource.type == "model"`)
	case "tool":
		lines = append(lines, `    input.resource.type == "tool"`)
	}
	if trimmedTarget := strings.TrimSpace(req.TargetName); trimmedTarget != "" && trimmedTarget != "*" {
		lines = append(lines, fmt.Sprintf(`    input.resource.name == %s`, regoString(trimmedTarget)))
	}
	lines = append(lines, "}")
	return strings.Join(lines, "\n")
}

func expectedScopeAction(scope string) (string, bool) {
	switch strings.TrimSpace(scope) {
	case "model":
		return "chat.completions", true
	case "tool":
		return "tools.execute", true
	case "runtime":
		return "", false
	default:
		return "", false
	}
}

func validPolicyScope(scope string) bool {
	switch strings.TrimSpace(scope) {
	case "runtime", "model", "tool":
		return true
	default:
		return false
	}
}

func normalizePolicySource(source string) string {
	trimmed := strings.TrimSpace(source)
	trimmed = strings.TrimPrefix(trimmed, "```rego")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}

func opaPolicyPath(policyID string) string {
	return "suez/generated/" + policyID
}

func opaSandboxPath(key string) string {
	return "suez/sandbox/" + key
}

func policyPackageKey(policyID string) string {
	return "p_" + strings.ReplaceAll(policyID, "-", "_")
}

func tenantPackageKey(tenantID string) string {
	return "t_" + strings.ReplaceAll(tenantID, "-", "_")
}

// rewriteAuthzPackage replaces the module's package declaration, which Rego
// requires to be the first statement. Only blank and comment lines may come
// before it and it must read exactly "package suez.authz". Matching any line
// that looks like a package (for example one inside a raw string) would let a
// module keep a different real package such as suez.authz with a trailing
// comment, or another tenant's namespace.
func rewriteAuthzPackage(source, pkg string) (string, error) {
	lines := strings.Split(normalizePolicySource(source), "\n")
	declaration := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed != "package suez.authz" {
			return "", errors.New("regoSource must start with the line: package suez.authz")
		}
		declaration = i
		break
	}
	if declaration < 0 {
		return "", errors.New("regoSource must start with the line: package suez.authz")
	}
	// A package ref cannot be continued on the next line.
	for _, line := range lines[declaration+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, ".") || strings.HasPrefix(trimmed, "[") {
			return "", errors.New("regoSource must start with the line: package suez.authz")
		}
		break
	}
	// Rego allows one package per module; refuse anything that looks like a
	// second declaration rather than relying on OPA to reject it later.
	for _, line := range lines[declaration+1:] {
		if strings.HasPrefix(strings.TrimSpace(line), "package ") {
			return "", errors.New("regoSource must declare package suez.authz exactly once")
		}
	}
	lines[declaration] = "package " + pkg
	return strings.Join(lines, "\n"), nil
}

func generatedPackage(tenantID, policyID string) string {
	return "suez.generated." + tenantPackageKey(tenantID) + "." + policyPackageKey(policyID)
}

func sandboxPackage(key string) string {
	return "suez.sandbox." + key
}

// opaModuleSource places a tenant's policy in its own namespace. No
// user-controlled source can select the shared base package or another
// tenant's namespace.
func opaModuleSource(tenantID, policyID, source string) (string, error) {
	return rewriteAuthzPackage(source, generatedPackage(tenantID, policyID))
}

func sandboxModuleSource(key, source string) (string, error) {
	return rewriteAuthzPackage(source, sandboxPackage(key))
}

func newSandboxKey() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("r_%d", time.Now().UnixNano())
	}
	return "r_" + hex.EncodeToString(buf)
}

func mustJSON(value any) []byte {
	payload, _ := json.Marshal(value)
	return payload
}

// regoString renders value as a Rego string literal. Rego string literals use
// JSON escaping, so quotes, backslashes and newlines in tenant or resource
// names cannot break out of the literal.
func regoString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// regoComment keeps a value on a single comment line.
func regoComment(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}
