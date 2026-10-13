package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hehehai/AudioSmith/internal/core"
	"github.com/hehehai/AudioSmith/internal/engine/proto"
)

// NewSupervisor returns a Supervisor for cfg. Nothing runs until Start.
func NewSupervisor(cfg Config) Supervisor {
	if len(cfg.Roles) == 0 {
		cfg.Roles = []proto.Role{proto.RoleGPU}
	}
	if cfg.HelloTimeout == 0 {
		cfg.HelloTimeout = 30 * time.Second
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = 3
	}
	if cfg.RestartWindow == 0 {
		cfg.RestartWindow = 10 * time.Minute
	}
	if cfg.CancelGrace == 0 {
		cfg.CancelGrace = 5 * time.Second
	}
	if cfg.StatsEvery == 0 {
		cfg.StatsEvery = 2 * time.Second
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	s := &supervisor{cfg: cfg, workers: map[proto.Role]*worker{}}
	for _, r := range slices.Concat(cfg.Roles, cfg.OnDemand) {
		if _, dup := s.workers[r]; dup {
			continue
		}
		s.roles = append(s.roles, r)
		s.workers[r] = &worker{role: r, status: WorkerStatus{Role: r, State: WorkerStopped, Since: time.Now()}}
	}
	return s
}

type supervisor struct {
	cfg     Config
	roles   []proto.Role // Roles, then OnDemand
	ctrlID  atomic.Int64
	mu      sync.Mutex // guards the workers' fields
	workers map[proto.Role]*worker
	stopped bool
}

type worker struct {
	role     proto.Role
	status   WorkerStatus
	proc     *proc         // the running process; nil when none
	ready    chan struct{} // closed when the current start succeeded or failed
	restarts []time.Time   // automatic restarts within the window
	timer    *time.Timer   // the pending automatic restart
}

// proc is one engine process.
type proc struct {
	s      *supervisor
	w      *worker
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	tail   *lineTail
	exited chan struct{}

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]*call
	closed  bool

	helloDone   atomic.Bool
	intentional atomic.Bool // stopped on purpose: no failure, no restart
	// Set before the process is killed for a reason of the supervisor.
	failReason FailReason
	failErr    string
	noRestart  bool
	pollNow    chan struct{}
}

type call struct {
	method string
	job    bool
	notify func(proto.Message)
	done   chan struct{}
	resp   proto.Message
	err    error
}

func (c *call) result() (json.RawMessage, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.resp.Error != nil {
		return nil, c.resp.Error
	}
	return c.resp.Result, nil
}

func (s *supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = false
	var ws []*worker
	for _, r := range s.cfg.Roles {
		w := s.workers[r]
		if w.proc == nil && w.timer == nil {
			ws = append(ws, w)
		}
	}
	s.mu.Unlock()
	for _, w := range ws {
		s.spawn(w, false)
	}
	return nil
}

// publish sends w's status on the bus. Never called with s.mu held.
func (s *supervisor) publish(st WorkerStatus) {
	s.cfg.Bus.Publish(StatusChanged{Status: st})
}

// setStatus changes w's status under s.mu and returns the copy to publish.
func (s *supervisor) statusLocked(w *worker) WorkerStatus {
	st := w.status
	st.Hello = cloneHello(st.Hello)
	st.Stats = cloneStats(st.Stats)
	if w.proc != nil {
		st.Running = w.proc.running()
	}
	return st
}

func (s *supervisor) Status() []WorkerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkerStatus, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, s.statusLocked(s.workers[r]))
	}
	return out
}

// spawn starts a process for w. auto marks an automatic restart.
func (s *supervisor) spawn(w *worker, auto bool) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	w.timer = nil
	w.status.RestartAt = time.Time{}
	if auto {
		w.status.Restarts++
	}
	if w.ready == nil || isClosed(w.ready) {
		w.ready = make(chan struct{})
	}
	bin := s.cfg.Bin
	if bin == "" {
		s.failLocked(w, FailMissing, "audiosmith-engine was not found: build it with scripts/build-engine.sh, or set AUDIOSMITH_ENGINE", "")
		st := s.statusLocked(w)
		s.mu.Unlock()
		s.publish(st)
		return
	}
	s.mu.Unlock()

	if s.cfg.Prepare != nil {
		s.cfg.Prepare(w.role)
	}
	p, err := s.startProcess(w, bin)
	s.mu.Lock()
	if err != nil {
		s.failLocked(w, FailStart, err.Error(), "")
		st := s.statusLocked(w)
		s.mu.Unlock()
		s.publish(st)
		return
	}
	if s.stopped {
		s.mu.Unlock()
		p.intentional.Store(true)
		p.kill()
		return
	}
	w.proc = p
	w.status.State, w.status.PID, w.status.Since = WorkerStarting, p.cmd.Process.Pid, time.Now()
	w.status.Err, w.status.Reason, w.status.StderrTail = "", "", ""
	w.status.Hello, w.status.Stats = nil, nil
	st := s.statusLocked(w)
	s.mu.Unlock()
	s.publish(st)
	go s.handshake(w, p)
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// failLocked marks w failed and wakes callers waiting for it. Caller holds s.mu.
func (s *supervisor) failLocked(w *worker, reason FailReason, msg, tail string) {
	w.status.State, w.status.Reason, w.status.Err, w.status.StderrTail = WorkerFailed, reason, msg, tail
	w.status.PID, w.status.Hello, w.status.Stats, w.status.Since = 0, nil, nil, time.Now()
	if w.ready != nil && !isClosed(w.ready) {
		close(w.ready)
	}
}

func (s *supervisor) startProcess(w *worker, bin string) (*proc, error) {
	cmd := exec.Command(bin, "--role", string(w.role))
	cmd.Dir = filepath.Dir(bin)
	cmd.Env = append(os.Environ(), s.cfg.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", bin, err)
	}
	p := &proc{s: s, w: w, cmd: cmd, stdin: stdin, tail: newLineTail(40), exited: make(chan struct{}),
		pending: map[string]*call{}, pollNow: make(chan struct{}, 1)}
	var logFile *os.File
	if s.cfg.LogDir != "" {
		logFile = openLog(filepath.Join(s.cfg.LogDir, "engine-"+string(w.role)+".log"))
	}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			p.tail.add(line)
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
			if strings.TrimSpace(line) != "" {
				s.emit(core.LogEntry{Time: time.Now(), Level: stderrLevel(line), Source: "engine:" + string(w.role), Message: line})
			}
		}
	}()
	go func() {
		p.read(stdout)
		<-stderrDone
		err := cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		s.exited(p, err)
	}()
	return p, nil
}

// openLog opens the stderr log for appending, keeping one previous file
// once it passes 10 MB.
func openLog(path string) *os.File {
	if st, err := os.Stat(path); err == nil && st.Size() > 10<<20 {
		os.Rename(path, path+".1")
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return f
}

func (s *supervisor) handshake(w *worker, p *proc) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.HelloTimeout)
	defer cancel()
	s.mu.Lock()
	cacheDir := s.cfg.CacheDir
	s.mu.Unlock()
	raw, err := p.call(ctx, "", proto.MethodHello, proto.HelloParams{
		Protocol: proto.Version, App: s.cfg.AppVersion, Role: w.role,
		CacheDir: cacheDir, Offline: s.cfg.Offline, LogLevel: "info",
	}, nil, false)
	var hello proto.HelloResult
	if err == nil {
		err = json.Unmarshal(raw, &hello)
	}
	var perr *proto.Error
	switch {
	case p.isClosed():
		return // exited (legacy engine, crash): exited reports it
	case errors.Is(err, context.DeadlineExceeded):
		p.killFor(FailHandshake, fmt.Sprintf("the engine did not answer engine.hello within %s", s.cfg.HelloTimeout), false)
		return
	case errors.As(err, &perr) && perr.Code == proto.CodeProtocolMismatch:
		p.killFor(FailProtocol, "engine version mismatch: rebuild audiosmith-engine ("+perr.Message+")", true)
		return
	case err != nil:
		p.killFor(FailHandshake, "engine.hello failed: "+err.Error(), false)
		return
	case hello.Protocol != proto.Version || hello.Role != w.role:
		p.killFor(FailProtocol, fmt.Sprintf("engine version mismatch: rebuild audiosmith-engine (it speaks protocol %d as role %q; want %d as %q)",
			hello.Protocol, hello.Role, proto.Version, w.role), true)
		return
	}
	p.helloDone.Store(true)
	s.mu.Lock()
	if w.proc != p {
		s.mu.Unlock()
		return
	}
	w.status.State, w.status.Since, w.status.Hello = WorkerReady, time.Now(), &hello
	if hello.PID != 0 {
		w.status.PID = hello.PID
	}
	if !isClosed(w.ready) {
		close(w.ready)
	}
	st := s.statusLocked(w)
	s.mu.Unlock()
	s.publish(st)
	if s.cfg.StatsEvery > 0 {
		go s.pollStats(w, p)
	}
}

// read routes the engine's stdout: responses to their calls, a job's
// notifications to its call's notify, log lines to the bus.
func (p *proc) read(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := readLine(r)
		if len(line) > 0 {
			p.handleLine(line)
		}
		if err != nil {
			if err != io.EOF {
				p.tail.add("stdout: " + err.Error())
			}
			// Drain, so the process is not blocked writing.
			io.Copy(io.Discard, r)
			return
		}
	}
}

// readLine reads one line of at most proto.MaxLine bytes; a longer one is
// skipped and reported.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if len(buf)+len(chunk) <= proto.MaxLine {
			buf = append(buf, chunk...)
		} else {
			buf = buf[:0:0]
		}
		if err != nil || !isPrefix {
			return buf, err
		}
	}
}

func (p *proc) handleLine(line []byte) {
	if !p.helloDone.Load() {
		if v, ok := proto.LegacyReady(line); ok {
			p.killFor(FailProtocol, fmt.Sprintf("the engine speaks protocol %d: rebuild audiosmith-engine", v), true)
			return
		}
	}
	m, err := proto.Decode(line)
	if err != nil {
		p.tail.add("stdout: " + string(line))
		return
	}
	switch m.Kind() {
	case proto.KindResponse:
		p.mu.Lock()
		c := p.pending[m.ID]
		delete(p.pending, m.ID)
		p.mu.Unlock()
		if c != nil {
			c.resp = m
			close(c.done)
			if c.job || c.method == proto.MethodModelsUnload {
				p.poll() // the resident models may have changed
			}
		}
	case proto.KindNotification:
		var ref struct {
			Job string `json:"job"`
		}
		m.DecodeParams(&ref)
		if m.Method == proto.MethodLog {
			p.log(m)
		}
		if ref.Job == "" {
			return
		}
		p.mu.Lock()
		c := p.pending[ref.Job]
		p.mu.Unlock()
		if c != nil && c.notify != nil {
			c.notify(m)
		}
	}
}

func (p *proc) log(m proto.Message) {
	var lp proto.LogParams
	if m.DecodeParams(&lp) != nil {
		return
	}
	level := core.Severity(lp.Level)
	switch level {
	case core.SeverityDebug, core.SeverityInfo, core.SeverityWarning, core.SeverityError:
	default:
		level = core.SeverityInfo
	}
	p.s.emit(core.LogEntry{Time: time.Now(), Level: level, Source: "engine:" + string(p.w.role), Message: lp.Message, JobID: lp.Job})
}

// emit hands a log line to Config.Log, or the bus.
func (s *supervisor) emit(e core.LogEntry) {
	if s.cfg.Log != nil {
		s.cfg.Log(e)
		return
	}
	s.cfg.Bus.Publish(core.LogAppended{Entry: e})
}

// stderrLevel guesses the level of a free-form stderr line.
func stderrLevel(line string) core.Severity {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "fatal") || strings.Contains(l, "error"):
		return core.SeverityError
	case strings.Contains(l, "warning"):
		return core.SeverityWarning
	}
	return core.SeverityInfo
}

func (p *proc) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *proc) running() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for id, c := range p.pending {
		if c.job {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (p *proc) jobCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.pending {
		if c.job {
			n++
		}
	}
	return n
}

func (p *proc) poll() {
	select {
	case p.pollNow <- struct{}{}:
	default:
	}
}

// killFor kills the process for a reason of the supervisor's; exited
// reports it (and restarts it unless noRestart).
func (p *proc) killFor(reason FailReason, msg string, noRestart bool) {
	p.mu.Lock()
	if p.failReason == "" {
		p.failReason, p.failErr, p.noRestart = reason, msg, noRestart
	}
	p.mu.Unlock()
	p.kill()
}

func (p *proc) kill() {
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}

func (p *proc) write(m proto.Message) error {
	line, err := proto.Encode(m)
	if err != nil {
		return err
	}
	if len(line) > proto.MaxLine {
		return fmt.Errorf("request of %d bytes is over the protocol's limit", len(line))
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdin.Write(line); err != nil {
		return fmt.Errorf("writing to the engine: %v: %w", err, ErrWorkerExited)
	}
	return nil
}

// call sends a request and waits for its response (see Supervisor.Call).
func (p *proc) call(ctx context.Context, id, method string, params any, notify func(proto.Message), job bool) (json.RawMessage, error) {
	if id == "" {
		id = fmt.Sprintf("c%d", p.s.ctrlID.Add(1))
	}
	req, err := proto.NewRequest(id, method, params)
	if err != nil {
		return nil, fmt.Errorf("%s params: %w", method, err)
	}
	c := &call{method: method, job: job, notify: notify, done: make(chan struct{})}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrWorkerExited
	}
	if _, dup := p.pending[id]; dup {
		p.mu.Unlock()
		return nil, fmt.Errorf("request id %s is already in flight: %w", id, core.ErrInvalid)
	}
	p.pending[id] = c
	p.mu.Unlock()
	if err := p.write(req); err != nil {
		p.drop(id)
		return nil, err
	}
	if job {
		p.poll()
	}

	select {
	case <-c.done:
		return c.result()
	case <-ctx.Done():
	}
	if !job {
		p.drop(id)
		return nil, ctx.Err()
	}
	// Ask the engine to stop the job; it answers the job with "cancelled".
	go func() {
		cctx, cancel := context.WithTimeout(context.Background(), p.s.cfg.CancelGrace)
		defer cancel()
		p.call(cctx, "", proto.MethodJobCancel, proto.CancelParams{Job: id}, nil, false)
	}()
	grace := time.NewTimer(p.s.cfg.CancelGrace)
	defer grace.Stop()
	select {
	case <-c.done:
	case <-grace.C:
		p.s.cfg.Logf("engine %s: job %s did not stop within %s of being cancelled: restarting the worker", p.w.role, id, p.s.cfg.CancelGrace)
		p.killFor(FailStuck, fmt.Sprintf("job %s did not stop within %s of being cancelled", id, p.s.cfg.CancelGrace), false)
		<-c.done
	}
	return c.result()
}

func (p *proc) drop(id string) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

// exited handles the end of a process: its calls fail as interrupted, and
// an unexpected exit marks the worker failed and plans a restart.
func (s *supervisor) exited(p *proc, waitErr error) {
	p.mu.Lock()
	p.closed = true
	calls := p.pending
	p.pending = map[string]*call{}
	reason, msg, noRestart := p.failReason, p.failErr, p.noRestart
	p.mu.Unlock()
	close(p.exited)
	for _, c := range calls {
		c.err = ErrWorkerExited
		close(c.done)
	}

	w := p.w
	s.mu.Lock()
	if w.proc != p {
		s.mu.Unlock()
		return
	}
	w.proc = nil
	if p.intentional.Load() {
		s.mu.Unlock()
		return
	}
	if reason == "" {
		reason, msg = FailCrashed, "the engine exited"
		if waitErr != nil {
			msg += ": " + waitErr.Error()
		}
	}
	tail := p.tail.String()
	s.failLocked(w, reason, msg, tail)
	s.cfg.Logf("engine %s: %s", w.role, msg)
	if !noRestart && !s.stopped {
		now := time.Now()
		w.restarts = slices.DeleteFunc(w.restarts, func(t time.Time) bool { return now.Sub(t) > s.cfg.RestartWindow })
		if len(w.restarts) < s.cfg.MaxRestarts {
			delay := min(time.Second<<len(w.restarts), 30*time.Second)
			w.restarts = append(w.restarts, now)
			w.status.RestartAt = now.Add(delay)
			w.timer = time.AfterFunc(delay, func() { s.spawn(w, true) })
			w.ready = make(chan struct{}) // calls wait for the restart
		}
	}
	st := s.statusLocked(w)
	s.mu.Unlock()
	s.publish(st)
}

// ready waits for w to be ready and returns its process. A worker that is
// starting, or failed with a restart planned, is waited for (within ctx);
// a stopped one, or a failed one that will not restart, is an error at once.
func (s *supervisor) ready(ctx context.Context, role proto.Role) (*proc, error) {
	for {
		s.mu.Lock()
		w, ok := s.workers[role]
		if !ok {
			s.mu.Unlock()
			return nil, fmt.Errorf("no %s engine is configured: %w", role, core.ErrEngine)
		}
		if w.status.State == WorkerStopped && w.proc == nil && w.timer == nil && !s.stopped && slices.Contains(s.cfg.OnDemand, role) {
			// First call to an on-demand worker: start it and wait.
			w.status.State, w.status.Since = WorkerStarting, time.Now()
			w.ready = make(chan struct{})
			s.mu.Unlock()
			s.spawn(w, false)
			continue
		}
		st, p, ready := w.status, w.proc, w.ready
		s.mu.Unlock()
		switch st.State {
		case WorkerReady:
			if p != nil {
				return p, nil
			}
		case WorkerStarting, WorkerFailed:
			if st.State == WorkerFailed && st.RestartAt.IsZero() {
				break
			}
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		msg := string(st.State)
		if st.Err != "" {
			msg = st.Err
		}
		return nil, fmt.Errorf("the %s engine is not ready (%s): %w", role, msg, core.ErrEngine)
	}
}

func (s *supervisor) Call(ctx context.Context, role proto.Role, id, method string, params any, notify func(proto.Message)) (json.RawMessage, error) {
	p, err := s.ready(ctx, role)
	if err != nil {
		return nil, err
	}
	return p.call(ctx, id, method, params, notify, proto.IsJob(method))
}

// pollStats polls engine.stats while jobs run (and once after each),
// publishing changes and keeping resident models within the budget.
func (s *supervisor) pollStats(w *worker, p *proc) {
	tick := time.NewTicker(s.cfg.StatsEvery)
	defer tick.Stop()
	var lastIdlePoll time.Time
	for {
		select {
		case <-p.exited:
			return
		case <-tick.C:
			if p.jobCount() == 0 {
				// Idle: poll now and then only to unload idle models.
				if !s.idlePollDue(w, lastIdlePoll) {
					continue
				}
				lastIdlePoll = time.Now()
			}
		case <-p.pollNow:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		raw, err := p.call(ctx, "", proto.MethodStats, nil, nil, false)
		cancel()
		if err != nil {
			continue
		}
		var stats proto.StatsResult
		if json.Unmarshal(raw, &stats) != nil {
			continue
		}
		s.mu.Lock()
		if w.proc != p {
			s.mu.Unlock()
			return
		}
		changed := w.status.Stats == nil || !sameModels(w.status.Stats.Models, stats.Models)
		w.status.Stats = &stats
		st := s.statusLocked(w)
		s.mu.Unlock()
		if changed {
			s.publish(st)
		}
		s.evict()
	}
}

// idlePollDue reports whether an idle worker's stats should be polled to
// find models idle for longer than the idle-unload limit.
func (s *supervisor) idlePollDue(w *worker, last time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	idle := s.cfg.IdleUnload
	if idle <= 0 || w.status.Stats == nil || len(w.status.Stats.Models) == 0 {
		return false
	}
	every := min(max(idle/4, time.Second), 30*time.Second)
	return time.Since(last) >= every
}

// SetMemoryBudget changes Config.MemoryBudget and applies it at once.
func (s *supervisor) SetMemoryBudget(bytes int64) {
	s.mu.Lock()
	s.cfg.MemoryBudget = max(bytes, 0)
	s.mu.Unlock()
	go s.evict()
}

// SetCacheDir changes Config.CacheDir for the workers started next.
func (s *supervisor) SetCacheDir(dir string) {
	s.mu.Lock()
	s.cfg.CacheDir = dir
	s.mu.Unlock()
}

// SetIdleUnload changes Config.IdleUnload; it applies at the next poll.
func (s *supervisor) SetIdleUnload(d time.Duration) {
	s.mu.Lock()
	s.cfg.IdleUnload = max(d, 0)
	s.mu.Unlock()
	go s.evict()
}

func sameModels(a, b []proto.LoadedModel) bool {
	return slices.EqualFunc(a, b, func(x, y proto.LoadedModel) bool { return x.Model == y.Model })
}

// budget is the bytes resident models may take together.
func (s *supervisor) budgetLocked() int64 {
	if s.cfg.MemoryBudget > 0 {
		return s.cfg.MemoryBudget
	}
	for _, w := range s.workers {
		if w.status.Hello != nil && w.status.Hello.Device.Memory > 0 {
			return w.status.Hello.Device.Memory * 6 / 10
		}
	}
	return 0
}

// evict unloads the least recently used idle models while the resident
// models of all workers are over the budget, and every model idle for
// longer than IdleUnload.
func (s *supervisor) evict() {
	type candidate struct {
		role proto.Role
		m    proto.LoadedModel
	}
	s.mu.Lock()
	budget := s.budgetLocked()
	idle := s.cfg.IdleUnload.Seconds()
	var total int64
	var cands []candidate
	for _, w := range s.workers {
		if w.status.State != WorkerReady || w.status.Stats == nil {
			continue
		}
		for _, m := range w.status.Stats.Models {
			total += m.Bytes
			if m.IdleSeconds > 0 {
				cands = append(cands, candidate{w.role, m})
			}
		}
	}
	s.mu.Unlock()
	overBudget := budget > 0 && total > budget
	if !overBudget && idle <= 0 {
		return
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].m.IdleSeconds > cands[j].m.IdleSeconds })
	for _, c := range cands {
		tooIdle := idle > 0 && c.m.IdleSeconds >= idle
		if !tooIdle && (budget <= 0 || total <= budget) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := s.Call(ctx, c.role, "", proto.MethodModelsUnload, proto.UnloadParams{Model: c.m.Model}, nil)
		cancel()
		if err != nil {
			s.cfg.Logf("engine %s: unloading %s: %v", c.role, c.m.Model, err)
			continue
		}
		why := "to stay within the memory budget"
		if tooIdle {
			why = "after the idle limit"
		}
		s.cfg.Logf("engine %s: unloaded %s (idle %.0f s) %s", c.role, c.m.Model, c.m.IdleSeconds, why)
		total -= c.m.Bytes
		s.mu.Lock()
		if w := s.workers[c.role]; w.status.Stats != nil {
			st := *w.status.Stats
			st.Models = slices.DeleteFunc(slices.Clone(st.Models), func(m proto.LoadedModel) bool { return m.Model == c.m.Model })
			w.status.Stats = &st
		}
		st := s.statusLocked(s.workers[c.role])
		s.mu.Unlock()
		s.publish(st)
	}
}

func (s *supervisor) Restart(role proto.Role) error {
	s.mu.Lock()
	w, ok := s.workers[role]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("no %s engine is configured: %w", role, core.ErrInvalid)
	}
	s.stopped = false
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.restarts = nil
	w.status.Restarts = 0
	p := w.proc
	w.proc = nil
	// Callers wait for the new process instead of failing.
	w.status.State, w.status.Since = WorkerStarting, time.Now()
	if w.ready == nil || isClosed(w.ready) {
		w.ready = make(chan struct{})
	}
	s.mu.Unlock()
	if p != nil {
		p.intentional.Store(true)
		p.kill()
		<-p.exited
	}
	s.spawn(w, false)
	return nil
}

func (s *supervisor) Stop() {
	s.mu.Lock()
	s.stopped = true
	var procs []*proc
	var sts []WorkerStatus
	for _, r := range s.roles {
		w := s.workers[r]
		if w.timer != nil {
			w.timer.Stop()
			w.timer = nil
		}
		if w.proc != nil {
			procs = append(procs, w.proc)
			w.proc.intentional.Store(true)
		}
		w.proc = nil
		w.status.State, w.status.PID, w.status.Hello, w.status.Stats = WorkerStopped, 0, nil, nil
		w.status.RestartAt, w.status.Since = time.Time{}, time.Now()
		if w.ready != nil && !isClosed(w.ready) {
			close(w.ready)
		}
		sts = append(sts, s.statusLocked(w))
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			p.call(ctx, "", proto.MethodShutdown, nil, nil, false)
			cancel()
			p.stdin.Close()
			select {
			case <-p.exited:
			case <-time.After(5 * time.Second):
				p.kill()
				<-p.exited
			}
		})
	}
	wg.Wait()
	for _, st := range sts {
		s.publish(st)
	}
}

func cloneHello(h *proto.HelloResult) *proto.HelloResult {
	if h == nil {
		return nil
	}
	c := *h
	c.Methods = slices.Clone(h.Methods)
	c.Families = slices.Clone(h.Families)
	return &c
}

func cloneStats(st *proto.StatsResult) *proto.StatsResult {
	if st == nil {
		return nil
	}
	c := *st
	c.Models = slices.Clone(st.Models)
	c.Jobs = slices.Clone(st.Jobs)
	return &c
}

// lineTail keeps the last lines written to it.
type lineTail struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func newLineTail(max int) *lineTail { return &lineTail{max: max} }

func (t *lineTail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
