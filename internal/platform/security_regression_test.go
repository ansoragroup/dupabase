package platform

import (
	"bytes"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDumpStreamReportsProcessFailure(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf partial-dump; exit 7")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := &dumpReader{cmd: cmd, ReadCloser: stdout}
	output, err := io.ReadAll(reader)
	if string(output) != "partial-dump" || err == nil {
		t.Fatalf("failed dump accepted: output=%q error=%v", output, err)
	}
	if err := reader.Close(); err == nil {
		t.Fatal("closing a failed dump must retain the process error")
	}
}

func TestDumpFilteringPreservesPublicRLSAndCopyData(t *testing.T) {
	input := `\restrict originalkey
CREATE TABLE public.records (id text PRIMARY KEY, value text);
COPY public.records (id,value) FROM stdin;
CREATE ROLE this_is_data	first
\restrict this_is_also_data	second
\.
ALTER TABLE public.records ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_rows ON public.records USING (id=auth.uid()::text);
COPY auth.users (id) FROM stdin;
filtered_auth_data
\.
CREATE FUNCTION public.example() RETURNS text LANGUAGE sql AS $fn$
SELECT 'CREATE ROLE this_is_function_data';
$fn$;
\unrestrict originalkey
`
	path := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	filtered, err := filterSQLFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filtered)
	data, err := os.ReadFile(filtered)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"CREATE ROLE this_is_data", "\\restrict this_is_also_data", "ENABLE ROW LEVEL SECURITY", "CREATE POLICY own_rows", "CREATE ROLE this_is_function_data"} {
		if !bytes.Contains(data, []byte(wanted)) {
			t.Fatalf("dump lost protected syntax/data: %s", wanted)
		}
	}
	for _, unwanted := range []string{"originalkey", "filtered_auth_data", "COPY auth.users"} {
		if bytes.Contains(data, []byte(unwanted)) {
			t.Fatalf("filter retained excluded material: %s", unwanted)
		}
	}
}

func TestCustomDumpFilteringKeepsPublicObjectsWithReservedNames(t *testing.T) {
	toc := "1; 1259 1 TABLE public auth postgres\n2; 0 1 TABLE DATA public auth postgres\n3; 1259 2 TABLE auth users postgres\n4; 0 0 SCHEMA - auth postgres\n5; 0 0 COMMENT - SCHEMA auth postgres\n6; 1255 3 FUNCTION public extensions() postgres\n"
	filtered := filterTOC(toc)
	for _, entry := range []string{"TABLE public auth", "TABLE DATA public auth", "FUNCTION public extensions()"} {
		if !strings.Contains(filtered, entry) {
			t.Fatalf("legitimate public object excluded: %s", entry)
		}
	}
	for _, entry := range []string{"TABLE auth users", "SCHEMA - auth", "COMMENT - SCHEMA auth"} {
		if strings.Contains(filtered, entry) {
			t.Fatalf("excluded auth object retained: %s", entry)
		}
	}
}

func TestPGWorkerEnvironmentPreservesTLSAndExcludesPlatformSecrets(t *testing.T) {
	t.Setenv("PLATFORM_JWT_SECRET", "test-only-platform-secret")
	t.Setenv("DATABASE_URL", "test-only-operator-credential")
	t.Setenv("ADMIN_PASSWORD", "test-only-admin-password")
	env := strings.Join(pgCommandEnv("postgresql://project:project-password@db.example/project?sslmode=verify-full&sslrootcert=%2Fcerts%2Fca.pem&sslcert=%2Fcerts%2Fclient.pem"), "\n")
	for _, expected := range []string{"PGPASSWORD=project-password", "PGSSLMODE=verify-full", "PGSSLROOTCERT=/certs/ca.pem", "PGSSLCERT=/certs/client.pem"} {
		if !strings.Contains(env, expected) {
			t.Errorf("missing TLS/connection parameter %s", expected)
		}
	}
	for _, secret := range []string{"test-only-platform-secret", "test-only-operator-credential", "test-only-admin-password"} {
		if strings.Contains(env, secret) {
			t.Fatal("worker inherited a platform secret")
		}
	}
}

func TestCommandOutputAndJobAdmissionAreBounded(t *testing.T) {
	var output boundedCommandOutput
	input := bytes.Repeat([]byte("x"), 2<<20)
	n, err := output.Write(input)
	if err != nil || n != len(input) || output.Len() != 1<<20 {
		t.Fatal("command output was not bounded without blocking the writer")
	}
	s := NewImportService(nil, "")
	var releases []func()
	for _, project := range []string{"a", "b", "c", "d"} {
		release, err := s.reserveJob(project)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := s.reserveJob("a"); err == nil {
		t.Fatal("concurrent imports into one project admitted")
	}
	if _, err := s.reserveJob("e"); err == nil {
		t.Fatal("unbounded global imports admitted")
	}
	for _, release := range releases {
		release()
	}
	if release, err := s.reserveJob("a"); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
}

func TestStorageEgressRequiresOperatorApprovalForPrivateEndpoints(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.0.0.1", "172.16.0.1", "192.168.0.1", "::ffff:127.0.0.1", "100.64.0.1"} {
		if publicStorageAddress(netip.MustParseAddr(raw)) {
			t.Fatalf("private/metadata address accepted: %s", raw)
		}
	}
	if !publicStorageAddress(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public addresses rejected")
	}
	t.Setenv("BACKUP_ALLOWED_ENDPOINTS", "http://minio:9000")
	if !approvedStorageEndpoint("http://minio:9000/bucket") || approvedStorageEndpoint("http://minio:9001") || approvedStorageEndpoint("http://minio.attacker:9000") {
		t.Fatal("endpoint approval does not match exact origin")
	}
	for _, endpoint := range []string{"file:///tmp/file", "http://user:pass@host", "https://host/#fragment"} {
		if _, err := endpointOrigin(endpoint); err == nil {
			t.Fatalf("invalid storage endpoint accepted: %s", endpoint)
		}
	}
}
