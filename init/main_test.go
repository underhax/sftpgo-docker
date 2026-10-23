package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

var (
	errMockExec    = errors.New("simulated exec error")
	errMockReadDir = errors.New("simulated readdir error")
	errMockRead    = errors.New("simulated read error")
)

var (
	recordedExitCode     = -1
	recordedExecArgs     []string
	recordedExecEnv      []string
	mockTargetSecretPath string
	redirectedSecretsDir string
)

func mockExit(code int) {
	recordedExitCode = code
}

func mockFailingReadDir(_ string) ([]fs.DirEntry, error) {
	return nil, errMockReadDir
}

func mockFailingReadFile(_ string) ([]byte, error) {
	return nil, errMockRead
}

func mockConditionalReadFile(path string) ([]byte, error) {
	if mockTargetSecretPath != "" && path == mockTargetSecretPath {
		return nil, errMockRead
	}
	return defaultReadFile(path)
}

func mockRedirectedReadFile(path string) ([]byte, error) {
	base := filepath.Base(path)
	return defaultReadFile(filepath.Join(redirectedSecretsDir, base))
}

func mockRedirectedReadDir(_ string) ([]fs.DirEntry, error) {
	return defaultReadDir(redirectedSecretsDir)
}

func mockSuccessfulExec(_ context.Context, args, env []string) error {
	recordedExecArgs = args
	recordedExecEnv = env
	return nil
}

func mockFailingExec(_ context.Context, _, _ []string) error {
	return errMockExec
}

func TestDefaultReadFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")
	expectedContent := []byte("secret_value")

	if err := os.WriteFile(filePath, expectedContent, 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	data, err := defaultReadFile(filePath)
	if err != nil {
		t.Fatalf("unexpected error reading file: %v", err)
	}

	if !bytes.Equal(data, expectedContent) {
		t.Fatalf("expected content %q, got %q", expectedContent, data)
	}

	if _, err := defaultReadFile(filepath.Join(tempDir, "non_existent.txt")); err == nil {
		t.Fatal("expected error reading non-existent file, got nil")
	}
}

func TestDefaultReadDir(t *testing.T) {
	tempDir := t.TempDir()

	entries, err := defaultReadDir(tempDir)
	if err != nil {
		t.Fatalf("unexpected error reading directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}

	if _, err := defaultReadDir(filepath.Join(tempDir, "non_existent_dir")); err == nil {
		t.Fatal("expected error reading non-existent directory, got nil")
	}
}

func TestDefaultCreateCommand(t *testing.T) {
	ctx := context.Background()
	cmd := defaultCreateCommand(ctx, []string{"custom"})
	if cmd == nil {
		t.Fatal("expected command object, got nil")
	}
	if cmd.Cancel == nil {
		t.Fatal("expected cancel function, got nil")
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("unexpected cancel error: %v", err)
	}

	runningCmd := exec.CommandContext(ctx, "sleep", "10")
	cancelFn := cancelCommand(runningCmd)
	if err := runningCmd.Start(); err != nil {
		t.Fatalf("cmd.Start() unexpected error: %v", err)
	}
	defer func() {
		if waitErr := runningCmd.Wait(); waitErr != nil {
			if _, ok := errors.AsType[*exec.ExitError](waitErr); !ok {
				t.Errorf("runningCmd.Wait() error: %v", waitErr)
			}
		}
	}()
	if err := cancelFn(); err != nil {
		t.Fatalf("cancelFn() unexpected error: %v", err)
	}
}

func TestDefaultExec(t *testing.T) {
	ctx := context.Background()
	origCreate := createCommand
	t.Cleanup(func() { createCommand = origCreate })

	createCommand = func(ctx context.Context, _ []string) *exec.Cmd {
		return exec.CommandContext(ctx, "/dev/null/non_existent_binary")
	}
	if err := defaultExec(ctx, nil, nil); err == nil {
		t.Fatal("expected error executing non-existent binary, got nil")
	}

	createCommand = func(ctx context.Context, _ []string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}
	if err := defaultExec(ctx, nil, nil); err != nil {
		t.Fatalf("expected successful execution, got error: %v", err)
	}
}

func TestReadOptionalSecret(t *testing.T) {
	tempDir := t.TempDir()
	secretPath := filepath.Join(tempDir, "token")

	val, exists, err := readOptionalSecret(secretPath)
	if err != nil || exists || val != "" {
		t.Fatalf("expected empty non-existing secret, got val=%q exists=%v err=%v", val, exists, err)
	}

	if writeErr := os.WriteFile(secretPath, []byte("  secret_pass\r\n"), 0o600); writeErr != nil {
		t.Fatalf("writing secret file: %v", writeErr)
	}

	val, exists, err = readOptionalSecret(secretPath)
	if err != nil || !exists || val != "secret_pass" {
		t.Fatalf("expected 'secret_pass', got val=%q exists=%v err=%v", val, exists, err)
	}

	origReadFile := readFile
	readFile = mockFailingReadFile
	t.Cleanup(func() { readFile = origReadFile })

	if _, _, err := readOptionalSecret(secretPath); err == nil {
		t.Fatal("expected mock read error, got nil")
	}
}

func TestReadRequiredSecret(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name        string
		secretName  string
		fileContent string
		createFile  bool
		wantErr     bool
	}{
		{
			name:        "missing_file",
			secretName:  "missing",
			createFile:  false,
			fileContent: "",
			wantErr:     true,
		},
		{
			name:        "empty_content",
			secretName:  "empty",
			createFile:  true,
			fileContent: "   \r\n",
			wantErr:     true,
		},
		{
			name:        "valid_content",
			secretName:  "valid",
			createFile:  true,
			fileContent: "my_db\n",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.createFile {
				p := filepath.Join(tempDir, tt.secretName)
				if err := os.WriteFile(p, []byte(tt.fileContent), 0o600); err != nil {
					t.Fatalf("writing secret: %v", err)
				}
			}

			val, err := readRequiredSecret(tempDir, tt.secretName)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got err=%v", tt.wantErr, err)
			}
			if !tt.wantErr && val != "my_db" {
				t.Fatalf("expected 'my_db', got %q", val)
			}
		})
	}

	t.Run("read_error", func(t *testing.T) {
		origReadFile := readFile
		readFile = mockConditionalReadFile
		t.Cleanup(func() {
			readFile = origReadFile
			mockTargetSecretPath = ""
		})
		mockTargetSecretPath = filepath.Join(tempDir, "read_error_file")
		if _, err := readRequiredSecret(tempDir, "read_error_file"); err == nil {
			t.Fatal("expected error reading required secret, got nil")
		}
	})
}

func TestSetEnvVar(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		value    string
		expected string
		baseEnv  []string
	}{
		{
			name:     "append_new",
			key:      "BETA",
			value:    "second",
			expected: "BETA=second",
			baseEnv:  []string{"ALPHA=first"},
		},
		{
			name:     "replace_existing",
			key:      "BETA",
			value:    "new",
			expected: "BETA=new",
			baseEnv:  []string{"GAMMA=third", "BETA=old"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := setEnvVar(tt.baseEnv, tt.key, tt.value)
			if !slices.Contains(res, tt.expected) {
				t.Fatalf("expected env item %q in %v", tt.expected, res)
			}
		})
	}
}

func TestSetEnvVarIfMissing(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		defVal   string
		expected string
		baseEnv  []string
	}{
		{
			name:     "add_when_missing",
			key:      "EPSILON",
			defVal:   "default_val",
			expected: "EPSILON=default_val",
			baseEnv:  []string{"DELTA=fourth"},
		},
		{
			name:     "preserve_when_present",
			key:      "EPSILON",
			defVal:   "default_val",
			expected: "EPSILON=custom",
			baseEnv:  []string{"ZETA=fifth", "EPSILON=custom"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := setEnvVarIfMissing(tt.baseEnv, tt.key, tt.defVal)
			if !slices.Contains(res, tt.expected) {
				t.Fatalf("expected env item %q in %v", tt.expected, res)
			}
		})
	}
}

func writeTestSecret(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writing secret %s: %v", name, err)
	}
	return p
}

func TestLookupEnvVar(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		expected string
		env      []string
	}{
		{
			name:     "present",
			key:      "OMEGA",
			expected: "last",
			env:      []string{"SIGMA=first", "OMEGA=last"},
		},
		{
			name:     "missing",
			key:      "KAPPA",
			expected: "",
			env:      []string{"SIGMA=first"},
		},
		{
			name:     "prefix_collision",
			key:      "THETA",
			expected: "",
			env:      []string{"THETA_EXTRA=value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lookupEnvVar(tt.env, tt.key); got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestListSecretNames(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.Mkdir(filepath.Join(tempDir, "nested_dir"), 0o700); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}
	writeTestSecret(t, tempDir, "sftpgo_smtp__user", "noreply@example.com")
	writeTestSecret(t, tempDir, "db_user", "test_user")
	writeTestSecret(t, tempDir, "ssh_host_rsa_key", "rsa_content")

	names, err := listSecretNames(tempDir)
	if err != nil {
		t.Fatalf("unexpected error listing secrets: %v", err)
	}
	expected := []string{"db_user", "sftpgo_smtp__user", "ssh_host_rsa_key"}
	if !slices.Equal(names, expected) {
		t.Fatalf("expected %v, got %v", expected, names)
	}

	names, err = listSecretNames(filepath.Join(tempDir, "non_existent"))
	if err != nil || names != nil {
		t.Fatalf("expected nil without error on non-existent dir, got names=%v err=%v", names, err)
	}

	origReadDir := readDir
	readDir = mockFailingReadDir
	t.Cleanup(func() { readDir = origReadDir })

	if _, err := listSecretNames(tempDir); err == nil {
		t.Fatal("expected mock readdir error, got nil")
	}
}

func TestHasSFTPGoSecretPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "lowercase", input: "sftpgo_smtp__host", expected: true},
		{name: "uppercase", input: "SFTPGO_SMTP__PORT", expected: true},
		{name: "mixed_case", input: "Sftpgo_smtp__from", expected: true},
		{name: "prefix_only", input: "sftpgo_", expected: true},
		{name: "missing_separator", input: "sftpgo", expected: false},
		{name: "database_secret", input: "db_password", expected: false},
		{name: "ssh_host_key", input: "ssh_host_ecdsa_key", expected: false},
		{name: "empty", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasSFTPGoSecretPrefix(tt.input); got != tt.expected {
				t.Fatalf("expected %v for %q, got %v", tt.expected, tt.input, got)
			}
		})
	}
}

func TestSecretEnvKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{name: "nested_lowercase", input: "sftpgo_smtp__password", expected: "SFTPGO_SMTP__PASSWORD"},
		{name: "already_uppercase", input: "SFTPGO_SMTP__AUTH_TYPE", expected: "SFTPGO_SMTP__AUTH_TYPE"},
		{name: "indexed_binding", input: "sftpgo_httpd__bindings__0__oidc__client_id", expected: "SFTPGO_HTTPD__BINDINGS__0__OIDC__CLIENT_ID"},
		{name: "prefix_only", input: "sftpgo_", wantErr: true},
		{name: "hyphen", input: "sftpgo_smtp-encryption", wantErr: true},
		{name: "dot", input: "sftpgo_smtp.domain", wantErr: true},
		{name: "equals_sign", input: "sftpgo_smtp__debug=1", wantErr: true},
		{name: "whitespace", input: "sftpgo_smtp templates", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := secretEnvKey(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got err=%v", tt.wantErr, err)
			}
			if got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestConfigureSFTPGoSecrets(t *testing.T) {
	tempDir := t.TempDir()

	writeTestSecret(t, tempDir, "sftpgo_smtp__password", "  smtp_pass\r\n")
	writeTestSecret(t, tempDir, "sftpgo_smtp__user", "noreply@example.com")
	writeTestSecret(t, tempDir, "SFTPGO_SMTP__HOST", "mail.example.net")
	writeTestSecret(t, tempDir, "sftpgo_kms__secrets__master_key", "kms_secret")
	writeTestSecret(t, tempDir, "sftpgo_httpd__bindings__0__oidc__client_secret", "oidc_secret")
	writeTestSecret(t, tempDir, "sftpgo_smtp__domain", "  \n")
	writeTestSecret(t, tempDir, "db_password", "db_pass")

	names, err := listSecretNames(tempDir)
	if err != nil {
		t.Fatalf("listing secrets: %v", err)
	}
	names = append(names, "sftpgo_listed_but_missing")

	baseEnv := []string{"SFTPGO_SMTP__USER=stale@example.org"}
	env, err := configureSFTPGoSecrets(baseEnv, tempDir, names)
	if err != nil {
		t.Fatalf("unexpected error configuring secrets: %v", err)
	}

	expectedItems := []string{
		"SFTPGO_SMTP__PASSWORD=smtp_pass",
		"SFTPGO_SMTP__USER=noreply@example.com",
		"SFTPGO_SMTP__HOST=mail.example.net",
		"SFTPGO_KMS__SECRETS__MASTER_KEY=kms_secret",
		"SFTPGO_HTTPD__BINDINGS__0__OIDC__CLIENT_SECRET=oidc_secret",
	}
	for _, item := range expectedItems {
		if !slices.Contains(env, item) {
			t.Fatalf("missing expected env item %q in %v", item, env)
		}
	}
	if len(env) != len(expectedItems) {
		t.Fatalf("expected exactly %d env items, got %d: %v", len(expectedItems), len(env), env)
	}

	if _, err := configureSFTPGoSecrets(nil, tempDir, []string{"sftpgo_smtp-port"}); err == nil {
		t.Fatal("expected error for unmappable secret name, got nil")
	}

	origReadFile := readFile
	readFile = mockFailingReadFile
	t.Cleanup(func() { readFile = origReadFile })

	if _, err := configureSFTPGoSecrets(nil, tempDir, names); err == nil {
		t.Fatal("expected mock readfile error, got nil")
	}
}

func TestConfigureDefaultAdmin(t *testing.T) {
	tests := []struct {
		env  map[string]string
		name string
		want bool
	}{
		{
			name: "complete_credentials",
			env:  map[string]string{envAdminUser: "admin_alpha", envAdminAuth: "pass_alpha"},
			want: true,
		},
		{
			name: "username_only",
			env:  map[string]string{envAdminUser: "admin_beta"},
			want: false,
		},
		{
			name: "password_only",
			env:  map[string]string{envAdminAuth: "pass_gamma"},
			want: false,
		},
		{
			name: "empty_username",
			env:  map[string]string{envAdminUser: "", envAdminAuth: "pass_delta"},
			want: false,
		},
		{
			name: "explicit_value_preserved",
			env:  map[string]string{envDataDefaultAdmin: "true"},
			want: true,
		},
		{
			name: "no_credentials",
			env:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := make([]string, 0, len(tt.env))
			for k, v := range tt.env {
				env = append(env, k+"="+v)
			}
			got := lookupEnvVar(configureDefaultAdmin(env), envDataDefaultAdmin)
			if got != strconv.FormatBool(tt.want) {
				t.Fatalf("expected %s=%t, got %q", envDataDefaultAdmin, tt.want, got)
			}
		})
	}
}

func TestDiscoverSSHHostKeys(t *testing.T) {
	tempDir := t.TempDir()

	rsaKey := writeTestSecret(t, tempDir, "ssh_host_rsa_key", "rsa_content")
	ed25519Key := writeTestSecret(t, tempDir, "ssh_host_ed25519_key", "ed25519_content")
	writeTestSecret(t, tempDir, "ssh_host_empty_key", "  \n")
	writeTestSecret(t, tempDir, "other_secret.txt", "other")

	names := []string{
		"other_secret.txt",
		"ssh_host_ed25519_key",
		"ssh_host_empty_key",
		"ssh_host_missing_key",
		"ssh_host_rsa_key",
	}

	keys, err := discoverSSHHostKeys(tempDir, names)
	if err != nil {
		t.Fatalf("unexpected error discovering keys: %v", err)
	}
	if !slices.Equal(keys, []string{ed25519Key, rsaKey}) {
		t.Fatalf("expected keys [%s, %s], got %v", ed25519Key, rsaKey, keys)
	}

	keys, err = discoverSSHHostKeys(tempDir, nil)
	if err != nil || len(keys) != 0 {
		t.Fatalf("expected empty result without names, got keys=%v err=%v", keys, err)
	}

	origReadFile := readFile
	readFile = mockFailingReadFile
	t.Cleanup(func() { readFile = origReadFile })

	if _, err := discoverSSHHostKeys(tempDir, names); err == nil {
		t.Fatal("expected mock readfile error, got nil")
	}
}

func TestPrepareEnvironment(t *testing.T) {
	tempDir := t.TempDir()

	writeSecret := func(name, content string) {
		t.Helper()
		p := filepath.Join(tempDir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing secret %s: %v", name, err)
		}
	}

	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error with missing db_name, got nil")
	}

	writeSecret("db_name", "test_db")
	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error with missing db_user, got nil")
	}

	writeSecret("db_user", "test_user")
	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error with missing db_password, got nil")
	}

	writeSecret("db_password", "test_pass")
	writeSecret("sftpgo_default_admin_username", "admin_account")
	writeSecret("sftpgo_default_admin_password", "admin_pass")
	writeSecret("sftpgo_kms__secrets__master_key", "kms_secret")
	writeSecret("sftpgo_smtp__password", "smtp_pass")
	writeSecret("ssh_host_ed25519_key", "ed25519_key_data")

	baseEnv := []string{
		"HOME=/custom/home",
		"EXISTING=value",
	}

	env, err := PrepareEnvironment(baseEnv, tempDir)
	if err != nil {
		t.Fatalf("unexpected error preparing environment: %v", err)
	}

	expectedItems := []string{
		"EXISTING=value",
		"HOME=/custom/home",
		"SFTPGO_LOG_FILE_PATH=/var/log/sftpgo/sftpgo.log",
		"SFTPGO_TELEMETRY__BIND_ADDRESS=127.0.0.1",
		"SFTPGO_TELEMETRY__BIND_PORT=10000",
		"SFTPGO_DATA_PROVIDER__DRIVER=postgresql",
		"SFTPGO_DATA_PROVIDER__HOST=/var/run/postgresql",
		"SFTPGO_DATA_PROVIDER__PORT=5432",
		"SFTPGO_DATA_PROVIDER__SSL_MODE=0",
		"SFTPGO_DATA_PROVIDER__NAME=test_db",
		"SFTPGO_DATA_PROVIDER__USERNAME=test_user",
		"SFTPGO_DATA_PROVIDER__PASSWORD=test_pass",
		"SFTPGO_DATA_PROVIDER__USERS_BASE_DIR=/data",
		"SFTPGO_DATA_PROVIDER__BACKUPS_PATH=/tmp",
		"SFTPGO_DATA_PROVIDER__CREATE_DEFAULT_ADMIN=true",
		"SFTPGO_HTTPD__TEMPLATES_PATH=/usr/share/sftpgo/templates",
		"SFTPGO_HTTPD__STATIC_FILES_PATH=/usr/share/sftpgo/static",
		"SFTPGO_HTTPD__OPENAPI_PATH=/usr/share/sftpgo/openapi",
		"SFTPGO_DEFAULT_ADMIN_USERNAME=admin_account",
		"SFTPGO_DEFAULT_ADMIN_PASSWORD=admin_pass",
		"SFTPGO_KMS__SECRETS__MASTER_KEY=kms_secret",
		"SFTPGO_SMTP__PASSWORD=smtp_pass",
		"SFTPGO_SFTPD__HOST_KEYS__0=" + filepath.Join(tempDir, "ssh_host_ed25519_key"),
	}

	for _, item := range expectedItems {
		if !slices.Contains(env, item) {
			t.Fatalf("missing expected env item %q in %v", item, env)
		}
	}

	overrideEnv := []string{
		"SFTPGO_HTTPD__BINDINGS__0__PORT=9000",
		"SFTPGO_WEBDAVD__BINDINGS__0__PORT=9001",
	}
	customEnv, err := PrepareEnvironment(overrideEnv, tempDir)
	if err != nil {
		t.Fatalf("unexpected error preparing custom environment: %v", err)
	}
	for _, expected := range overrideEnv {
		if !slices.Contains(customEnv, expected) {
			t.Fatalf("expected custom env %q to be preserved in %v", expected, customEnv)
		}
	}

	origReadFile := readFile
	readFile = mockConditionalReadFile
	t.Cleanup(func() {
		readFile = origReadFile
		mockTargetSecretPath = ""
	})

	for _, fname := range []string{"sftpgo_default_admin_username", "ssh_host_ed25519_key"} {
		mockTargetSecretPath = filepath.Join(tempDir, fname)
		if _, err := PrepareEnvironment(nil, tempDir); err == nil {
			t.Fatalf("expected error reading %s, got nil", fname)
		}
	}

	readFile = origReadFile
	origReadDir := readDir
	readDir = mockFailingReadDir
	t.Cleanup(func() { readDir = origReadDir })

	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error listing secrets directory, got nil")
	}
}

func TestPrepareEnvironment_EnforcedSettingsOverrideSecrets(t *testing.T) {
	tempDir := t.TempDir()

	writeTestSecret(t, tempDir, "db_name", "test_db")
	writeTestSecret(t, tempDir, "db_user", "test_user")
	writeTestSecret(t, tempDir, "db_password", "trusted_db_pass")
	writeTestSecret(t, tempDir, "sftpgo_data_provider__password", "hostile_override")
	writeTestSecret(t, tempDir, "sftpgo_data_provider__users_base_dir", "/hostile")
	writeTestSecret(t, tempDir, "sftpgo_log_file_path", "/hostile.log")

	env, err := PrepareEnvironment(nil, tempDir)
	if err != nil {
		t.Fatalf("unexpected error preparing environment: %v", err)
	}

	tests := []struct {
		key  string
		want string
	}{
		{key: envDataProviderAuth, want: "trusted_db_pass"},
		{key: envDataUsersBaseDir, want: defaultUsersBaseDir},
		{key: envLogFilePath, want: defaultLogPath},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := lookupEnvVar(env, tt.key); got != tt.want {
				t.Fatalf("expected %s=%q, got %q", tt.key, tt.want, got)
			}
		})
	}
}

func TestPrepareEnvironment_InvalidSecretName(t *testing.T) {
	tempDir := t.TempDir()

	writeTestSecret(t, tempDir, "db_name", "test_db")
	writeTestSecret(t, tempDir, "db_user", "test_user")
	writeTestSecret(t, tempDir, "db_password", "test_pass")
	writeTestSecret(t, tempDir, "sftpgo_smtp-from", "noreply@example.com")

	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error for unmappable secret name, got nil")
	}
}

func TestBuildCommandArguments(t *testing.T) {
	tests := []struct {
		name       string
		defaultCmd string
		args       []string
		expected   []string
	}{
		{
			name:       "empty_args",
			defaultCmd: defaultCommand,
			args:       nil,
			expected:   []string{defaultBinaryName, defaultCommand},
		},
		{
			name:       "binary_path_argument",
			defaultCmd: defaultCommand,
			args:       []string{defaultBinaryPath, defaultCommand},
			expected:   []string{defaultBinaryName, defaultCommand},
		},
		{
			name:       "binary_name_argument",
			defaultCmd: defaultCommand,
			args:       []string{defaultBinaryName, defaultCommand},
			expected:   []string{defaultBinaryName, defaultCommand},
		},
		{
			name:       "custom_subcommand",
			defaultCmd: defaultCommand,
			args:       []string{"gen", "hash"},
			expected:   []string{defaultBinaryName, "gen", "hash"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := BuildCommandArguments(tt.defaultCmd, tt.args)
			if !slices.Equal(res, tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, res)
			}
		})
	}
}

func TestRun(t *testing.T) {
	tempDir := t.TempDir()

	writeSecret := func(name, content string) {
		t.Helper()
		p := filepath.Join(tempDir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing secret %s: %v", name, err)
		}
	}

	writeSecret("db_name", "test_db")
	writeSecret("db_user", "test_user")
	writeSecret("db_password", "test_pass")

	origExec := execProcess
	execProcess = mockSuccessfulExec
	t.Cleanup(func() { execProcess = origExec })

	ctx := context.Background()
	if err := Run(ctx, []string{defaultCommand}, tempDir); err != nil {
		t.Fatalf("unexpected error running init: %v", err)
	}

	if !slices.Equal(recordedExecArgs, []string{defaultBinaryName, defaultCommand}) {
		t.Fatalf("expected args %v, got %v", []string{defaultBinaryName, defaultCommand}, recordedExecArgs)
	}

	execProcess = mockFailingExec
	if err := Run(ctx, nil, tempDir); err == nil {
		t.Fatal("expected execution failure, got nil")
	}

	if err := Run(ctx, nil, filepath.Join(tempDir, "invalid_dir")); err == nil {
		t.Fatal("expected prepare environment failure, got nil")
	}
}

func TestMain_Execution(t *testing.T) {
	tempDir := t.TempDir()

	writeSecret := func(name, content string) {
		t.Helper()
		p := filepath.Join(tempDir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing secret %s: %v", name, err)
		}
	}

	writeSecret("db_name", "test_db")
	writeSecret("db_user", "test_user")
	writeSecret("db_password", "test_pass")

	origExit := exitFunc
	origExec := execProcess
	origArgs := os.Args
	exitFunc = mockExit
	execProcess = mockSuccessfulExec
	t.Cleanup(func() {
		exitFunc = origExit
		execProcess = origExec
		os.Args = origArgs
	})

	recordedExitCode = -1
	os.Args = []string{"init", defaultCommand}

	origReadFile := readFile
	origReadDir := readDir
	redirectedSecretsDir = tempDir
	readFile = mockRedirectedReadFile
	readDir = mockRedirectedReadDir
	t.Cleanup(func() {
		readFile = origReadFile
		readDir = origReadDir
		redirectedSecretsDir = ""
	})

	main()

	if recordedExitCode != -1 {
		t.Fatalf("expected exit not called on success, got %d", recordedExitCode)
	}

	execProcess = mockFailingExec
	recordedExitCode = -1

	oldStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("creating pipe: %v", pipeErr)
	}
	os.Stderr = w

	main()

	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("closing pipe writer: %v", closeErr)
	}
	os.Stderr = oldStderr
	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("copying from pipe: %v", copyErr)
	}

	if recordedExitCode != 1 {
		t.Fatalf("expected exit code 1 on failure, got %d", recordedExitCode)
	}
	if !bytes.Contains(buf.Bytes(), []byte("init error:")) {
		t.Fatalf("expected stderr output to contain 'init error:', got %q", buf.String())
	}
}
