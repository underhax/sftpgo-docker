// Package main initializes SFTPGo by injecting Docker secrets into the daemon environment.
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
	fileDBName = "db_name"
	fileDBUser = "db_user"
	fileDBAuth = "db_password"
)

const (
	envDataProviderDriver = "SFTPGO_DATA_PROVIDER__DRIVER"
	envDataProviderName   = "SFTPGO_DATA_PROVIDER__NAME"
	envDataProviderHost   = "SFTPGO_DATA_PROVIDER__HOST"
	envDataProviderPort   = "SFTPGO_DATA_PROVIDER__PORT"
	envDataProviderUser   = "SFTPGO_DATA_PROVIDER__USERNAME"
	envDataProviderAuth   = "SFTPGO_DATA_PROVIDER__PASSWORD"
	envDataProviderSSL    = "SFTPGO_DATA_PROVIDER__SSL_MODE"
	envDataUsersBaseDir   = "SFTPGO_DATA_PROVIDER__USERS_BASE_DIR"
	envDataBackupsPath    = "SFTPGO_DATA_PROVIDER__BACKUPS_PATH"
	envDataDefaultAdmin   = "SFTPGO_DATA_PROVIDER__CREATE_DEFAULT_ADMIN"
	envHTTPTemplatesPath  = "SFTPGO_HTTPD__TEMPLATES_PATH"
	envHTTPStaticPath     = "SFTPGO_HTTPD__STATIC_FILES_PATH"
	envHTTPOpenAPIPath    = "SFTPGO_HTTPD__OPENAPI_PATH"
	envAdminUser          = "SFTPGO_DEFAULT_ADMIN_USERNAME"
	envAdminAuth          = "SFTPGO_DEFAULT_ADMIN_PASSWORD"
	envHome               = "HOME"
	envLogFilePath        = "SFTPGO_LOG_FILE_PATH"
	envTelemetryAddress   = "SFTPGO_TELEMETRY__BIND_ADDRESS"
	envTelemetryPort      = "SFTPGO_TELEMETRY__BIND_PORT"
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
	defaultUsersBaseDir    = "/data"
	defaultBackupsPath     = "/tmp"
	defaultCreateAdmin     = "false"
	defaultTemplatesPath   = "/usr/share/sftpgo/templates"
	defaultStaticFilesPath = "/usr/share/sftpgo/static"
	defaultOpenAPIPath     = "/usr/share/sftpgo/openapi"
)

const (
	sftpgoSecretPrefix = "sftpgo_"
	sshHostPrefix      = "ssh_host_"
	sshHostSuffix      = "_key"
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

func lookupEnvVar(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if value, found := strings.CutPrefix(kv, prefix); found {
			return value
		}
	}
	return ""
}

func listSecretNames(secretsDir string) ([]string, error) {
	entries, err := readDir(secretsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning secrets directory: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)
	return names, nil
}

func hasSFTPGoSecretPrefix(name string) bool {
	return len(name) >= len(sftpgoSecretPrefix) && strings.EqualFold(name[:len(sftpgoSecretPrefix)], sftpgoSecretPrefix)
}

func isInvalidEnvKeyRune(r rune) bool {
	return (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_'
}

func secretEnvKey(name string) (string, error) {
	key := strings.ToUpper(name)
	if len(key) == len(sftpgoSecretPrefix) || strings.ContainsFunc(key, isInvalidEnvKeyRune) {
		return "", fmt.Errorf("secret %q cannot be mapped to an environment variable", name)
	}
	return key, nil
}

func configureSFTPGoSecrets(env []string, secretsDir string, names []string) ([]string, error) {
	for _, name := range names {
		if !hasSFTPGoSecretPrefix(name) {
			continue
		}

		key, err := secretEnvKey(name)
		if err != nil {
			return nil, err
		}

		val, exists, err := readOptionalSecret(filepath.Join(secretsDir, name))
		if err != nil {
			return nil, err
		}
		if exists && val != "" {
			env = setEnvVar(env, key, val)
		}
	}
	return env, nil
}

func discoverSSHHostKeys(secretsDir string, names []string) ([]string, error) {
	validPaths := make([]string, 0, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, sshHostPrefix) || !strings.HasSuffix(name, sshHostSuffix) {
			continue
		}
		keyPath := filepath.Join(secretsDir, name)
		val, exists, err := readOptionalSecret(keyPath)
		if err != nil {
			return nil, err
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

func configureDefaultAdmin(env []string) []string {
	if lookupEnvVar(env, envAdminUser) != "" && lookupEnvVar(env, envAdminAuth) != "" {
		return setEnvVar(env, envDataDefaultAdmin, "true")
	}
	return setEnvVarIfMissing(env, envDataDefaultAdmin, defaultCreateAdmin)
}

func configureSSHHostKeys(env []string, secretsDir string, names []string) ([]string, error) {
	hostKeyPaths, err := discoverSSHHostKeys(secretsDir, names)
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
	env = setEnvVar(env, envDataUsersBaseDir, defaultUsersBaseDir)
	env = setEnvVar(env, envDataBackupsPath, defaultBackupsPath)
	env = setEnvVar(env, envHTTPTemplatesPath, defaultTemplatesPath)
	env = setEnvVar(env, envHTTPStaticPath, defaultStaticFilesPath)
	env = setEnvVar(env, envHTTPOpenAPIPath, defaultOpenAPIPath)
	env = setEnvVarIfMissing(env, envHome, defaultHomeDir)
	return env
}

// PrepareEnvironment merges secrets and base settings into the daemon process environment.
func PrepareEnvironment(baseEnv []string, secretsDir string) ([]string, error) {
	env := slices.Clone(baseEnv)

	names, err := listSecretNames(secretsDir)
	if err != nil {
		return nil, err
	}

	if env, err = configureSFTPGoSecrets(env, secretsDir, names); err != nil {
		return nil, err
	}
	if env, err = configureDatabaseSecrets(env, secretsDir); err != nil {
		return nil, err
	}
	if env, err = configureSSHHostKeys(env, secretsDir, names); err != nil {
		return nil, err
	}

	env = configureDefaultAdmin(env)
	env = configureDefaultSettings(env)
	return env, nil
}

// BuildCommandArguments constructs command arguments with defaults for the daemon.
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

// Run prepares the environment and starts the daemon.
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
