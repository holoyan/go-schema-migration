package migrate

// Logger receives diagnostic messages from the Migrator. Implement
// this to plug in your own logging library. Pass nil to use a no-op
// logger (recommended for most callers).
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debugf(string, ...any) {}
func (noopLogger) Infof(string, ...any)  {}
func (noopLogger) Warnf(string, ...any)  {}

func defaultLogger(l Logger) Logger {
	if l == nil {
		return noopLogger{}
	}
	return l
}
