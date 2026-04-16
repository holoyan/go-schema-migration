package migrate

import "testing"

func TestNoopLoggerSatisfiesInterface(t *testing.T) {
	var l Logger = noopLogger{}
	l.Debugf("hello %s", "world")
	l.Infof("x=%d", 1)
	l.Warnf("oops")
	// no panic = pass
}

func TestDefaultLoggerIsNoop(t *testing.T) {
	got := defaultLogger(nil)
	if got == nil {
		t.Fatal("defaultLogger(nil) must not return nil")
	}
	if _, ok := got.(noopLogger); !ok {
		t.Fatalf("defaultLogger(nil) must return noopLogger, got %T", got)
	}
}

func TestDefaultLoggerPassesThrough(t *testing.T) {
	custom := &recordingLogger{}
	got := defaultLogger(custom)
	if got != custom {
		t.Fatalf("defaultLogger should pass non-nil logger through unchanged")
	}
}

type recordingLogger struct{ lines []string }

func (r *recordingLogger) Debugf(format string, args ...any) {}
func (r *recordingLogger) Infof(format string, args ...any)  {}
func (r *recordingLogger) Warnf(format string, args ...any)  {}
