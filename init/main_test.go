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

func TestDiscoverSSHHostKeys(t *testing.T) {
	tempDir := t.TempDir()

	subDir := filepath.Join(tempDir, "ssh_host_ignored_dir_key")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	rsaKey := filepath.Join(tempDir, "ssh_host_rsa_key")
	if err := os.WriteFile(rsaKey, []byte("rsa_content"), 0o600); err != nil {
		t.Fatalf("writing rsa key: %v", err)
	}

	ed25519Key := filepath.Join(tempDir, "ssh_host_ed25519_key")
	if err := os.WriteFile(ed25519Key, []byte("ed25519_content"), 0o600); err != nil {
		t.Fatalf("writing ed25519 key: %v", err)
	}

	emptyKey := filepath.Join(tempDir, "ssh_host_empty_key")
	if err := os.WriteFile(emptyKey, []byte("  \n"), 0o600); err != nil {
		t.Fatalf("writing empty key: %v", err)
	}

	unrelated := filepath.Join(tempDir, "other_secret.txt")
	if err := os.WriteFile(unrelated, []byte("other"), 0o600); err != nil {
		t.Fatalf("writing unrelated: %v", err)
	}

	keys, err := discoverSSHHostKeys(tempDir)
	if err != nil {
		t.Fatalf("unexpected error discovering keys: %v", err)
	}

	if len(keys) != 2 {
		t.Fatalf("expected 2 discovered keys, got %d", len(keys))
	}
	if keys[0] != ed25519Key || keys[1] != rsaKey {
		t.Fatalf("expected sorted keys [%s, %s], got %v", ed25519Key, rsaKey, keys)
	}

	keys, err = discoverSSHHostKeys(filepath.Join(tempDir, "non_existent"))
	if err != nil || len(keys) != 0 {
		t.Fatalf("expected empty slice without error on non-existent dir, got keys=%v err=%v", keys, err)
	}

	origReadDir := readDir
	readDir = mockFailingReadDir
	t.Cleanup(func() { readDir = origReadDir })

	if _, err := discoverSSHHostKeys(tempDir); err == nil {
		t.Fatal("expected mock readdir error, got nil")
	}

	readDir = origReadDir
	origReadFile := readFile
	readFile = mockFailingReadFile
	t.Cleanup(func() { readFile = origReadFile })

	if _, err := discoverSSHHostKeys(tempDir); err == nil {
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
	writeSecret("sftpgo_admin_user", "admin_account")
	writeSecret("sftpgo_admin_password", "admin_pass")
	writeSecret("sftpgo_kms_master_key", "kms_secret")
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
		"SFTPGO_HTTPD__BINDINGS__0__PORT=8080",
		"SFTPGO_WEBDAVD__BINDINGS__0__PORT=8090",
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
		"SFTPGO_COMMON__IDLE_TIMEOUT=15",
		"SFTPGO_COMMON__UPLOAD_MODE=0",
		"SFTPGO_DEFAULT_ADMIN_USERNAME=admin_account",
		"SFTPGO_DEFAULT_ADMIN_PASSWORD=admin_pass",
		"SFTPGO_KMS__SECRETS__MASTER_KEY=kms_secret",
		"SFTPGO_HOOK__ENABLE_OIDC_UI=1",
		"SFTPGO_SFTPD__HOST_KEYS__0=" + filepath.Join(tempDir, "ssh_host_ed25519_key"),
	}

	for _, item := range expectedItems {
		if !slices.Contains(env, item) {
			t.Fatalf("missing expected env item %q in %v", item, env)
		}
	}

	overrideEnv := []string{
		"SFTPGO_HTTPD__BINDINGS__0__PORT=0",
		"SFTPGO_WEBDAVD__BINDINGS__0__PORT=0",
		"SFTPGO_COMMON__IDLE_TIMEOUT=30",
		"SFTPGO_COMMON__UPLOAD_MODE=2",
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

	mockTargetSecretPath = filepath.Join(tempDir, "sftpgo_admin_user")
	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error reading sftpgo_admin_user, got nil")
	}

	mockTargetSecretPath = filepath.Join(tempDir, "sftpgo_admin_password")
	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error reading sftpgo_admin_password, got nil")
	}

	mockTargetSecretPath = filepath.Join(tempDir, "sftpgo_kms_master_key")
	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error reading sftpgo_kms_master_key, got nil")
	}

	readFile = origReadFile
	origReadDir := readDir
	readDir = mockFailingReadDir
	t.Cleanup(func() { readDir = origReadDir })

	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error discovering ssh keys, got nil")
	}
}

func TestPrepareEnvironment_OIDC_Complete(t *testing.T) {
	tempDir := t.TempDir()

	writeSecret := func(name, content string) {
		p := filepath.Join(tempDir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing secret %s: %v", name, err)
		}
	}

	writeSecret("db_name", "test_db")
	writeSecret("db_user", "test_user")
	writeSecret("db_password", "test_pass")
	writeSecret("sftpgo_oidc_config_url", "https://auth.example.com/application/o/sftpgo/")
	writeSecret("sftpgo_oidc_client_id", "sftpgo_client_app")
	writeSecret("sftpgo_oidc_client_secret", "sftpgo_client_secret_data")
	writeSecret("sftpgo_oidc_redirect_url", "https://sftp.example.com")

	env, err := PrepareEnvironment(nil, tempDir)
	if err != nil {
		t.Fatalf("unexpected error preparing oidc environment: %v", err)
	}

	expectedItems := []string{
		"SFTPGO_HTTPD__BINDINGS__0__OIDC__CONFIG_URL=https://auth.example.com/application/o/sftpgo/",
		"SFTPGO_HTTPD__BINDINGS__0__OIDC__CLIENT_ID=sftpgo_client_app",
		"SFTPGO_HTTPD__BINDINGS__0__OIDC__CLIENT_SECRET=sftpgo_client_secret_data",
		"SFTPGO_HTTPD__BINDINGS__0__OIDC__REDIRECT_BASE_URL=https://sftp.example.com",
	}

	for _, item := range expectedItems {
		if !slices.Contains(env, item) {
			t.Fatalf("missing expected oidc env item %q in %v", item, env)
		}
	}
}

func TestPrepareEnvironment_OIDC_Incomplete(t *testing.T) {
	tempDir := t.TempDir()

	writeSecret := func(name, content string) {
		p := filepath.Join(tempDir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing secret %s: %v", name, err)
		}
	}

	writeSecret("db_name", "test_db")
	writeSecret("db_user", "test_user")
	writeSecret("db_password", "test_pass")
	writeSecret("sftpgo_oidc_client_id", "sftpgo_client_app")

	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error with incomplete oidc configuration, got nil")
	}
}

func TestPrepareEnvironment_OIDC_ReadErrors(t *testing.T) {
	tempDir := t.TempDir()

	writeSecret := func(name, content string) {
		p := filepath.Join(tempDir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing secret %s: %v", name, err)
		}
	}

	writeSecret("db_name", "test_db")
	writeSecret("db_user", "test_user")
	writeSecret("db_password", "test_pass")

	origReadFile := readFile
	readFile = mockConditionalReadFile
	t.Cleanup(func() {
		readFile = origReadFile
		mockTargetSecretPath = ""
	})

	filesToTest := []string{
		"sftpgo_oidc_config_url",
		"sftpgo_oidc_client_id",
		"sftpgo_oidc_client_secret",
		"sftpgo_oidc_redirect_url",
	}

	for _, fname := range filesToTest {
		mockTargetSecretPath = filepath.Join(tempDir, fname)
		if _, err := PrepareEnvironment(nil, tempDir); err == nil {
			t.Fatalf("expected error reading %s, got nil", fname)
		}
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
