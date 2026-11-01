//go:build windows

package cmdrun

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// isolate gives the process a hidden console of its own, so neither it nor
// the tools it starts open a window, and a ctrl+c in the TUI's console
// never reaches it. It starts suspended, so attach can put it in a Job
// Object before it starts anything. It must not get
// CREATE_NEW_PROCESS_GROUP: that would make it ignore Ctrl+C.
func isolate(cmd *exec.Cmd) {
	clearIgnoreCtrlC()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED}
}

// processTree is a Job Object holding a process and every process it
// starts, which is killed when the job is.
type processTree struct {
	pid  int
	job  windows.Handle
	once sync.Once
}

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// attach puts the suspended process p in a new kill-on-close Job Object,
// then lets it run.
func attach(p *os.Process) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(p.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	// Go closes the thread handle after CreateProcess, so resume the
	// process as a whole.
	if status, _, _ := ntResumeProcess.Call(uintptr(h)); status != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		return nil, windows.NTStatus(status)
	}
	return &processTree{pid: p.Pid, job: job}, nil
}

// interrupt asks the process to stop as a terminal's ctrl+c would: a
// helper, this program run as `<exe> __ctrlc <pid>`, attaches to the
// process's console and sends CTRL_C_EVENT to everything in it.
func (t *processTree) interrupt() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, ctrlCArg, strconv.Itoa(t.pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	_ = cmd.Run()
}

// kill terminates every process in the job and closes it.
func (t *processTree) kill() {
	t.once.Do(func() {
		_ = windows.TerminateJobObject(t.job, 1)
		_ = windows.CloseHandle(t.job)
	})
}

// release leaves what is left in the job running: the job stops killing
// its processes when closed, then is closed.
func (t *processTree) release() {
	t.once.Do(func() {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		if _, err := windows.SetInformationJobObject(t.job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			_ = windows.TerminateJobObject(t.job, 1) // it can't be left: as kill
		}
		_ = windows.CloseHandle(t.job)
	})
}

// exitCanceled is the exit status of a process a console ctrl+c ended
// (STATUS_CONTROL_C_EXIT), Windows' 130.
const exitCanceled = 0xC000013A

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	setConsoleCtrlHandler    = kernel32.NewProc("SetConsoleCtrlHandler")
	attachConsole            = kernel32.NewProc("AttachConsole")
	generateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

// clearIgnoreCtrlC clears this process's "ignore Ctrl+C" flag, once:
// processes it starts inherit it (SSH sessions and Node's detached spawns
// set it), and a run that ignores Ctrl+C can't be asked to stop.
var clearIgnoreCtrlC = sync.OnceFunc(func() { _, _, _ = setConsoleCtrlHandler.Call(0, 0) })

// sendCtrlC is the __ctrlc helper: it joins the console of process pid
// and sends CTRL_C_EVENT to everything in it, having set itself to ignore
// the event first.
func sendCtrlC(pid int) int {
	_, _, _ = setConsoleCtrlHandler.Call(0, 1)
	if ok, _, _ := attachConsole.Call(uintptr(pid)); ok == 0 {
		return 1
	}
	if ok, _, _ := generateConsoleCtrlEvent.Call(windows.CTRL_C_EVENT, 0); ok == 0 {
		return 1
	}
	return 0
}
