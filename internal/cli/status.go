package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/exec-io/distil-proxy/internal/config"
	"github.com/exec-io/distil-proxy/internal/daemon"
	"github.com/exec-io/distil-proxy/internal/version"
	"github.com/spf13/cobra"
)

const (
	defaultNodeCity    = "Sydney"
	defaultNodeCountry = "AU"
	defaultNodeType    = "residential"

	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiCyan   = "\x1b[36m"
)

var (
	statusDaemonFunc     = daemon.Status
	statusConfigLoadFunc = config.Load
	statusNowFunc        = time.Now
)

type statusLocation struct {
	City    string `json:"city"`
	Country string `json:"country"`
	Type    string `json:"type"`
}

type statusWebSocket struct {
	URL             string `json:"url"`
	State           string `json:"state"`
	ConnectAttempts int64  `json:"connect_attempts"`
	Reconnects      int64  `json:"reconnects"`
	LastError       string `json:"last_error"`
}

type statusJobs struct {
	Served  int64 `json:"served"`
	Success int64 `json:"success"`
	Error   int64 `json:"error"`
}

type statusLatency struct {
	AverageMS int64 `json:"average_ms"`
	LE100MS   int64 `json:"le_100_ms"`
	LE500MS   int64 `json:"le_500_ms"`
	LE1000MS  int64 `json:"le_1000_ms"`
	GT1000MS  int64 `json:"gt_1000_ms"`
}

type statusOutput struct {
	Version       string          `json:"version"`
	Status        string          `json:"status"`
	PID           int             `json:"pid"`
	UptimeSeconds int64           `json:"uptime_seconds"`
	UptimeHuman   string          `json:"uptime_human"`
	StartTime     string          `json:"start_time"`
	Email         string          `json:"email,omitempty"`
	Location      statusLocation  `json:"location"`
	WebSocket     statusWebSocket `json:"websocket"`
	Jobs          statusJobs      `json:"jobs"`
	Latency       statusLatency   `json:"latency"`

	StartTimeHuman string `json:"-"`
	WebSocketHost  string `json:"-"`
}

func newStatusCmd(info version.Info) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := config.DetectPaths()
			if err != nil {
				return err
			}

			status, err := statusDaemonFunc(paths)
			if err != nil {
				return err
			}

			cfg, cfgErr := statusConfigLoadFunc(paths)
			if cfgErr != nil {
				cfg = config.Config{}
			}
			cfg.ApplyDefaults()

			accountEmail := strings.TrimSpace(cfg.Email)
			if cfgErr == nil && accountEmail == "" && strings.TrimSpace(cfg.APIKey) != "" {
				if account, lookupErr := lookupAccountForConfig(paths, &cfg, statusAccountLookupTimeout); lookupErr == nil {
					accountEmail = strings.TrimSpace(account.Email)
				}
			}

			out := buildStatusOutput(info.Version, cfg.Server, accountEmail, status, statusNowFunc())
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			renderStatusHuman(cmd.OutOrStdout(), out, supportsANSIColor(cmd.OutOrStdout()))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output status as JSON")
	return cmd
}

func buildStatusOutput(versionStr string, websocketURL string, email string, status daemon.RuntimeStatus, now time.Time) statusOutput {
	uptimeSeconds := status.UptimeSeconds
	if uptimeSeconds < 0 {
		uptimeSeconds = 0
	}

	startedAt := status.StartedAt
	if startedAt.IsZero() {
		startedAt = now.Add(-time.Duration(uptimeSeconds) * time.Second)
	}
	startLocal := startedAt.In(time.Local)

	state := strings.TrimSpace(status.WSState)
	if !status.Running {
		state = "stopped"
	}
	if state == "" {
		state = "starting"
	}

	serverURL := strings.TrimSpace(websocketURL)
	if serverURL == "" {
		serverURL = config.DefaultServerURL
	}
	host := websocketHost(serverURL)
	if host == "" {
		host = "proxy.distil.net"
	}

	return statusOutput{
		Version:       versionStr,
		Status:        state,
		PID:           status.PID,
		UptimeSeconds: uptimeSeconds,
		UptimeHuman:   humanDuration(uptimeSeconds),
		StartTime:     startLocal.Format(time.RFC3339),
		Email:         strings.TrimSpace(email),
		Location: statusLocation{
			City:    defaultNodeCity,
			Country: defaultNodeCountry,
			Type:    defaultNodeType,
		},
		WebSocket: statusWebSocket{
			URL:             serverURL,
			State:           state,
			ConnectAttempts: status.ConnectAttempts,
			Reconnects:      status.Reconnects,
			LastError:       status.LastError,
		},
		Jobs: statusJobs{
			Served:  status.JobsServed,
			Success: status.JobsSuccess,
			Error:   status.JobsError,
		},
		Latency: statusLatency{
			AverageMS: status.AvgLatencyMS,
			LE100MS:   status.LatencyLE100MS,
			LE500MS:   status.LatencyLE500MS,
			LE1000MS:  status.LatencyLE1000MS,
			GT1000MS:  status.LatencyGT1000MS,
		},
		StartTimeHuman: startLocal.Format("2006-01-02 15:04 MST"),
		WebSocketHost:  host,
	}
}

func renderStatusHuman(w io.Writer, out statusOutput, useColor bool) {
	statusIcon, statusHint, statusColor := statusPresentation(out.Status, out.WebSocketHost)
	stateText := out.Status
	if useColor {
		stateText = colorize(stateText, statusColor, useColor)
	}

	fmt.Fprintln(w, colorize("distil-proxy v"+out.Version, ansiBold, useColor))
	printField(w, "Status:", fmt.Sprintf("%-18s %s", stateText+" "+statusIcon, statusHint))
	printField(w, "PID:", fmt.Sprintf("%d", out.PID))
	printField(w, "Uptime:", fmt.Sprintf("%s (since %s)", out.UptimeHuman, out.StartTimeHuman))
	accountText := out.Email
	if accountText == "" {
		accountText = "unavailable"
	}
	printField(w, "Account:", accountText)
	printField(w, "Your Node:", fmt.Sprintf("%s, %s (%s IP active when connected)", out.Location.City, out.Location.Country, out.Location.Type))
	fmt.Fprintln(w)

	fmt.Fprintln(w, colorize("Connection:", ansiBold+ansiCyan, useColor))
	printIndentedField(w, "WebSocket:", out.WebSocket.URL)
	printIndentedField(w, "State:", out.WebSocket.State)
	printIndentedField(w, "Attempts:", fmt.Sprintf("%d total (%d reconnects)", out.WebSocket.ConnectAttempts, out.WebSocket.Reconnects))
	lastErr := strings.TrimSpace(out.WebSocket.LastError)
	if lastErr == "" {
		lastErr = "none"
	}
	printIndentedField(w, "Last error:", lastErr)
	fmt.Fprintln(w)

	fmt.Fprintln(w, colorize("Activity (this session):", ansiBold+ansiCyan, useColor))
	printIndentedField(w, "Jobs served:", fmt.Sprintf("%d", out.Jobs.Served))
	printIndentedField(w, "Success:", fmt.Sprintf("%d", out.Jobs.Success))
	printIndentedField(w, "Errors:", fmt.Sprintf("%d", out.Jobs.Error))
	fmt.Fprintln(w)

	fmt.Fprintln(w, colorize("Latency stats:", ansiBold+ansiCyan, useColor))
	printIndentedField(w, "Average:", fmt.Sprintf("%d ms", out.Latency.AverageMS))
	printIndentedField(w, "≤ 100 ms:", fmt.Sprintf("%d", out.Latency.LE100MS))
	printIndentedField(w, "≤ 500 ms:", fmt.Sprintf("%d", out.Latency.LE500MS))
	printIndentedField(w, "≤ 1000 ms:", fmt.Sprintf("%d", out.Latency.LE1000MS))
	printIndentedField(w, "> 1000 ms:", fmt.Sprintf("%d", out.Latency.GT1000MS))
	fmt.Fprintln(w)

	fmt.Fprintln(w, colorize("Quick tips:", ansiBold+ansiCyan, useColor))
	fmt.Fprintln(w, "  • Run 'distil-proxy logs' to see detailed logs")
	fmt.Fprintln(w, "  • Run 'distil fetch https://example.com' to test a request")
	fmt.Fprintln(w, "  • Run 'distil-proxy dashboard' to sign in to your dashboard")

	if out.Status == "reconnecting" || out.Status == "disconnected" || out.Status == "error" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "If reconnecting persists, try:")
		fmt.Fprintln(w, "  • distil-proxy restart")
		fmt.Fprintln(w, "  • Check your firewall/VPN")
		fmt.Fprintln(w, "  • Visit status.distil.net for network status")
	}
}

func statusPresentation(state string, host string) (icon string, hint string, color string) {
	switch state {
	case "connected":
		return "✅", fmt.Sprintf("(connected to %s)", host), ansiGreen
	case "reconnecting":
		return "⚠️", fmt.Sprintf("(attempting to reconnect to %s)", host), ansiYellow
	case "disconnected":
		return "⚠️", fmt.Sprintf("(disconnected from %s)", host), ansiYellow
	case "error":
		return "❌", "(daemon reported an error)", ansiRed
	case "stopped":
		return "❌", "(daemon is not running)", ansiRed
	default:
		return "ℹ️", "(daemon is starting)", ansiYellow
	}
}

func printField(w io.Writer, key string, value string) {
	fmt.Fprintf(w, "%-12s %s\n", key, value)
}

func printIndentedField(w io.Writer, key string, value string) {
	fmt.Fprintf(w, "  %-11s %s\n", key, value)
}

func humanDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}

	remaining := seconds
	days := remaining / 86400
	remaining %= 86400
	hours := remaining / 3600
	remaining %= 3600
	minutes := remaining / 60
	secs := remaining % 60

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	parts = append(parts, fmt.Sprintf("%ds", secs))
	return strings.Join(parts, " ")
}

func websocketHost(serverURL string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed == nil {
		return ""
	}
	return strings.TrimSpace(parsed.Hostname())
}

func supportsANSIColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}

	fdWriter, ok := w.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	file := os.NewFile(fdWriter.Fd(), "")
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func colorize(value string, style string, enabled bool) string {
	if !enabled || style == "" {
		return value
	}
	return style + value + ansiReset
}
