package platform

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Alpine supplies patched 16–18 clients. Client 16 also supports servers 14–15.
// Using client 18 everywhere would emit settings older servers cannot accept.
var pgClientMinimum = map[int]int{16: 15, 17: 11, 18: 6}
var pgToolVersions sync.Map
var pgVersionPattern = regexp.MustCompile(`\(PostgreSQL\)\s+(\d+)\.(\d+)`)
var dumpSourcePattern = regexp.MustCompile(`(?m)^[;\-]+\s*Dumped from database version:?\s+(\d+)\.`)
var dumpClientPattern = regexp.MustCompile(`(?m)^[;\-]+\s*Dumped by pg_dump version:?\s+(\d+)\.`)

func pgClientMajor(serverMajor int) (int, error) {
	if serverMajor < 14 || serverMajor > 18 {
		return 0, fmt.Errorf("PostgreSQL %d backup/import compatibility is not verified; supported servers are 14–18", serverMajor)
	}
	if serverMajor < 16 {
		return 16, nil
	}
	return serverMajor, nil
}

func pgServerMajor(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	if pool == nil {
		return 0, fmt.Errorf("PostgreSQL version provider is not configured")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire PostgreSQL version: %w", err)
	}
	defer conn.Release()
	version := conn.Conn().PgConn().ParameterStatus("server_version")
	major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	if err != nil {
		return 0, fmt.Errorf("invalid PostgreSQL server version")
	}
	if _, err := pgClientMajor(major); err != nil {
		return 0, err
	}
	return major, nil
}

func pgTool(ctx context.Context, name string, major int) (string, error) {
	if name != "pg_dump" && name != "pg_restore" && name != "psql" {
		return "", fmt.Errorf("unsupported PostgreSQL tool")
	}
	minimum, ok := pgClientMinimum[major]
	if !ok {
		return "", fmt.Errorf("unsupported PostgreSQL client %d", major)
	}
	var candidates []string
	if root := os.Getenv("POSTGRES_CLIENT_BIN_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, strconv.Itoa(major), name))
	}
	candidates = append(candidates,
		fmt.Sprintf("/usr/libexec/postgresql%d/%s", major, name),
		fmt.Sprintf("/usr/lib/postgresql/%d/bin/%s", major, name),
		fmt.Sprintf("/opt/homebrew/opt/postgresql@%d/bin/%s", major, name),
	)
	if path, err := exec.LookPath(name); err == nil {
		candidates = append(candidates, path)
	}
	for _, path := range candidates {
		if cached, ok := pgToolVersions.Load(path); ok {
			version := cached.([2]int)
			if version[0] == major && version[1] >= minimum {
				return path, nil
			}
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(checkCtx, path, "--version")
		cmd.Env = pgCommandEnv("")
		out, err := runBoundedCommand(cmd)
		cancel()
		match := pgVersionPattern.FindStringSubmatch(string(out))
		if err != nil || len(match) != 3 {
			continue
		}
		parsedMajor, _ := strconv.Atoi(match[1])
		minor, _ := strconv.Atoi(match[2])
		pgToolVersions.Store(path, [2]int{parsedMajor, minor})
		if parsedMajor == major && minor >= minimum {
			return path, nil
		}
	}
	return "", fmt.Errorf("install %s client %d.%d or newer in that major; the Dupabase container includes it", name, major, minimum)
}

func pgLatestTool(ctx context.Context, name string) (string, error) {
	for _, major := range []int{18, 17, 16} {
		if path, err := pgTool(ctx, name, major); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("a patched PostgreSQL 16–18 %s client is required", name)
}

func pgPSQLCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	path, err := pgLatestTool(ctx, "psql")
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, path, args...), nil
}

type pgRestorePlan struct {
	tool string
	toc  string
}

func validateDumpTarget(content string, target int, requireHeader bool) error {
	client, err := pgClientMajor(target)
	if err != nil {
		return err
	}
	source := dumpSourcePattern.FindStringSubmatch(content)
	producer := dumpClientPattern.FindStringSubmatch(content)
	if requireHeader && (len(source) < 2 || len(producer) < 2) {
		return fmt.Errorf("dump archive is missing PostgreSQL version metadata")
	}
	if len(source) >= 2 {
		sourceMajor, _ := strconv.Atoi(source[1])
		if sourceMajor > target {
			return fmt.Errorf("cannot restore PostgreSQL %d data into older PostgreSQL %d; use a same-major or newer destination", sourceMajor, target)
		}
	}
	if len(producer) >= 2 {
		producerMajor, _ := strconv.Atoi(producer[1])
		if producerMajor > client {
			return fmt.Errorf("dump produced by pg_dump %d requires a newer destination/client than PostgreSQL %d; re-export the source with a compatible pg_dump", producerMajor, target)
		}
	}
	return nil
}

func preparePGRestore(ctx context.Context, pool *pgxpool.Pool, file string) (*pgRestorePlan, error) {
	target, err := pgServerMajor(ctx, pool)
	if err != nil {
		return nil, err
	}
	reader, err := pgLatestTool(ctx, "pg_restore")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, reader, "--list", file)
	cmd.Env = pgCommandEnv("")
	toc, err := runBoundedCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("inspect dump before restore: %w", err)
	}
	if err := validateDumpTarget(string(toc), target, true); err != nil {
		return nil, err
	}
	major, _ := pgClientMajor(target)
	tool, err := pgTool(ctx, "pg_restore", major)
	if err != nil {
		return nil, err
	}
	return &pgRestorePlan{tool: tool, toc: string(toc)}, nil
}

func preflightSQLDump(ctx context.Context, pool *pgxpool.Pool, file string) error {
	target, err := pgServerMajor(ctx, pool)
	if err != nil {
		return err
	}
	if _, err := pgLatestTool(ctx, "psql"); err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	header, err := io.ReadAll(io.LimitReader(f, 64*1024))
	if err != nil {
		return err
	}
	return validateDumpTarget(string(header), target, false)
}
