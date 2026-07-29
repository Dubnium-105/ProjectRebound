package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/observability"
	"github.com/jackc/pgx/v5"
)

var steamIDPattern = regexp.MustCompile(`^[0-9]{16,20}$`)

type legacyDocument struct {
	PlayerID string                     `json:"playerId"`
	Roles    map[string]json.RawMessage `json:"roles"`
}

type report struct {
	DryRun           bool     `json:"dry_run"`
	Files            int      `json:"files"`
	PlayersMapped    int      `json:"players_mapped"`
	LoadoutsReady    int      `json:"loadouts_ready"`
	LoadoutsImported int      `json:"loadouts_imported"`
	Conflicts        []string `json:"conflicts"`
	Errors           []string `json:"errors"`
}

func main() {
	source := flag.String("source", "", "legacy loadout directory (read-only)")
	configPath := flag.String("config", "config.control-plane.yaml", "path to configuration")
	apply := flag.Bool("apply", false, "commit the validated import transaction")
	dryRun := flag.Bool("dry-run", false, "validate and report without writing (the default)")
	flag.Parse()
	logger := observability.NewLogger(os.Stderr, config.Defaults.Logging)
	slog.SetDefault(logger)
	if strings.TrimSpace(*source) == "" {
		logger.Error("--source is required")
		os.Exit(2)
	}
	if *apply && *dryRun {
		logger.Error("--apply and --dry-run are mutually exclusive")
		os.Exit(2)
	}
	absoluteSource, err := filepath.Abs(*source)
	if err != nil {
		logger.Error("resolve source", "error", err)
		os.Exit(2)
	}
	info, err := os.Stat(absoluteSource)
	if err != nil || !info.IsDir() {
		logger.Error("source must be an existing directory")
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	definitions, err := metaserver.LoadDefinitionIndex()
	if err != nil {
		logger.Error("load pinned definitions", "error", err)
		os.Exit(1)
	}
	result, err := runImport(ctx, pool, definitions, absoluteSource, *apply)
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if err != nil || len(result.Errors) > 0 || len(result.Conflicts) > 0 {
		if err != nil {
			logger.Error("MetaServer import failed", "error", err)
		}
		os.Exit(1)
	}
}

type dbPool interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

func runImport(
	ctx context.Context,
	pool dbPool,
	definitions *metaserver.DefinitionIndex,
	source string,
	apply bool,
) (report, error) {
	result := report{DryRun: !apply, Conflicts: []string{}, Errors: []string{}}
	files, err := filepath.Glob(filepath.Join(source, "*.json"))
	if err != nil {
		return result, err
	}
	sort.Strings(files)
	result.Files = len(files)
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := time.Now().UTC()
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			result.Errors = append(result.Errors, filepath.Base(path)+": read failed")
			continue
		}
		var document legacyDocument
		if err := json.Unmarshal(raw, &document); err != nil || document.PlayerID == "" || document.Roles == nil {
			result.Errors = append(result.Errors, filepath.Base(path)+": invalid legacy document")
			continue
		}
		playerID, err := resolvePlayer(ctx, tx, document.PlayerID)
		if err != nil {
			if err == pgx.ErrNoRows {
				result.Errors = append(result.Errors, filepath.Base(path)+": player mapping not found")
				continue
			}
			return result, err
		}
		result.PlayersMapped++
		for roleID, snapshot := range document.Roles {
			if !definitions.HasRole(roleID) {
				result.Errors = append(result.Errors, filepath.Base(path)+": unknown role "+roleID)
				continue
			}
			if len(snapshot) == 0 || len(snapshot) > 2<<20 || !json.Valid(snapshot) {
				result.Errors = append(result.Errors, filepath.Base(path)+": invalid role snapshot "+roleID)
				continue
			}
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM meta_role_loadouts WHERE player_id = $1 AND role_id = $2
				)
			`, playerID, roleID).Scan(&exists); err != nil {
				return result, err
			}
			if exists {
				result.Conflicts = append(result.Conflicts, playerID+":"+roleID)
				continue
			}
			canonical, err := canonicalJSON(snapshot)
			if err != nil {
				result.Errors = append(result.Errors, filepath.Base(path)+": invalid role object "+roleID)
				continue
			}
			var snapshotObject map[string]any
			if err := json.Unmarshal(canonical, &snapshotObject); err != nil {
				result.Errors = append(result.Errors, filepath.Base(path)+": invalid role object "+roleID)
				continue
			}
			if err := definitions.ValidateLoadoutSnapshot(roleID, snapshotObject); err != nil {
				result.Errors = append(
					result.Errors,
					filepath.Base(path)+": definition validation failed for "+roleID,
				)
				continue
			}
			digest := sha256.Sum256(canonical)
			result.LoadoutsReady++
			if apply {
				if _, err := tx.Exec(ctx, `
					INSERT INTO meta_role_loadouts (
						player_id, role_id, snapshot, snapshot_sha256,
						revision, created_at, updated_at
					) VALUES ($1, $2, $3::jsonb, $4, 1, $5, $5)
				`, playerID, roleID, canonical, digest[:], now); err != nil {
					return result, err
				}
				result.LoadoutsImported++
			}
		}
	}
	if len(result.Errors) > 0 || len(result.Conflicts) > 0 || !apply {
		return result, tx.Rollback(ctx)
	}
	return result, tx.Commit(ctx)
}

func resolvePlayer(ctx context.Context, tx pgx.Tx, sourceID string) (string, error) {
	var playerID string
	err := tx.QueryRow(ctx, `SELECT id FROM players WHERE id = $1`, sourceID).Scan(&playerID)
	if err == nil {
		return playerID, nil
	}
	if err != pgx.ErrNoRows || !steamIDPattern.MatchString(sourceID) {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM players WHERE steam_id = $1`, sourceID).Scan(&playerID)
	return playerID, err
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("snapshot must be an object")
	}
	return json.Marshal(object)
}
