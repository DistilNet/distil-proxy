package upgrade

import (
	"strconv"
	"strings"
)

func isNewerVersion(current, latest string) bool {
	currentParts := parseVersion(normalizeVersion(current))
	latestParts := parseVersion(normalizeVersion(latest))
	if len(currentParts) == 0 || len(latestParts) == 0 {
		return false
	}

	for i := 0; i < 3; i++ {
		var cur, lat int
		if i < len(currentParts) {
			cur = currentParts[i]
		}
		if i < len(latestParts) {
			lat = latestParts[i]
		}
		if lat > cur {
			return true
		}
		if lat < cur {
			return false
		}
	}
	return false
}

func normalizeVersion(version string) string {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(v, "v")
	return v
}

func parseVersion(version string) []int {
	parts := strings.Split(version, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}
