// Package main implements a minimal static initialization wrapper that injects
// Docker secrets into environment variables for PostgreSQL authentication and
// daemon configuration before transferring execution to SFTPGo as PID 1.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	defaultBinaryPath = "/usr/local/bin/sftpgo"
	defaultBinaryName = "sftpgo"
	defaultCommand    = "serve"
	defaultSecretsDir = "/run/secrets"
)

const (
	fileDBName       = "db_name"
	fileDBUser       = "db_user"
	fileDBAuth       = "db_password"
	fileAdminAccount = "sftpgo_admin_user"
	fileKMSMaster    = "sftpgo_kms_master_key"
)

const (
	envDataProviderDriver      = "SFTPGO_DATA_PROVIDER__DRIVER"
	envDataProviderName        = "SFTPGO_DATA_PROVIDER__NAME"
	envDataProviderHost        = "SFTPGO_DATA_PROVIDER__HOST"
	envDataProviderPort        = "SFTPGO_DATA_PROVIDER__PORT"
	envDataProviderUser        = "SFTPGO_DATA_PROVIDER__USERNAME"
	envDataProviderAuth        = "SFTPGO_DATA_PROVIDER__PASSWORD"
	envDataProviderSSL         = "SFTPGO_DATA_PROVIDER__SSL_MODE"
	envDataUsersBaseDir        = "SFTPGO_DATA_PROVIDER__USERS_BASE_DIR"
	envDataBackupsPath         = "SFTPGO_DATA_PROVIDER__BACKUPS_PATH"
	envDataDefaultAdmin        = "SFTPGO_DATA_PROVIDER__CREATE_DEFAULT_ADMIN"
	envHTTPPort                = "SFTPGO_HTTPD__BINDINGS__0__PORT"
	envWebDAVPort              = "SFTPGO_WEBDAVD__BINDINGS__0__PORT"
	envHTTPTemplatesPath       = "SFTPGO_HTTPD__TEMPLATES_PATH"
	envHTTPStaticPath          = "SFTPGO_HTTPD__STATIC_FILES_PATH"
	envHTTPOpenAPIPath         = "SFTPGO_HTTPD__OPENAPI_PATH"
	envCommonIdleTimeout       = "SFTPGO_COMMON__IDLE_TIMEOUT"
	envCommonUploadMode        = "SFTPGO_COMMON__UPLOAD_MODE"
	envAdminUser               = "SFTPGO_DEFAULT_ADMIN_USERNAME"
	envAdminAuth               = "SFTPGO_DEFAULT_ADMIN_PASSWORD"
	envKMSKey                  = "SFTPGO_KMS__SECRETS__MASTER_KEY"
	envHome                    = "HOME"
	envLogFilePath             = "SFTPGO_LOG_FILE_PATH"
	envTelemetryAddress        = "SFTPGO_TELEMETRY__BIND_ADDRESS"
	envTelemetryPort           = "SFTPGO_TELEMETRY__BIND_PORT"
	envHookOIDCUI              = "SFTPGO_HOOK__ENABLE_OIDC_UI"
	envOIDCBinding0ConfigURL   = "SFTPGO_HTTPD__BINDINGS__0__OIDC__CONFIG_URL"
	envOIDCBinding0ClientID    = "SFTPGO_HTTPD__BINDINGS__0__OIDC__CLIENT_ID"
	envOIDCBinding0ClientAuth  = "SFTPGO_HTTPD__BINDINGS__0__OIDC__CLIENT_SECRET"
	envOIDCBinding0RedirectURL = "SFTPGO_HTTPD__BINDINGS__0__OIDC__REDIRECT_BASE_URL"
)

const (
	defaultPostgresDriver  = "postgresql"
	defaultPostgresHost    = "/var/run/postgresql"
	defaultPostgresPort    = "5432"
	defaultPostgresSSLMode = "0"
	defaultHomeDir         = "/tmp"
	defaultLogPath         = "/var/log/sftpgo/sftpgo.log"
	defaultTelemetryAddr   = "127.0.0.1"
	defaultTelemetryPort   = "10000"
	defaultHTTPPort        = "8080"
	defaultWebDAVPort      = "8090"
	defaultUsersBaseDir    = "/data"
	defaultBackupsPath     = "/tmp"
	defaultCreateAdmin     = "false"
	defaultTemplatesPath   = "/usr/share/sftpgo/templates"
	defaultStaticFilesPath = "/usr/share/sftpgo/static"
	defaultOpenAPIPath     = "/usr/share/sftpgo/openapi"
	defaultIdleTimeout     = "15"
	defaultUploadMode      = "0"
	defaultHookOIDCUI      = "1"
)

const (
	sshHostPrefix = "ssh_host_"
	sshHostSuffix = "_key"
)

func defaultReadFile(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading secret file %q: %w", cleanPath, err)
	}
	return data, nil
}

func defaultReadDir(dirname string) ([]fs.DirEntry, error) {
	cleanDir := filepath.Clean(dirname)
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("reading secrets directory %q: %w", cleanDir, err)
	}
	return entries, nil
}

func cancelCommand(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
}

func defaultCreateCommand(ctx context.Context, _ []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, defaultBinaryPath, defaultCommand)
	cmd.Cancel = cancelCommand(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func defaultExec(ctx context.Context, args, env []string) error {
	cmd := createCommand(ctx, args)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %q: %w", defaultBinaryPath, err)
	}
	return nil
}

var (
	createCommand = defaultCreateCommand
	execProcess   = defaultExec
	exitFunc      = os.Exit
	readDir       = defaultReadDir
	readFile      = defaultReadFile
)

func readOptionalSecret(path string) (secret string, exists bool, err error) {
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading secret %q: %w", path, err)
	}

	trimmed := strings.TrimSpace(string(data))
	return trimmed, true, nil
}

func readRequiredSecret(secretsDir, secretName string) (string, error) {
	path := filepath.Join(secretsDir, secretName)
	val, exists, err := readOptionalSecret(path)
	if err != nil {
		return "", err
	}
	if !exists || val == "" {
		return "", fmt.Errorf("required secret %q is missing or empty", secretName)
	}
	return val, nil
}

func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func setEnvVarIfMissing(env []string, key, defaultValue string) []string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return env
		}
	}
	return append(env, prefix+defaultValue)
}

func discoverSSHHostKeys(secretsDir string) ([]string, error) {
	entries, err := readDir(secretsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning secrets directory for ssh host keys: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, sshHostPrefix) && strings.HasSuffix(name, sshHostSuffix) {
			names = append(names, name)
		}
	}

	slices.Sort(names)

	validPaths := make([]string, 0, len(names))
	for _, name := range names {
		keyPath := filepath.Join(secretsDir, name)
		val, exists, readErr := readOptionalSecret(keyPath)
		if readErr != nil {
			return nil, readErr
		}
		if exists && val != "" {
			validPaths = append(validPaths, keyPath)
		}
	}

	return validPaths, nil
}

func configureDatabaseSecrets(env []string, secretsDir string) ([]string, error) {
	dbName, err := readRequiredSecret(secretsDir, fileDBName)
	if err != nil {
		return nil, err
	}

	dbUser, err := readRequiredSecret(secretsDir, fileDBUser)
	if err != nil {
		return nil, err
	}

	dbPassword, err := readRequiredSecret(secretsDir, fileDBAuth)
	if err != nil {
		return nil, err
	}

	env = setEnvVar(env, envDataProviderDriver, defaultPostgresDriver)
	env = setEnvVar(env, envDataProviderHost, defaultPostgresHost)
	env = setEnvVar(env, envDataProviderPort, defaultPostgresPort)
	env = setEnvVar(env, envDataProviderSSL, defaultPostgresSSLMode)
	env = setEnvVar(env, envDataProviderName, dbName)
	env = setEnvVar(env, envDataProviderUser, dbUser)
	env = setEnvVar(env, envDataProviderAuth, dbPassword)
	return env, nil
}

func configureAdminSecrets(env []string, secretsDir string) ([]string, error) {
	adminUser, adminUserExists, err := readOptionalSecret(filepath.Join(secretsDir, fileAdminAccount))
	if err != nil {
		return nil, err
	}

	adminPass, adminPassExists, err := readOptionalSecret(filepath.Join(secretsDir, "sftpgo_admin_password"))
	if err != nil {
		return nil, err
	}

	hasUser := adminUserExists && adminUser != ""
	hasPass := adminPassExists && adminPass != ""

	if hasUser != hasPass {
		return nil, errors.New("incomplete admin configuration: both sftpgo_admin_user and sftpgo_admin_password are required to create default admin")
	}

	if hasUser && hasPass {
		env = setEnvVar(env, envAdminUser, adminUser)
		env = setEnvVar(env, envAdminAuth, adminPass)
		env = setEnvVar(env, envDataDefaultAdmin, "true")
	} else {
		env = setEnvVarIfMissing(env, envDataDefaultAdmin, defaultCreateAdmin)
	}

	kmsKey, kmsKeyExists, err := readOptionalSecret(filepath.Join(secretsDir, fileKMSMaster))
	if err != nil {
		return nil, err
	}
	if kmsKeyExists && kmsKey != "" {
		env = setEnvVar(env, envKMSKey, kmsKey)
	}

	return env, nil
}

type oidcSecrets struct {
	configURL    string
	clientID     string
	clientSecret string
	redirectURL  string
}

func readOIDCSecrets(secretsDir string) (*oidcSecrets, error) {
	configURL, _, err := readOptionalSecret(filepath.Join(secretsDir, "sftpgo_oidc_config_url"))
	if err != nil {
		return nil, err
	}
	clientID, _, err := readOptionalSecret(filepath.Join(secretsDir, "sftpgo_oidc_client_id"))
	if err != nil {
		return nil, err
	}
	clientSecret, _, err := readOptionalSecret(filepath.Join(secretsDir, "sftpgo_oidc_client_secret"))
	if err != nil {
		return nil, err
	}
	redirectURL, _, err := readOptionalSecret(filepath.Join(secretsDir, "sftpgo_oidc_redirect_url"))
	if err != nil {
		return nil, err
	}

	return &oidcSecrets{
		configURL:    configURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
	}, nil
}

func configureOIDCSecrets(env []string, secretsDir string) ([]string, error) {
	env = setEnvVar(env, envHookOIDCUI, defaultHookOIDCUI)

	s, err := readOIDCSecrets(secretsDir)
	if err != nil {
		return nil, err
	}

	coreCount := 0
	if s.configURL != "" {
		coreCount++
	}
	if s.clientID != "" {
		coreCount++
	}
	if s.clientSecret != "" {
		coreCount++
	}

	if coreCount > 0 && coreCount < 3 {
		return nil, errors.New("incomplete oidc configuration: config_url, client_id, and client_secret are all required")
	}
	if coreCount == 0 {
		return env, nil
	}

	env = setEnvVar(env, envOIDCBinding0ConfigURL, s.configURL)
	env = setEnvVar(env, envOIDCBinding0ClientID, s.clientID)
	env = setEnvVar(env, envOIDCBinding0ClientAuth, s.clientSecret)
	if s.redirectURL != "" {
		env = setEnvVar(env, envOIDCBinding0RedirectURL, s.redirectURL)
	}

	return env, nil
}

func configureSSHHostKeys(env []string, secretsDir string) ([]string, error) {
	hostKeyPaths, err := discoverSSHHostKeys(secretsDir)
	if err != nil {
		return nil, err
	}
	for i, keyPath := range hostKeyPaths {
		env = setEnvVar(env, fmt.Sprintf("SFTPGO_SFTPD__HOST_KEYS__%d", i), keyPath)
	}
	return env, nil
}

func configureDefaultSettings(env []string) []string {
	env = setEnvVar(env, envLogFilePath, defaultLogPath)
	env = setEnvVar(env, envTelemetryAddress, defaultTelemetryAddr)
	env = setEnvVar(env, envTelemetryPort, defaultTelemetryPort)
	env = setEnvVarIfMissing(env, envHTTPPort, defaultHTTPPort)
	env = setEnvVarIfMissing(env, envWebDAVPort, defaultWebDAVPort)
	env = setEnvVar(env, envDataUsersBaseDir, defaultUsersBaseDir)
	env = setEnvVar(env, envDataBackupsPath, defaultBackupsPath)
	env = setEnvVarIfMissing(env, envDataDefaultAdmin, defaultCreateAdmin)
	env = setEnvVar(env, envHTTPTemplatesPath, defaultTemplatesPath)
	env = setEnvVar(env, envHTTPStaticPath, defaultStaticFilesPath)
	env = setEnvVar(env, envHTTPOpenAPIPath, defaultOpenAPIPath)
	env = setEnvVarIfMissing(env, envCommonIdleTimeout, defaultIdleTimeout)
	env = setEnvVarIfMissing(env, envCommonUploadMode, defaultUploadMode)
	env = setEnvVarIfMissing(env, envHome, defaultHomeDir)
	return env
}

// PrepareEnvironment merges Docker secrets and default runtime settings into the
// process environment so that database credentials and host keys are securely
// mediated without hardcoding sensitive values into static configurations.
func PrepareEnvironment(baseEnv []string, secretsDir string) ([]string, error) {
	env := slices.Clone(baseEnv)

	var err error
	if env, err = configureDatabaseSecrets(env, secretsDir); err != nil {
		return nil, err
	}
	if env, err = configureAdminSecrets(env, secretsDir); err != nil {
		return nil, err
	}
	if env, err = configureOIDCSecrets(env, secretsDir); err != nil {
		return nil, err
	}
	if env, err = configureSSHHostKeys(env, secretsDir); err != nil {
		return nil, err
	}

	env = configureDefaultSettings(env)
	return env, nil
}

// BuildCommandArguments constructs the execution argument vector, ensuring that
// the SFTPGo daemon receives default service arguments when custom arguments are omitted.
func BuildCommandArguments(defaultCmd string, args []string) []string {
	if len(args) == 0 {
		return []string{defaultBinaryName, defaultCmd}
	}

	if args[0] == defaultBinaryPath || args[0] == defaultBinaryName {
		res := make([]string, len(args))
		copy(res, args)
		res[0] = defaultBinaryName
		return res
	}

	res := make([]string, 0, len(args)+1)
	res = append(res, defaultBinaryName)
	res = append(res, args...)
	return res
}

// Run prepares the runtime environment with database credentials, secrets,
// and default parameters before starting the SFTPGo daemon.
func Run(ctx context.Context, args []string, secretsDir string) error {
	env, err := PrepareEnvironment(os.Environ(), secretsDir)
	if err != nil {
		return fmt.Errorf("preparing runtime environment: %w", err)
	}

	cmdArgs := BuildCommandArguments(defaultCommand, args)
	if execErr := execProcess(ctx, cmdArgs, env); execErr != nil {
		return fmt.Errorf("executing %q: %w", defaultBinaryPath, execErr)
	}

	return nil
}

func main() {
	var args []string
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, args, defaultSecretsDir); err != nil {
		fmt.Fprintf(os.Stderr, "init error: %v\n", err)
		exitFunc(1)
	}
}
