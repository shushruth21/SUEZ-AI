package main

import (
	"net/http"
	"sort"
	"strings"
)

// Permissions are the unit of authorization. Every control-plane route maps to
// exactly one permission in routePermission, and the gateway checks the same
// names for its actions. Roles and API-key scopes are only bundles of
// permissions; nothing checks a role name directly.
const (
	permSessionRead   = "session.read"
	permCatalogRead   = "catalog.read"
	permCatalogManage = "catalog.manage"
	permAccessRequest = "access.request"
	permAccessReview  = "access.review"
	permGrantsRead    = "grants.read"
	permTenantsRead   = "tenants.read"
	permTenantsManage = "tenants.manage"
	permAPIKeysManage = "apikeys.manage"
	permUsersManage   = "users.manage"
	permPolicyRead    = "policy.read"
	permPolicyManage  = "policy.manage"
	permAuditRead     = "audit.read"
	permGatewayInvoke = "gateway.invoke"
	permGatewayTools  = "gateway.tools"
	permUsageRead     = "usage.read"
	permMemoryRead    = "memory.read"
	permMemoryWrite   = "memory.write"
	permMemoryManage  = "memory.manage"
)

const (
	scopeReadOnly         = "read-only"
	scopeGatewayInference = "gateway-inference"
	scopePolicyAdmin      = "policy-admin"
	scopeAuditReader      = "audit-reader"
	scopeTenantAdmin      = "tenant-admin"
	// scopePlatformAdmin allows creating and listing tenants. It cannot be
	// granted through the API by someone who does not already hold it.
	scopePlatformAdmin = "platform-admin"
)

var readOnlyPermissions = []string{permSessionRead, permCatalogRead, permGrantsRead, permTenantsRead, permUsageRead}

var scopePermissions = map[string][]string{
	scopeReadOnly: readOnlyPermissions,
	scopeGatewayInference: append([]string{
		permGatewayInvoke, permGatewayTools, permAccessRequest, permMemoryRead, permMemoryWrite,
	}, readOnlyPermissions...),
	scopePolicyAdmin: append([]string{permPolicyRead, permPolicyManage}, readOnlyPermissions...),
	scopeAuditReader: append([]string{permAuditRead}, readOnlyPermissions...),
	scopeTenantAdmin: append([]string{
		permCatalogManage, permAccessRequest, permAccessReview, permAPIKeysManage, permUsersManage,
		permPolicyRead, permPolicyManage, permAuditRead, permGatewayInvoke, permGatewayTools,
		permMemoryRead, permMemoryWrite, permMemoryManage,
	}, readOnlyPermissions...),
	scopePlatformAdmin: {permSessionRead, permTenantsRead, permTenantsManage},
}

// roleScopes maps the role stored on a user to scopes.
var roleScopes = map[string][]string{
	"admin":        {scopeTenantAdmin},
	"policy-admin": {scopePolicyAdmin},
	"auditor":      {scopeAuditReader},
	"user":         {scopeGatewayInference},
	"read-only":    {scopeReadOnly},
}

func validScope(scope string) bool {
	_, ok := scopePermissions[scope]
	return ok
}

func validRole(role string) bool {
	_, ok := roleScopes[role]
	return ok
}

func permissionsForScopes(scopes []string) []string {
	set := map[string]struct{}{}
	for _, scope := range scopes {
		for _, permission := range scopePermissions[scope] {
			set[permission] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for permission := range set {
		out = append(out, permission)
	}
	sort.Strings(out)
	return out
}

func hasPermission(identity identityContext, permission string) bool {
	for _, held := range identity.Permissions {
		if held == permission {
			return true
		}
	}
	return false
}

func hasScope(identity identityContext, scope string) bool {
	for _, held := range identity.Scopes {
		if held == scope {
			return true
		}
	}
	return false
}

// canGrantScopes reports whether identity may hand out scopes: it must hold
// every permission the scopes carry, so a key can never exceed its creator.
func canGrantScopes(identity identityContext, scopes []string) bool {
	for _, permission := range permissionsForScopes(scopes) {
		if !hasPermission(identity, permission) {
			return false
		}
	}
	return true
}

// routePermission is the single authorization table for the control plane.
// A route that is not listed is refused: new endpoints must be added here.
func routePermission(method, path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	read := method == http.MethodGet || method == http.MethodHead
	switch {
	case path == "/api/v1/auth/session":
		return permSessionRead, read
	case path == "/api/v1/tenants":
		if read {
			return permTenantsRead, true
		}
		return permTenantsManage, method == http.MethodPost
	case path == "/api/v1/api-keys" || strings.HasPrefix(path, "/api/v1/api-keys/"):
		return permAPIKeysManage, true
	case path == "/api/v1/users" || strings.HasPrefix(path, "/api/v1/users/"):
		return permUsersManage, true
	case path == "/api/v1/access-requests":
		if read {
			return permGrantsRead, true
		}
		return permAccessRequest, method == http.MethodPost
	case strings.HasPrefix(path, "/api/v1/access-requests/"):
		return permAccessReview, method == http.MethodPost
	case path == "/api/v1/access-grants":
		return permGrantsRead, read
	case strings.HasPrefix(path, "/api/v1/access-grants/"):
		return permAccessReview, method == http.MethodDelete
	case path == "/api/v1/models" || path == "/api/v1/tools":
		if read {
			return permCatalogRead, true
		}
		return permCatalogManage, method == http.MethodPost
	case path == "/api/v1/policies" || strings.HasPrefix(path, "/api/v1/policies/"):
		if read {
			return permPolicyRead, true
		}
		return permPolicyManage, method == http.MethodPost
	case path == "/api/v1/audit-log" || strings.HasPrefix(path, "/api/v1/audit-log/"):
		return permAuditRead, read
	case strings.HasPrefix(path, "/api/v1/memory/"):
		return memoryRoutePermission(method, path)
	}
	return "", false
}
