package kernel

import (
	"log/slog"
)

// loggerOrDefault returns the builder-configured logger, falling back to the
// package-level slog default.
func (b *ContainerBuilder) loggerOrDefault() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}
