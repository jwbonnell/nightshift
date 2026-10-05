// Package notify sends desktop notifications about a finished run.
package notify

import (
	"context"
	"os/exec"
	"time"
)

// Notifier sends one notification. urgent selects the OS-level urgency.
type Notifier interface {
	Notify(title, msg string, urgent bool)
}

// Noop sends nothing; selected by NIGHTSHIFT_NOTIFY=off.
type Noop struct{}

func (Noop) Notify(title, msg string, urgent bool) {}

var _ Notifier = Noop{}

// Desktop sends a notification via notify-send, silently doing nothing if
// it is not found on PATH. Bin overrides the binary name/path for tests;
// "" means "notify-send".
type Desktop struct {
	Bin string
}

func (d Desktop) Notify(title, msg string, urgent bool) {
	bin := d.Bin
	if bin == "" {
		bin = "notify-send"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return
	}

	urgency := "normal"
	if urgent {
		urgency = "critical"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, bin, "--app-name=nightshift", "--urgency="+urgency, title, msg).Run()
}

var _ Notifier = Desktop{}
