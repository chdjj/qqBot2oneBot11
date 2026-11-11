// Package buildinfo carries values stamped in at compile time.
package buildinfo

// Set via -ldflags "-X qqbotonebot/internal/buildinfo.Version=...".
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String renders a short human-readable version, e.g. "v1.2.3 (a1b2c3d, 2026-11-11)".
func String() string {
	if Commit == "unknown" && Date == "unknown" {
		return Version
	}
	return Version + " (" + Commit + ", " + Date + ")"
}
