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
	policyImport := strings.Index(compose, "  policy-import:")
	if loaderStart < 0 || policyImport < 0 {
		t.Fatal("fixture-loader and policy-import services are required")
	}
	loaderBlock := compose[loaderStart:policyImport]
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
	policyBlock := compose[policyImport:]
	if !strings.Contains(policyBlock, "fixture-loader:") || !strings.Contains(policyBlock, "condition: service_completed_successfully") {
		t.Fatalf("policy-import must wait for fixture-loader completion")
	}
}

func TestDockerfileBuildsAllPhaseTwoCommandsWithoutFixedEntrypoint(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(contents)
	for _, fragment := range []string{
		"-o /app/api ./cmd/api",
		"-o /app/commerce-mcp ./cmd/commerce-mcp",
		"-o /app/policy-import ./cmd/policy-import",
		"apk add --no-cache ca-certificates",
		"COPY --from=build",
		"/app /app",
		"EXPOSE 8080 8081",
	} {
		if !strings.Contains(dockerfile, fragment) {
			t.Errorf("Dockerfile missing %q", fragment)
		}
	}
	if strings.Contains(dockerfile, "ENTRYPOINT") {
		t.Fatal("Dockerfile must not select a fixed entrypoint; Compose selects the command")
	}
}

func TestComposeStartsDeterministicPhaseTwoTopology(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "deploy", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	compose := string(contents)
	postgres := serviceBlock(t, compose, "postgres")
	fixtureLoader := serviceBlock(t, compose, "fixture-loader")
	policyImport := serviceBlock(t, compose, "policy-import")
	mcp := serviceBlock(t, compose, "commerce-mcp")
	api := serviceBlock(t, compose, "api")

	for _, fragment := range []string{"healthcheck:", "pg_isready -U postgres -d actionguard"} {
		if !strings.Contains(postgres, fragment) {
			t.Errorf("postgres missing %q", fragment)
		}
	}
	requireFragments(t, fixtureLoader, "postgres:", "condition: service_healthy")
	requireFragments(t, policyImport,
		"command: [\"/app/policy-import\"]",
		"fixture-loader:",
		"condition: service_completed_successfully",
		"LLM_PROVIDER: ${LLM_PROVIDER:-deterministic}",
		"EMBEDDING_PROVIDER: ${EMBEDDING_PROVIDER:-deterministic}",
		"OPENAI_API_KEY: ${OPENAI_API_KEY:-}",
		"OPENAI_EMBEDDING_MODEL: ${OPENAI_EMBEDDING_MODEL:-}",
	)
	requireFragments(t, mcp,
		"command: [\"/app/commerce-mcp\"]",
		"policy-import:",
		"condition: service_completed_successfully",
		"MCP_ADDR: :8081",
		"MCP_GATEWAY_TOKEN: actionguard-demo-gateway-token",
		"healthcheck:",
	)
	requireFragments(t, api,
		"command: [\"/app/api\"]",
		"commerce-mcp:",
		"condition: service_healthy",
		"MCP_URL: http://commerce-mcp:8081/mcp",
		"LLM_PROVIDER: ${LLM_PROVIDER:-deterministic}",
		"EMBEDDING_PROVIDER: ${EMBEDDING_PROVIDER:-deterministic}",
		"OPENAI_BASE_URL: ${OPENAI_BASE_URL:-https://api.openai.com}",
		"OPENAI_API_KEY: ${OPENAI_API_KEY:-}",
		"OPENAI_MODEL: ${OPENAI_MODEL:-}",
		"OPENAI_EMBEDDING_MODEL: ${OPENAI_EMBEDDING_MODEL:-}",
		"DEMO_FULL_TOKEN: actionguard-demo-full-token",
		"DEMO_READ_ONLY_TOKEN: actionguard-demo-read-only-token",
		"DEMO_USER_999_TOKEN: actionguard-demo-user-999-token",
	)
}

func serviceBlock(t *testing.T, compose, service string) string {
	t.Helper()
	var block []string
	found := false
	for _, line := range strings.Split(compose, "\n") {
		isService := strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":")
		if isService {
			if found {
				break
			}
			found = line == "  "+service+":"
		}
		if found {
			block = append(block, line)
		}
	}
	if !found {
		t.Fatalf("Compose service %q is missing", service)
	}
	return strings.Join(block, "\n")
}

func requireFragments(t *testing.T, contents string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(contents, fragment) {
			t.Errorf("service configuration missing %q", fragment)
		}
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
