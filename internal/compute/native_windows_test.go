package compute

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"proxyplatform/internal/identity"
)

func nativeWindowsGate(t *testing.T) {
	t.Helper()
	if os.Getenv("RW_WINDOWS_NATIVE_GATE") != "1" {
		t.Skip("explicit short native Windows fixture required")
	}
}

func nativeWindowsEnv(mode string) []string {
	env := []string{"GOMAXPROCS=1", "GOMEMLIMIT=128MiB", "RW_WINDOWS_NATIVE_GATE=1", "RW_WINDOWS_NATIVE_MODE=" + mode}
	for _, name := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "RW_COMPUTE_WORKER_BINARY"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

type nativeJobReport struct {
	PID        int
	Flags      uint32
	Memory     uint64
	CPUFlags   uint32
	CPURate    uint32
	ChildOwned bool
	CPUUsedMS  int64
}

// Only this separately spawned helper joins the constrained job. The test
// runner, PowerShell, Codex and other workloads must never join it.
func TestWindowsNativeJobHelper(t *testing.T) {
	nativeWindowsGate(t)
	mode := os.Getenv("RW_WINDOWS_NATIVE_MODE")
	if mode == "" {
		t.Skip("child helper only")
	}
	if mode == "leaf" {
		time.Sleep(45 * time.Second)
		return
	}
	if mode != "supervisor" {
		t.Fatal("invalid helper mode")
	}
	if err := EnforceLimits(); err != nil {
		t.Fatal("enforce own child job", err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(windows.Handle(computeJob), 9, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		t.Fatal(err)
	}
	var rate struct{ Flags, Rate uint32 }
	if err := windows.QueryInformationJobObject(windows.Handle(computeJob), 15, uintptr(unsafe.Pointer(&rate)), uint32(unsafe.Sizeof(rate)), nil); err != nil {
		t.Fatal(err)
	}
	// Run the actual cross-built evaluator within the same inherited job.
	s := planFixture()
	hash, err := Hash(s)
	if err != nil {
		t.Fatal(err)
	}
	task := Task{Version: 1, OwnerEpoch: 1, ID: "task", Attempt: "attempt", Session: "session", Token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Snapshot: s, SnapshotDigest: hash, First: 0, Count: 64, CPUReservationMS: 500, WallBudgetMS: 5000}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	result, err := executeBinary(ctx, os.Getenv("RW_COMPUTE_WORKER_BINARY"), task)
	cancel()
	if err != nil || result.CPUUsedMS > 500 || result.Candidate.ID == 0 {
		t.Fatal("native evaluator", err, result.CPUUsedMS)
	}
	leaf := exec.Command(os.Args[0], "-test.run=^TestWindowsNativeJobHelper$")
	leaf.Env = nativeWindowsEnv("leaf")
	leaf.Stdout, leaf.Stderr = io.Discard, io.Discard
	if err = leaf.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leaf.Process.Kill(); _ = leaf.Wait() }()
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(leaf.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	var owned uint32
	ok, _, callErr := kernel.NewProc("IsProcessInJob").Call(uintptr(process), computeJob, uintptr(unsafe.Pointer(&owned)))
	if ok == 0 {
		t.Fatal(callErr)
	}
	report := nativeJobReport{PID: leaf.Process.Pid, Flags: limits.BasicLimitInformation.LimitFlags,
		Memory: uint64(limits.JobMemoryLimit), CPUFlags: rate.Flags, CPURate: rate.Rate, ChildOwned: owned != 0, CPUUsedMS: result.CPUUsedMS}
	if err = json.NewEncoder(os.Stdout).Encode(report); err != nil {
		t.Fatal(err)
	}
	time.Sleep(45 * time.Second)
}

func TestWindowsNativeJobLimitsAndTreeCleanup(t *testing.T) {
	nativeWindowsGate(t)
	if os.Getenv("RW_COMPUTE_WORKER_BINARY") == "" {
		t.Fatal("cross-built worker binary required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsNativeJobHelper$")
	cmd.Env = nativeWindowsEnv("supervisor")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	var report nativeJobReport
	if err = json.NewDecoder(io.LimitReader(output, 4096)).Decode(&report); err != nil {
		t.Fatal("job helper response", err, stderr.String())
	}
	if report.Flags&0x2200 != 0x2200 || report.Memory != 512<<20 || report.CPUFlags != 5 || report.CPURate != uint32(10000/max(1, runtime.NumCPU())) || !report.ChildOwned {
		t.Fatalf("kernel job limits: %+v", report)
	}
	leaf, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(report.PID))
	if err != nil {
		t.Fatal("open own leaf process", err)
	}
	defer windows.CloseHandle(leaf)
	defer windows.TerminateProcess(leaf, 1) // own helper only, in case the assertion fails
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	state, err := windows.WaitForSingleObject(leaf, 3000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatal("kill-on-close left own descendant running", state, err)
	}
	t.Logf("job_memory_bytes=%d job_cpu_rate=%d inherited_child=%v evaluator_cpu_ms=%d descendant_exited=true", report.Memory, report.CPURate, report.ChildOwned, report.CPUUsedMS)
}

func restrictNativeFixture(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal("restrict own fixture ACL", err)
	}
}

func nativeFixtureDACL(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

// Administrative provisioning runs on Linux. Keep the native test authority
// in memory and install only synthetic leaf bundles as that admin would; the
// Windows client path under test starts at RenewIdentity, not CA bootstrap.
func nativeFixtureAuthority(t *testing.T) *identity.Authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Native test authority"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &identity.Authority{Cert: parsed, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func installNativeFixtureBundle(t *testing.T, path string, bundle identity.Bundle) {
	t.Helper()
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsNativeRenewalPreservesACL(t *testing.T) {
	nativeWindowsGate(t)
	dir := t.TempDir()
	restrictNativeFixture(t, dir)
	a := nativeFixtureAuthority(t)
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), a.PEM, 0600); err != nil {
		t.Fatal(err)
	}
	serverBundle, err := a.IssueCompute("compute-dispatcher", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	installNativeFixtureBundle(t, filepath.Join(dir, "server.json"), serverBundle)
	fullExpiry := a.Cert.NotAfter
	a.Cert.NotAfter = time.Now().Add(24 * time.Hour)
	bundle, err := a.IssueCompute("native-peer", false, nil)
	a.Cert.NotAfter = fullExpiry
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "client.json")
	installNativeFixtureBundle(t, path, bundle)
	restrictNativeFixture(t, path)
	before := nativeFixtureDACL(t, path)
	serverTLS, err := identity.ServerTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compute/v1/renew" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "identity", 403)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4096))
		var request struct {
			CSR []byte `json:"csr"`
		}
		if e != nil || StrictDecode(raw, &request) != nil {
			http.Error(w, "request", 400)
			return
		}
		renewed, e := a.IssueCompute("native-peer", false, request.CSR)
		if e != nil {
			http.Error(w, "renew", 400)
			return
		}
		data, e := Canonical(map[string][]byte{"certificate": renewed.Certificate})
		if e != nil {
			http.Error(w, "encode", 500)
			return
		}
		_, _ = w.Write(data)
	}))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	secure, err := identity.ClientTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	secure.ServerName = "compute-dispatcher"
	client, err := NewClient(server.URL, secure)
	if err != nil {
		t.Fatal(err)
	}
	defer client.HTTP.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = client.RenewIdentity(ctx, dir); err != nil {
		t.Fatal("native renewal", err)
	}
	if after := nativeFixtureDACL(t, path); after != before {
		t.Fatal("renewal changed protected ACL")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var renewed identity.Bundle
	if err = json.Unmarshal(raw, &renewed); err != nil || !bytes.Equal(bundle.PrivateKey, renewed.PrivateKey) {
		t.Fatal("renewal changed private key", err)
	}
	cert, err := identity.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || time.Until(leaf.NotAfter) < 6*24*time.Hour {
		t.Fatal("renewal did not extend certificate", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, ".compute-renew-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("renewal temporary files retained", err)
	}
}

func TestWindowsNativeBootClockAndExpiry(t *testing.T) {
	nativeWindowsGate(t)
	first, err := bootElapsed()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	second, err := bootElapsed()
	if err != nil || second < first+20*time.Millisecond {
		t.Fatal("boot clock did not advance", err)
	}
	cache := NewCache(nil)
	s, err := cache.Capture(planFixture())
	if err != nil {
		t.Fatal(err)
	}
	captured := cache.items[s.Policy]
	captured.at -= 31 * time.Second
	cache.items[s.Policy] = captured
	if cache.Fresh(s.Policy, s.Sequence) {
		t.Fatal("expired native-clock observation accepted")
	}
	ctx, cancel, err := elapsedDeadline(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("native elapsed deadline not enforced")
	}
}
