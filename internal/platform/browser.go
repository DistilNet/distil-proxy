package platform

import "fmt"

// OpenBrowser opens a URL in the user's default browser.
func OpenBrowser(targetURL string) error {
	switch platformGOOS {
	case "darwin":
		if err := execCommandFunc("open", targetURL).Run(); err != nil {
			return fmt.Errorf("open browser: %w", err)
		}
	case "linux":
		if err := execCommandFunc("xdg-open", targetURL).Run(); err != nil {
			return fmt.Errorf("open browser: %w", err)
		}
	case "windows":
		if err := execCommandFunc("rundll32", "url.dll,FileProtocolHandler", targetURL).Run(); err != nil {
			return fmt.Errorf("open browser: %w", err)
		}
	default:
		return fmt.Errorf("open browser: unsupported os %s", platformGOOS)
	}

	return nil
}
