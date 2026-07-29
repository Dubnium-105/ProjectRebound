package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/admin"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
)

func main() {
	configPath := flag.String("config", "config.control-plane.yaml", "path to the control-plane YAML configuration")
	username := flag.String("username", "", "administrator login username")
	displayName := flag.String("display-name", "", "administrator display name")
	role := flag.String("role", "SUPER_ADMIN", "initial administrator role")
	flag.Parse()

	if strings.TrimSpace(*username) == "" {
		fatal("username is required")
	}
	password := os.Getenv("ADMINCTL_PASSWORD")
	if password == "" {
		fatal("ADMINCTL_PASSWORD is required")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load configuration: %v", err)
	}
	if strings.TrimSpace(cfg.Admin.MFAEncryptionKeyBase64) == "" {
		fatal("ADMIN_MFA_ENCRYPTION_KEY_BASE64 is required so the account remains usable after restart")
	}
	secretBox, ephemeral, err := admin.NewSecretBox(cfg.Admin.MFAEncryptionKeyBase64, cfg.Environment)
	if err != nil {
		fatal("initialize MFA encryption: %v", err)
	}
	if ephemeral {
		fatal("refusing to create an administrator with an ephemeral MFA encryption key")
	}
	passwordHash, err := admin.HashPassword(password)
	if err != nil {
		fatal("hash password: %v", err)
	}
	totpSecret, err := admin.NewTOTPSecret()
	if err != nil {
		fatal("generate TOTP secret: %v", err)
	}
	recoveryCodes, recoveryHashes, err := admin.NewRecoveryCodes(10)
	if err != nil {
		fatal("generate recovery codes: %v", err)
	}
	adminID := auth.NewID("adm_")
	encryptedSecret, err := secretBox.Encrypt(adminID, totpSecret)
	if err != nil {
		fatal("encrypt TOTP secret: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		fatal("open database: %v", err)
	}
	defer pool.Close()
	if err := database.NewMigrator(pool.Pool).Up(ctx); err != nil {
		fatal("apply database migrations: %v", err)
	}
	now := time.Now().UTC()
	name := strings.TrimSpace(*displayName)
	if name == "" {
		name = strings.TrimSpace(*username)
	}
	user := admin.AdminUser{
		ID:           adminID,
		Username:     strings.ToLower(strings.TrimSpace(*username)),
		DisplayName:  name,
		PasswordHash: passwordHash,
		Status:       admin.AdminStatusActive,
		MFARequired:  true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := admin.NewAuthRepository(pool.Pool).CreateAdmin(
		ctx,
		user,
		encryptedSecret,
		recoveryHashes,
		strings.ToUpper(strings.TrimSpace(*role)),
	); err != nil {
		fatal("create administrator: %v", err)
	}

	fmt.Printf("Administrator created: %s (%s)\n", user.Username, user.ID)
	fmt.Printf("TOTP provisioning URI:\n%s\n", admin.TOTPProvisioningURI("ProjectRebound Admin", user.Username, totpSecret))
	fmt.Println("Recovery codes (store once, then remove this output):")
	for _, code := range recoveryCodes {
		fmt.Println(code)
	}
}

func fatal(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}
