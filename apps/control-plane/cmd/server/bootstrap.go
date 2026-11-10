package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// runBootstrap creates the first tenant and its first API key in an empty
// deployment:
//
//	control-plane bootstrap --tenant-slug acme --tenant-name "Acme Inc"
//
// The key holds tenant-admin and platform-admin and is printed once to
// stdout; only its hash is stored. The command refuses to run when any API
// key already exists, so it cannot be used to mint a second root credential.
func runBootstrap(args []string) error {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	slug := flags.String("tenant-slug", "", "slug of the first tenant (lowercase letters, digits, dashes)")
	name := flags.String("tenant-name", "", "display name of the first tenant")
	environment := flags.String("environment", "prod", "tenant environment label")
	expiresInDays := flags.Int("expires-in-days", 30, "lifetime of the bootstrap key; rotate to a long-lived key before it ends")
	if err := flags.Parse(args); err != nil {
		return err
	}
	req := createTenantRequest{Name: strings.TrimSpace(*name), Slug: strings.TrimSpace(*slug), Environment: strings.TrimSpace(*environment)}
	if err := validateTenantRequest(req); err != nil {
		return err
	}
	if *expiresInDays < 1 || *expiresInDays > 365 {
		return errors.New("--expires-in-days must be between 1 and 365")
	}
	databaseURL := env("DATABASE_URL", "")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return errors.New("database configuration is not valid")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var existing int
	if err := db.QueryRowContext(ctx, `select count(*) from api_keys`).Scan(&existing); err != nil {
		return fmt.Errorf("database is not ready (have migrations been applied?): %w", err)
	}
	if existing > 0 {
		return errors.New("this deployment already has API keys; bootstrap only runs on an empty deployment")
	}
	app := &controlPlaneApp{db: db}
	created, err := app.createTenant(ctx, req)
	if err != nil {
		return fmt.Errorf("could not create the tenant: %w", err)
	}
	expires := time.Now().Add(time.Duration(*expiresInDays) * 24 * time.Hour)
	bootstrapIdentity := identityContext{TenantID: created.ID, Actor: "bootstrap"}
	key, rawKey, err := app.createAPIKey(ctx, bootstrapIdentity, createAPIKeyRequest{
		TenantID: created.ID, Label: "Bootstrap admin key", Scopes: []string{scopeTenantAdmin, scopePlatformAdmin},
	}, &expires)
	if err != nil {
		return fmt.Errorf("could not create the key: %w", err)
	}
	_ = insertAuditRecord(ctx, db, created.ID, "bootstrap", "deployment.bootstrap", "tenant", created.Slug, "allow", map[string]any{"apiKeyId": key.ID, "expiresAt": key.ExpiresAt})
	fmt.Fprintf(os.Stderr, "Created tenant %s (%s). The key below is shown once and expires %s.\n", created.Slug, created.ID, key.ExpiresAt)
	fmt.Println(rawKey)
	return nil
}
