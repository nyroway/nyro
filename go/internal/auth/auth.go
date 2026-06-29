// Package auth implements the outbound credential layer (OAuth driver framework).
// See driver.go for the AuthDriver interface and registry.
//
// Inbound API-key auth + quotas lives in proxy/inbound.go (checkAccess). The
// old Authorize stub that was here during P1 has been removed.
package auth
