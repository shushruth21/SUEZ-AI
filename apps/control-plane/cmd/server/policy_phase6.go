package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type policyTestCase struct {
	ID              string          `json:"id"`
	PolicyID        string          `json:"policyId"`
	TenantID        string          `json:"tenantId"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Identity        json.RawMessage `json:"identity"`
	Action          string          `json:"action"`
	Resource        json.RawMessage `json:"resource"`
	Request         json.RawMessage `json:"request"`
	ExpectedAllow   bool            `json:"expectedAllow"`
	ExpectedReasons []string        `json:"expectedReasons"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
}

type policyTestRun struct {
	ID           string          `json:"id"`
	PolicyID     string          `json:"policyId"`
	TenantID     string          `json:"tenantId"`
	InitiatedBy  string          `json:"initiatedBy"`
	Status       string          `json:"status"`
	TotalCount   int             `json:"totalCount"`
	PassedCount  int             `json:"passedCount"`
	FailedCount  int             `json:"failedCount"`
	Results      json.RawMessage `json:"results"`
	StartedAt    string          `json:"startedAt"`
	CompletedAt  string          `json:"completedAt,omitempty"`
	CreatedAt    string          `json:"createdAt"`
}

type createPolicyTestCaseRequest struct {
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	Identity        map[string]any `json:"identity"`
	Action          string         `json:"action"`
	Resource        map[string]any `json:"resource"`
	Request         map[string]any `json:"request"`
	ExpectedAllow   bool           `json:"expectedAllow"`
	ExpectedReasons []string       `json:"expectedReasons"`
}

type policyTestResult struct {
	CaseID          string      `json:"caseId"`
	Name            string      `json:"name"`
	Passed          bool        `json:"passed"`
	ExpectedAllow   bool        `json:"expectedAllow"`
	ActualAllow     bool        `json:"actualAllow"`
	ExpectedReasons []string    `json:"expectedReasons"`
	ActualReasons   []string    `json:"actualReasons"`
	Decision        opaDecision `json:"decision"`
}

type opaDecision struct {
	Allow         bool     `json:"allow"`
	Phase         string   `json:"phase,omitempty"`
	Action        string   `json:"action,omitempty"`
	PolicyVersion any      `json:"policyVersion,omitempty"`
	Reasons       []string `json:"reasons,omitempty"`
}

func (a *controlPlaneApp) listPolicyTests(ctx context.Context, tenantID, policyID string) ([]policyTestCase, []policyTestRun, error) {
	if _, err := a.getPolicy(ctx, tenantID, policyID); err != nil {
		return nil, nil, err
	}
	testCases, err := a.listPolicyTestCases(ctx, tenantID, policyID)
	if err != nil {
		return nil, nil, err
	}
	runs, err := a.listPolicyTestRuns(ctx, tenantID, policyID, 20)
	if err != nil {
		return nil, nil, err
	}
	return testCases, runs, nil
}

func (a *controlPlaneApp) createPolicyTestCase(ctx context.Context, identity identityContext, policyID string, req createPolicyTestCaseRequest) (policyTestCase, error) {
	policy, err := a.getPolicy(ctx, identity.TenantID, policyID)
	if err != nil {
		return policyTestCase{}, err
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Action) == "" {
		return policyTestCase{}, errors.New("name and action are required")
	}
	if req.Identity == nil {
		req.Identity = map[string]any{}
	}
	if req.Resource == nil {
		req.Resource = map[string]any{}
	}
	if req.Request == nil {
		req.Request = map[string]any{}
	}
	if _, ok := req.Identity["tenantId"]; !ok {
		req.Identity["tenantId"] = identity.TenantID
	}
	if _, ok := req.Identity["tenantSlug"]; !ok && identity.TenantSlug != "" {
		req.Identity["tenantSlug"] = identity.TenantSlug
	}
	if _, ok := req.Identity["tenantName"]; !ok && identity.TenantName != "" {
		req.Identity["tenantName"] = identity.TenantName
	}
	if _, ok := req.Identity["roles"]; !ok {
		req.Identity["roles"] = []string{"user"}
	}

	row := a.db.QueryRowContext(ctx, `
		insert into policy_test_cases (
			policy_id, tenant_id, name, description, identity_payload, action, resource_payload,
			request_payload, expected_allow, expected_reasons, updated_at
		)
		values (
			$1, $2, $3, $4, $5::jsonb, $6, $7::jsonb,
			$8::jsonb, $9, $10::jsonb, now()
		)
		returning id, policy_id, tenant_id, name, description, identity_payload, action, resource_payload, request_payload, expected_allow, expected_reasons, created_at, updated_at
	`, policy.ID, identity.TenantID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Description), string(mustJSON(req.Identity)), strings.TrimSpace(req.Action), string(mustJSON(req.Resource)), string(mustJSON(req.Request)), req.ExpectedAllow, string(mustJSON(req.ExpectedReasons)))
	testCase, err := scanPolicyTestCase(row)
	if err != nil {
		return policyTestCase{}, err
	}
	_ = a.insertAudit(ctx, identity, "policy.test_case.create", "policy", policy.Name, "allow", map[string]any{
		"policyId":       policy.ID,
		"policyVersion":  policy.Version,
		"testCaseId":     testCase.ID,
		"expectedAllow":  testCase.ExpectedAllow,
		"expectedReason": testCase.ExpectedReasons,
	})
	return testCase, nil
}

func (a *controlPlaneApp) listPolicyVersions(ctx context.Context, tenantID, policyID string) ([]policyRecord, error) {
	policy, err := a.getPolicy(ctx, tenantID, policyID)
	if err != nil {
		return nil, err
	}
	rows, err := a.db.QueryContext(ctx, `
		select id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''),
			coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''),
			rego_package, rego_source, status, version, enabled, validation_errors, published_at,
			coalesce(published_by, ''), last_compiled_at, created_at, updated_at
		from policies
		where tenant_id = $1 and scope = $2 and coalesce(target_name, '') = coalesce($3, '')
		order by version desc, created_at desc
	`, tenantID, policy.Scope, policy.TargetName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []policyRecord
	for rows.Next() {
		version, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (a *controlPlaneApp) runPolicyTests(ctx context.Context, identity identityContext, policyID string) (policyTestRun, error) {
	policy, err := a.getPolicy(ctx, identity.TenantID, policyID)
	if err != nil {
		return policyTestRun{}, err
	}
	testCases, err := a.listPolicyTestCases(ctx, identity.TenantID, policy.ID)
	if err != nil {
		return policyTestRun{}, err
	}

	startedAt := time.Now().UTC()
	results := make([]policyTestResult, 0, len(testCases))
	status := "passed"
	if len(testCases) == 0 {
		status = "empty"
	}
	passedCount := 0
	failedCount := 0

	var restoreErr error
	if len(testCases) > 0 {
		restoreErr = a.runPolicyTestsWithSimulatedPublish(ctx, policy, testCases, &results, &passedCount, &failedCount)
		if restoreErr != nil {
			status = "error"
		} else if failedCount > 0 {
			status = "failed"
		}
	}

	completedAt := time.Now().UTC()
	row := a.db.QueryRowContext(ctx, `
		insert into policy_test_runs (
			policy_id, tenant_id, initiated_by, status, total_count, passed_count, failed_count,
			results, started_at, completed_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
		returning id, policy_id, tenant_id, initiated_by, status, total_count, passed_count, failed_count, results, started_at, completed_at, created_at
	`, policy.ID, policy.TenantID, identity.Actor, status, len(testCases), passedCount, failedCount, string(mustJSON(results)), startedAt, completedAt)
	run, err := scanPolicyTestRun(row)
	if err != nil {
		return policyTestRun{}, err
	}

	decision := "allow"
	if run.Status == "failed" || run.Status == "error" {
		decision = "deny"
	}
	metadata := map[string]any{
		"policyId":     policy.ID,
		"policyName":   policy.Name,
		"runId":        run.ID,
		"status":       run.Status,
		"totalCount":   run.TotalCount,
		"passedCount":  run.PassedCount,
		"failedCount":  run.FailedCount,
		"policyStatus": policy.Status,
	}
	if restoreErr != nil {
		metadata["error"] = restoreErr.Error()
	}
	_ = a.insertAudit(ctx, identity, "policy.tests.run", "policy", policy.Name, decision, metadata)
	if restoreErr != nil {
		return run, restoreErr
	}
	return run, nil
}

func (a *controlPlaneApp) rollbackPolicy(ctx context.Context, identity identityContext, policyID string) (policyRecord, error) {
	source, err := a.getPolicy(ctx, identity.TenantID, policyID)
	if err != nil {
		return policyRecord{}, err
	}
	if source.Status == "draft" && len(source.ValidationErrors) > 0 {
		return policyRecord{}, errors.New("cannot rollback to a draft with validation errors")
	}
	version, _, err := a.nextPolicyVersion(ctx, source.TenantID, source.Scope, source.TargetName)
	if err != nil {
		return policyRecord{}, err
	}
	newPolicyID, err := a.newDraftPolicyID(ctx)
	if err != nil {
		return policyRecord{}, err
	}
	regoSource := strings.ReplaceAll(source.RegoSource, source.ID, newPolicyID)

	row := a.db.QueryRowContext(ctx, `
		insert into policies (
			id, tenant_id, parent_policy_id, rollback_source_policy_id, name, scope, target_name,
			natural_language_rule, generation_prompt, generator_model, rego_package, rego_source,
			status, version, enabled, validation_errors, last_compiled_at, updated_at
		)
		values (
			$1, $2, $3::uuid, $4::uuid, $5, $6, nullif($7, ''), nullif($8, ''), nullif($9, ''),
			$10, $11, $12, 'draft', $13, $14, $15::jsonb, now(), now()
		)
		returning id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''),
			coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''),
			rego_package, rego_source, status, version, enabled, validation_errors, published_at,
			coalesce(published_by, ''), last_compiled_at, created_at, updated_at
	`, newPolicyID, source.TenantID, source.ID, source.ID, source.Name, source.Scope, source.TargetName, source.NaturalLanguageRule, source.GenerationPrompt, fallbackString(strings.TrimSpace(source.GeneratorModel), "rollback-copy"), source.RegoPackage, regoSource, version, source.Enabled, string(mustJSON(source.ValidationErrors)))
	cloned, err := scanPolicy(row)
	if err != nil {
		return policyRecord{}, err
	}

	testCases, err := a.listPolicyTestCases(ctx, source.TenantID, source.ID)
	if err != nil {
		return policyRecord{}, err
	}
	for _, testCase := range testCases {
		if _, err := a.db.ExecContext(ctx, `
			insert into policy_test_cases (
				policy_id, tenant_id, name, description, identity_payload, action, resource_payload,
				request_payload, expected_allow, expected_reasons, updated_at
			)
			values ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, $8::jsonb, $9, $10::jsonb, now())
		`, cloned.ID, cloned.TenantID, testCase.Name, testCase.Description, string(testCase.Identity), testCase.Action, string(testCase.Resource), string(testCase.Request), testCase.ExpectedAllow, string(mustJSON(testCase.ExpectedReasons))); err != nil {
			return policyRecord{}, err
		}
	}
	updated, err := a.publishPolicy(ctx, identity, cloned.ID)
	if err != nil {
		return policyRecord{}, err
	}
	_ = a.insertAudit(ctx, identity, "policy.rollback", "policy", updated.Name, "allow", map[string]any{
		"policyId":          updated.ID,
		"rollbackSourceId":  source.ID,
		"rollbackSourceVer": source.Version,
		"version":           updated.Version,
	})
	return updated, nil
}

func (a *controlPlaneApp) runPolicyTestsWithSimulatedPublish(ctx context.Context, policy policyRecord, testCases []policyTestCase, results *[]policyTestResult, passedCount, failedCount *int) error {
	currentPublished, err := a.listPublishedPoliciesForTenant(ctx, policy.TenantID)
	if err != nil {
		return err
	}
	effectivePolicies := make([]policyRecord, 0, len(currentPublished)+1)
	for _, published := range currentPublished {
		if published.Scope == policy.Scope && strings.TrimSpace(published.TargetName) == strings.TrimSpace(policy.TargetName) {
			continue
		}
		effectivePolicies = append(effectivePolicies, published)
	}
	simulated := policy
	simulated.Status = "published"
	effectivePolicies = append(effectivePolicies, simulated)

	if err := a.applyManagedPolicySet(ctx, effectivePolicies); err != nil {
		return err
	}
	defer func() {
		_ = a.syncPublishedPolicies(ctx, "")
	}()

	for _, testCase := range testCases {
		decision, err := a.evaluatePolicyInput(ctx, testCase)
		if err != nil {
			return err
		}
		passed := decision.Allow == testCase.ExpectedAllow && reasonsMatch(decision.Reasons, testCase.ExpectedReasons)
		if passed {
			*passedCount = *passedCount + 1
		} else {
			*failedCount = *failedCount + 1
		}
		*results = append(*results, policyTestResult{
			CaseID:          testCase.ID,
			Name:            testCase.Name,
			Passed:          passed,
			ExpectedAllow:   testCase.ExpectedAllow,
			ActualAllow:     decision.Allow,
			ExpectedReasons: append([]string(nil), testCase.ExpectedReasons...),
			ActualReasons:   append([]string(nil), decision.Reasons...),
			Decision:        decision,
		})
	}
	return nil
}

func (a *controlPlaneApp) listPolicyTestCases(ctx context.Context, tenantID, policyID string) ([]policyTestCase, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, policy_id, tenant_id, name, description, identity_payload, action, resource_payload,
			request_payload, expected_allow, expected_reasons, created_at, updated_at
		from policy_test_cases
		where tenant_id = $1 and policy_id = $2
		order by created_at asc
	`, tenantID, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var testCases []policyTestCase
	for rows.Next() {
		testCase, err := scanPolicyTestCase(rows)
		if err != nil {
			return nil, err
		}
		testCases = append(testCases, testCase)
	}
	return testCases, rows.Err()
}

func (a *controlPlaneApp) listPolicyTestRuns(ctx context.Context, tenantID, policyID string, limit int) ([]policyTestRun, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, policy_id, tenant_id, initiated_by, status, total_count, passed_count, failed_count,
			results, started_at, completed_at, created_at
		from policy_test_runs
		where tenant_id = $1 and policy_id = $2
		order by created_at desc
		limit $3
	`, tenantID, policyID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []policyTestRun
	for rows.Next() {
		run, err := scanPolicyTestRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (a *controlPlaneApp) listPublishedPoliciesForTenant(ctx context.Context, tenantID string) ([]policyRecord, error) {
	rows, err := a.db.QueryContext(ctx, `
		select id, tenant_id, coalesce(parent_policy_id::text, ''), name, scope, coalesce(target_name, ''),
			coalesce(natural_language_rule, ''), coalesce(generation_prompt, ''), coalesce(generator_model, ''),
			rego_package, rego_source, status, version, enabled, validation_errors, published_at,
			coalesce(published_by, ''), last_compiled_at, created_at, updated_at
		from policies
		where tenant_id = $1 and status = 'published' and enabled = true
		order by updated_at asc
	`, tenantID)
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

func (a *controlPlaneApp) applyManagedPolicySet(ctx context.Context, policies []policyRecord) error {
	existingIDs, err := a.listManagedOPAPolicies(ctx)
	if err != nil {
		return err
	}
	for _, existing := range existingIDs {
		if err := a.deleteOPAPolicy(ctx, existing); err != nil {
			return err
		}
	}
	for _, policy := range policies {
		if err := a.putOPAPolicy(ctx, policy.ID, policy.RegoSource); err != nil {
			return err
		}
	}
	return nil
}

func (a *controlPlaneApp) evaluatePolicyInput(ctx context.Context, testCase policyTestCase) (opaDecision, error) {
	var identity map[string]any
	if len(testCase.Identity) > 0 {
		if err := json.Unmarshal(testCase.Identity, &identity); err != nil {
			return opaDecision{}, err
		}
	}
	var resource map[string]any
	if len(testCase.Resource) > 0 {
		if err := json.Unmarshal(testCase.Resource, &resource); err != nil {
			return opaDecision{}, err
		}
	}
	var requestPayload map[string]any
	if len(testCase.Request) > 0 {
		if err := json.Unmarshal(testCase.Request, &requestPayload); err != nil {
			return opaDecision{}, err
		}
	}

	body, _ := json.Marshal(map[string]any{
		"input": map[string]any{
			"action":   testCase.Action,
			"identity": identity,
			"resource": resource,
			"request":  requestPayload,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(env("OPA_DECISION_URL", "http://opa:8181/v1/data/suez/authz/decision"), "/"), strings.NewReader(string(body)))
	if err != nil {
		return opaDecision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return opaDecision{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return opaDecision{}, fmt.Errorf("opa returned %s", resp.Status)
	}
	var decoded struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return opaDecision{}, err
	}
	if len(decoded.Result) == 0 {
		return opaDecision{}, errors.New("opa returned empty result")
	}
	var decision opaDecision
	if err := json.Unmarshal(decoded.Result, &decision); err == nil {
		return decision, nil
	}
	var allowed bool
	if err := json.Unmarshal(decoded.Result, &allowed); err == nil {
		return opaDecision{Allow: allowed, Action: testCase.Action}, nil
	}
	return opaDecision{}, errors.New("opa result format was not understood")
}

func scanPolicyTestCase(scanner interface {
	Scan(dest ...any) error
}) (policyTestCase, error) {
	var (
		testCase        policyTestCase
		expectedReasons []byte
		createdAt       time.Time
		updatedAt       time.Time
	)
	if err := scanner.Scan(
		&testCase.ID,
		&testCase.PolicyID,
		&testCase.TenantID,
		&testCase.Name,
		&testCase.Description,
		&testCase.Identity,
		&testCase.Action,
		&testCase.Resource,
		&testCase.Request,
		&testCase.ExpectedAllow,
		&expectedReasons,
		&createdAt,
		&updatedAt,
	); err != nil {
		return policyTestCase{}, err
	}
	_ = json.Unmarshal(expectedReasons, &testCase.ExpectedReasons)
	testCase.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	testCase.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return testCase, nil
}

func scanPolicyTestRun(scanner interface {
	Scan(dest ...any) error
}) (policyTestRun, error) {
	var (
		run         policyTestRun
		startedAt   time.Time
		completedAt sql.NullTime
		createdAt   time.Time
	)
	if err := scanner.Scan(
		&run.ID,
		&run.PolicyID,
		&run.TenantID,
		&run.InitiatedBy,
		&run.Status,
		&run.TotalCount,
		&run.PassedCount,
		&run.FailedCount,
		&run.Results,
		&startedAt,
		&completedAt,
		&createdAt,
	); err != nil {
		return policyTestRun{}, err
	}
	run.StartedAt = startedAt.UTC().Format(time.RFC3339)
	if completedAt.Valid {
		run.CompletedAt = completedAt.Time.UTC().Format(time.RFC3339)
	}
	run.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return run, nil
}

func reasonsMatch(actual, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(actual))
	for _, reason := range actual {
		set[reason] = struct{}{}
	}
	for _, reason := range expected {
		if _, ok := set[reason]; !ok {
			return false
		}
	}
	return true
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

