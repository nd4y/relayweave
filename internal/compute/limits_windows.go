package compute

import (
	"golang.org/x/sys/windows"
	"runtime"
	"unsafe"
)

var kernel = windows.NewLazySystemDLL("kernel32.dll")
var computeJob uintptr

func processCPU() (int64, error) {
	var created, exit, k, u windows.Filetime
	if e := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exit, &k, &u); e != nil {
		return 0, e
	}
	ticks := (uint64(k.HighDateTime)<<32 | uint64(k.LowDateTime)) + (uint64(u.HighDateTime)<<32 | uint64(u.LowDateTime))
	return int64(ticks / 10000), nil
}

func EnforceLimits() error {
	if runtime.NumCPU() > 64 {
		return ErrInvalid
	}
	create := kernel.NewProc("CreateJobObjectW")
	set := kernel.NewProc("SetInformationJobObject")
	assign := kernel.NewProc("AssignProcessToJobObject")
	job, _, e := create.Call(0, 0)
	if job == 0 {
		return e
	}
	// Keep the handle open for this worker lifetime; kill-on-close covers only
	// this newly created job, never an unrelated Tdarr process tree.
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = 0x200 | 0x2000
	limits.JobMemoryLimit = 512 << 20
	ok, _, e := set.Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		windows.CloseHandle(windows.Handle(job))
		return e
	}
	rate := struct{ Flags, Rate uint32 }{5, uint32(10000 / max(1, runtime.NumCPU()))}
	ok, _, e = set.Call(job, 15, uintptr(unsafe.Pointer(&rate)), unsafe.Sizeof(rate))
	if ok == 0 {
		windows.CloseHandle(windows.Handle(job))
		return e
	}
	ok, _, e = assign.Call(job, uintptr(windows.CurrentProcess()))
	if ok == 0 {
		windows.CloseHandle(windows.Handle(job))
		return e
	}
	computeJob = job
	return nil
}
