package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadFixturesRunsMigrationAndFixturesInOneTransaction(t *testing.T) {
	repoRoot := repositoryRoot(t)
	tempDir := t.TempDir()
	argsPath := filepath.Join(tempDir, "psql-args")
	countPath := filepath.Join(tempDir, "psql-count")
	fakePSQL := filepath.Join(tempDir, "psql")
	fake := `#!/bin/sh
printf 'called\n' >> "$PSQL_COUNT_PATH"
printf '%s\n' "$@" > "$PSQL_ARGS_PATH"
`
	if err := os.WriteFile(fakePSQL, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/bash", filepath.Join(repoRoot, "scripts", "load-fixtures.sh"))
	cmd.Dir = tempDir
	cmd.Env = append(withoutEnvironmentVariable(os.Environ(), "DATABASE_URL"),
		"DATABASE_URL=postgres://postgres:postgres@localhost:5432/actionguard?sslmode=disable",
		"PATH="+tempDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PSQL_ARGS_PATH="+argsPath,
		"PSQL_COUNT_PATH="+countPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("load fixtures: %v\n%s", err, output)
	}

	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(count), "called\n"); got != 1 {
		t.Fatalf("psql invocation count = %d, want 1", got)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"postgres://postgres:postgres@localhost:5432/actionguard?sslmode=disable",
		"--single-transaction",
		"-v",
		"ON_ERROR_STOP=1",
		"-f",
		filepath.Join(repoRoot, "migrations", "001_commerce.sql"),
		"-f",
		filepath.Join(repoRoot, "fixtures", "commerce.sql"),
	}
	if got, want := strings.Split(strings.TrimSpace(string(args)), "\n"), wantArgs; !equalStrings(got, want) {
		t.Fatalf("psql args = %q, want %q", got, want)
	}
}

func TestLoadFixturesRequiresDatabaseURL(t *testing.T) {
	cmd := exec.Command("/bin/bash", filepath.Join(repositoryRoot(t), "scripts", "load-fixtures.sh"))
	cmd.Env = withoutEnvironmentVariable(os.Environ(), "DATABASE_URL")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("load fixtures succeeded without DATABASE_URL")
	}
	if got, want := strings.TrimSpace(string(output)), "error: DATABASE_URL is required"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestLoadFixturesRequiresPSQL(t *testing.T) {
	cmd := exec.Command("/bin/bash", filepath.Join(repositoryRoot(t), "scripts", "load-fixtures.sh"))
	cmd.Env = append(withoutEnvironmentVariable(os.Environ(), "DATABASE_URL"),
		"DATABASE_URL=postgres://localhost/actionguard",
		"PATH="+t.TempDir(),
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("load fixtures succeeded without psql")
	}
	if got, want := strings.TrimSpace(string(output)), "error: psql is required to load fixtures"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestComposeSeedsDatabaseBeforeStartingMCP(t *testing.T) {
	composePath := filepath.Join(repositoryRoot(t), "deploy", "docker-compose.yml")
	contents, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	compose := string(contents)

	loaderStart := strings.Index(compose, "  fixture-loader:")
	mcpService := strings.Index(compose, "  commerce-mcp:")
	if loaderStart < 0 || mcpService < 0 || loaderStart > mcpService {
		t.Fatalf("fixture-loader must be defined before commerce-mcp")
	}
	loaderBlock := compose[loaderStart:mcpService]
	requiredLoaderFragments := []string{
		"image: pgvector/pgvector:pg16",
		"condition: service_healthy",
		"../migrations/001_commerce.sql:/migrations/001_commerce.sql:ro",
		"../fixtures/commerce.sql:/fixtures/commerce.sql:ro",
		"--single-transaction",
		"/migrations/001_commerce.sql",
		"/fixtures/commerce.sql",
	}
	for _, fragment := range requiredLoaderFragments {
		if !strings.Contains(loaderBlock, fragment) {
			t.Errorf("fixture-loader missing %q", fragment)
		}
	}
	migration := strings.Index(loaderBlock, "/migrations/001_commerce.sql")
	fixture := strings.Index(loaderBlock, "/fixtures/commerce.sql")
	if migration < 0 || fixture < 0 || migration > fixture {
		t.Fatalf("fixture-loader must run migration before fixtures")
	}
	mcpBlock := compose[mcpService:]
	if !strings.Contains(mcpBlock, "fixture-loader:") || !strings.Contains(mcpBlock, "condition: service_completed_successfully") {
		t.Fatalf("commerce-mcp must wait for fixture-loader completion")
	}
}

func withoutEnvironmentVariable(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, variable := range environment {
		if !strings.HasPrefix(variable, prefix) {
			filtered = append(filtered, variable)
		}
	}
	return filtered
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
