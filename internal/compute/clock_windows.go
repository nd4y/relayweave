package compute

import (
	"golang.org/x/sys/windows"
	"time"
)

var tick64 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")

func bootElapsed() (time.Duration, error) {
	v, _, _ := tick64.Call()
	return time.Duration(v) * time.Millisecond, nil
}
