package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/distilnet/distil-proxy/internal/config"
	"github.com/distilnet/distil-proxy/internal/daemon"
	"github.com/distilnet/distil-proxy/internal/observability"
	"github.com/distilnet/distil-proxy/internal/platform"
	"github.com/spf13/cobra"
)

func newStartCmd() *cobra.Command {
	var foreground bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start daemon in the background",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				if errors.Is(err, config.ErrConfigNotFound) {
					return errors.New("config not found; run 'distil-proxy auth' first")
				}
				return err
			}

			if foreground {
				return daemon.StartForeground(cmd.Context(), paths, cfg, cmd.OutOrStdout())
			}

			if err := daemon.Start(paths, cfg); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy started")
			return nil
		},
	}

	cmd.Flags().BoolVar(&foreground, "foreground", false, "run daemon in foreground mode")
	return cmd
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			err = daemon.Stop(paths)
			if err != nil {
				if errors.Is(err, daemon.ErrNotRunning) {
					fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy is not running")
					return nil
				}
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy stopped")
			return nil
		},
	}
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				if errors.Is(err, config.ErrConfigNotFound) {
					return errors.New("config not found; run 'distil-proxy auth' first")
				}
				return err
			}

			if err := daemon.Restart(paths, cfg); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy restarted")
			return nil
		},
	}
}

func newAuthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "auth [dk_or_dpk_key]",
		Short: "Authenticate distil-proxy on this machine",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return saveAuthKey(cmd, args[0])
			}
			return runInteractiveAuth(cmd)
		},
	}
}

type installAuthResponse struct {
	Status string `json:"status"`
	Email  string `json:"email"`
	APIKey string `json:"api_key"`
	Proxy  string `json:"proxy_key"`
}

type installErrorResponse struct {
	Error string `json:"error"`
}

func runInteractiveAuth(cmd *cobra.Command) error {
	paths, err := config.DetectPaths()
	if err != nil {
		return err
	}

	cfg, err := config.Load(paths)
	if err != nil {
		cfg = config.Config{}
	}
	cfg.ApplyDefaults()
	baseURL := resolveAuthBaseURL(cfg.Server)
	reader := bufio.NewReader(cmd.InOrStdin())

	credential, err := readPromptLine(reader, cmd.OutOrStdout(), "Enter your email or existing API key: ")
	if err != nil {
		return err
	}
	if credential == "" {
		return errors.New("email or API key is required")
	}

	email := credential
	if strings.HasPrefix(credential, "dk_") || strings.HasPrefix(credential, "dpk_") {
		keyResp, keyErr := postInstallJSON(baseURL, "/api/v1/install/key", map[string]string{
			"api_key": credential,
		})
		if keyErr != nil {
			return fmt.Errorf("API key authentication failed: %w", keyErr)
		}
		email = strings.TrimSpace(keyResp.Email)
		if email == "" {
			email, err = readPromptLine(reader, cmd.OutOrStdout(), "Enter the account email to receive a 6-digit code: ")
			if err != nil {
				return err
			}
		}
	}
	if email == "" {
		return errors.New("email is required for verification")
	}

	if _, regErr := postInstallJSON(baseURL, "/api/v1/install/register", map[string]string{
		"email": email,
	}); regErr != nil {
		return fmt.Errorf("could not send verification code: %w", regErr)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "We just sent a 6-digit code to %s\n", email)
	code, err := readPromptLine(reader, cmd.OutOrStdout(), "Enter the 6-digit code sent to your email: ")
	if err != nil {
		return err
	}
	if code == "" {
		return errors.New("verification code is required")
	}

	verifyResp, verifyErr := postInstallJSON(baseURL, "/api/v1/install/verify", map[string]string{
		"email": email,
		"code":  code,
	})
	if verifyErr != nil {
		return fmt.Errorf("verification failed: %w", verifyErr)
	}

	// Prefer api_key (dk_) since the websocket client requires it for authentication.
	// Fall back to proxy_key (dpk_) only if api_key is not provided.
	daemonKey := strings.TrimSpace(verifyResp.APIKey)
	if daemonKey == "" {
		daemonKey = strings.TrimSpace(verifyResp.Proxy)
	}
	if daemonKey == "" {
		return errors.New("verification succeeded but no daemon key was returned")
	}

	return saveAuthKey(cmd, daemonKey)
}

func saveAuthKey(cmd *cobra.Command, key string) error {
	apiKey := strings.TrimSpace(key)
	if err := config.ValidateAPIKey(apiKey); err != nil {
		return err
	}

	paths, err := config.DetectPaths()
	if err != nil {
		return err
	}

	cfg, err := config.Load(paths)
	if err != nil {
		cfg = config.Config{}
	}

	cfg.APIKey = apiKey
	if err := config.Save(paths, cfg); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "updated config: %s\n", paths.ConfigFile)
	return nil
}

func readPromptLine(reader *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return strings.TrimSpace(line), nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func resolveAuthBaseURL(server string) string {
	if override := strings.TrimSpace(os.Getenv("DISTIL_AUTH_BASE_URL")); override != "" {
		return strings.TrimRight(override, "/")
	}

	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || u == nil || u.Hostname() == "" {
		return "https://distil.net"
	}

	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	scheme := "https"
	if u.Scheme == "ws" || u.Scheme == "http" {
		scheme = "http"
	}

	if (host == "localhost" || host == "127.0.0.1") && u.Port() == "3120" {
		return fmt.Sprintf("%s://%s:3000", scheme, host)
	}
	if host == "proxy.distil.net" {
		return "https://distil.net"
	}
	if strings.HasPrefix(host, "proxy.") {
		return fmt.Sprintf("https://%s", strings.TrimPrefix(host, "proxy."))
	}

	return "https://distil.net"
}

func postInstallJSON(baseURL, path string, payload map[string]string) (installAuthResponse, error) {
	requestURL := strings.TrimRight(baseURL, "/") + path
	body, err := json.Marshal(payload)
	if err != nil {
		return installAuthResponse{}, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return installAuthResponse{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return installAuthResponse{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return installAuthResponse{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e installErrorResponse
		if decodeErr := json.Unmarshal(raw, &e); decodeErr == nil && strings.TrimSpace(e.Error) != "" {
			return installAuthResponse{}, fmt.Errorf("%s", e.Error)
		}
		return installAuthResponse{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	var parsed installAuthResponse
	if len(raw) == 0 {
		return parsed, nil
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return installAuthResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return parsed, nil
}

func newLogsCmd() *cobra.Command {
	var lineCount int

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print recent daemon logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			lines, err := daemon.ReadLogTail(paths, lineCount)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Fprintln(cmd.OutOrStdout(), "no log file found")
					return nil
				}
				return err
			}

			for _, line := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}

	cmd.Flags().IntVarP(&lineCount, "lines", "n", 100, "number of lines to print")

	return cmd
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__run",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			cfg, err := config.Load(paths)
			if err != nil {
				return err
			}

			logger, err := observability.NewLogger(cfg.LogLevel, os.Stdout)
			if err != nil {
				return err
			}
			logger.LogAttrs(cmd.Context(), slog.LevelInfo, "daemon runtime boot")

			return daemon.Run(cmd.Context(), paths, cfg, logger)
		},
	}
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove daemon files and service definitions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			if err := daemon.Stop(paths); err != nil && !errors.Is(err, daemon.ErrNotRunning) {
				return err
			}

			if err := platform.RemoveServiceDefinitions(paths.HomeDir); err != nil {
				return err
			}

			for _, link := range []string{"/usr/local/bin/distil-proxy", filepath.Join(paths.HomeDir, ".local", "bin", "distil-proxy")} {
				if removeErr := removeSymlinkIfPresent(link); removeErr != nil {
					return removeErr
				}
			}

			if err := os.RemoveAll(paths.RootDir); err != nil {
				return fmt.Errorf("remove runtime directory: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), "distil-proxy uninstalled")
			return nil
		},
	}
}

func removeSymlinkIfPresent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove symlink %s: %w", path, err)
	}
	return nil
}
