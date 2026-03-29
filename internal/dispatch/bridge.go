package dispatch

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

// BridgeToFileQueue writes a dispatched agent to the legacy queue file (~/.agentguard/queue.txt)
// for backward compatibility with existing workers.
//
// Migration path:
//   - Phase 1 (now): Dispatcher writes to queue.txt (workers unchanged)
//   - Phase 2 (later): Workers read from Redis priority queue directly
//   - Phase 3 (final): Remove queue.txt entirely
//
// Uses flock to match existing enqueue.sh behavior.
func (d *Dispatcher) BridgeToFileQueue(agentName string) error {
	if d.queueFile == "" {
		return nil // bridge disabled
	}

	// Open or create queue file
	f, err := os.OpenFile(d.queueFile, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Acquire exclusive lock (matching enqueue.sh flock behavior)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	// Check for duplicates -- don't enqueue if already present
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == agentName {
			return nil // already queued
		}
	}

	// Seek to end and append
	if _, err := f.Seek(0, 2); err != nil {
		return err
	}
	_, err = f.WriteString(agentName + "\n")
	return err
}
