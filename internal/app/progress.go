package app

import (
	"context"
	"errors"
	"os"
	"sync"
)

// The steps of an install the page shows a bar for.
const (
	phaseDownload = "download"
	phaseUnpack   = "unpack"
	phaseApply    = "apply" // putting the files in place: can't be stopped
)

// progress is what the running operation is doing: which mod, which step,
// how many bytes of how many. It has its own lock because downloads report
// into it many times a second.
type progress struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	stopped     bool
	item, phase string
	done, total int64
}

func (p *progress) start(cancel context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancel, p.stopped, p.item, p.phase, p.done, p.total = cancel, false, "", "", 0, 0
}

func (p *progress) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancel, p.stopped, p.item, p.phase, p.done, p.total = nil, false, "", "", 0, 0
}

// name says which mod the operation is working on.
func (p *progress) name(item string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.item, p.phase, p.done, p.total = item, "", 0, 0
}

func (p *progress) set(phase string, done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase, p.done, p.total = phase, done, total
}

// ProgressView is the running operation as the page shows it.
type ProgressView struct {
	Busy  string `json:"busy,omitempty"`  // the operation ("Installing updates")
	Item  string `json:"item,omitempty"`  // the mod it is working on
	Phase string `json:"phase,omitempty"` // download, unpack, apply
	Done  int64  `json:"done"`
	Total int64  `json:"total"` // 0: not known
	// CanStop: Cancel would end it now (a download or an unpack is running).
	CanStop bool `json:"canStop"`
}

// Progress is polled by the page while something runs.
func (a *App) Progress() ProgressView {
	a.mu.Lock()
	busy := a.busy
	a.mu.Unlock()
	p := &a.prog
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProgressView{Busy: busy, Item: p.item, Phase: p.phase, Done: p.done, Total: p.total,
		CanStop: p.cancel != nil && !p.stopped && (p.phase == phaseDownload || p.phase == phaseUnpack)}
}

// ErrStopped: the user stopped the running download or install.
var ErrStopped = errors.New("Stopped. Install it again to continue where the download stopped.")

// Cancel stops the running download or unpack. What was downloaded so far is
// kept, so installing the same file again continues from there. It reports
// whether there was something to stop.
func (a *App) Cancel() bool {
	p := &a.prog
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel == nil || p.stopped || (p.phase != phaseDownload && p.phase != phaseUnpack) {
		return false
	}
	p.stopped = true
	p.cancel()
	return true
}

// stopped turns the error of an operation the user stopped into ErrStopped.
func (a *App) stopped(ctx context.Context, err error) error {
	a.prog.mu.Lock()
	stopped := a.prog.stopped
	a.prog.mu.Unlock()
	if err != nil && stopped && ctx.Err() != nil {
		return ErrStopped
	}
	return err
}

// dropDownload removes a downloaded file after its install. When the install
// was stopped while unpacking, the file is kept as its own ".part" instead,
// so the next try doesn't download it again.
func dropDownload(ctx context.Context, path string) {
	if ctx.Err() != nil {
		if os.Rename(path, path+".part") == nil {
			return
		}
	}
	os.Remove(path)
}
