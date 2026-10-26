package routing

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestDeliveredEWMAAndUnknownAggregateCap(t *testing.T) {
	var rate RateEstimator
	now := time.Now()
	if rate.Observe(now, 1000) != 1000 {
		t.Fatal("initial rate")
	}
	got := rate.Observe(now.Add(10*time.Second), 0)
	if math.Abs(got-1000/math.E) > 1e-6 {
		t.Fatal("time EWMA", got)
	}
	if rate.Observe(now.Add(400*time.Second), 2000) != 2000 {
		t.Fatal("stale rate did not reset")
	}
	plan := Build([]Input{{ID: "known", RTTMS: 200, Known: true}, {ID: "unknown-a", RTTMS: 1}, {ID: "unknown-b", RTTMS: 1}}, nil)
	unknown := uint32(0)
	for i := 0; i < plan.N; i++ {
		if plan.Shares[i].ID != "known" {
			unknown += plan.Shares[i].PPM
		}
	}
	if unknown > 10001 {
		t.Fatal("unknown groups multiplied exploration share", unknown)
	}
}

func TestScoreGolden(t *testing.T) {
	for _, tt := range []struct {
		v    Input
		want float64
	}{
		{Input{RTTMS: 100}, 100},
		{Input{RTTMS: 100, Active: 4, ErrorRate: .25, BytesPerSecond: 5_000_000}, 200},
		{Input{RTTMS: math.NaN(), ErrorRate: math.Inf(1)}, 216},
	} {
		if got := Score(tt.v); math.Abs(got-tt.want) > 1e-9 {
			t.Fatalf("score %v want %v", got, tt.want)
		}
	}
}

func TestEstimatorDecayResetAndConcurrent(t *testing.T) {
	var e Estimator
	now := time.Unix(1000, 0)
	e.Observe(now, 100, false)
	r, err, n := e.Read(now)
	if math.Abs(r-(200+(1-math.Exp(-1))*(-100))) > 1e-9 || n != 1 || err >= .02 {
		t.Fatalf("initial %v %v %v", r, err, n)
	}
	e.Observe(now.Add(30*time.Second), 0, true)
	_, err, _ = e.Read(now.Add(30 * time.Second))
	if err < .6 {
		t.Fatal("error outcome lost")
	}
	if r, _, n = e.Read(now.Add(400 * time.Second)); r != 200 || n != 0 {
		t.Fatal("stale state survived")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				e.Observe(time.Now(), 10, false)
				e.Read(time.Now())
			}
		})
	}
	wg.Wait()
}

func TestGroupingLanesColdStartAndCaps(t *testing.T) {
	inputs := []Input{{ID: "a", Group: "shared", Egress: "isp2", RTTMS: 100, Known: true}, {ID: "alias", Group: "shared", Egress: "isp2", RTTMS: 100, Known: true}, {ID: "b", Egress: "isp3", RTTMS: 100, Known: true}}
	p := Build(inputs, []Lane{{ID: "isp2", Weight: 8}, {ID: "isp3", Weight: 5}})
	if p.N != 2 || p.Shares[0].ID != "a" || p.Shares[0].PPM != 615385 || p.Shares[1].PPM != 384615 {
		t.Fatalf("alias inflated capacity %+v", p)
	}
	inputs = []Input{{ID: "a", RTTMS: 1}, {ID: "b", RTTMS: 100}}
	p = Build(inputs, nil)
	if p.N != 2 {
		t.Fatal("all unknown cold start failed")
	}
	inputs[1].Known = true
	p = Build(inputs, nil)
	if p.Shares[0].PPM > 10000 {
		t.Fatal("unknown cap bypass")
	}
	p = Build(inputs, []Lane{{ID: "disabled", Weight: 0}})
	if p.N != 0 {
		t.Fatal("disabled lane admitted")
	}
}

func TestFreshUtilizationAndActiveLaneLoad(t *testing.T) {
	in := []Input{{ID: "a", Egress: "a", RTTMS: 100}, {ID: "b", Egress: "b", RTTMS: 100}}
	p := Build(in, []Lane{{ID: "a", Weight: 1, Fresh: true, Utilization: 1}, {ID: "b", Weight: 1}})
	if p.Shares[0].PPM != 333333 {
		t.Fatal(p)
	}
	p = Build(in, []Lane{{ID: "a", Weight: 1, Fresh: false, Utilization: 1}, {ID: "b", Weight: 1}})
	if p.Shares[0].PPM != 500000 {
		t.Fatal("stale telemetry affected weights")
	}
}

func TestAliasCannotHideSharedActiveLoad(t *testing.T) {
	p := Build([]Input{{ID: "a", Group: "server", RTTMS: 100, Active: 12, Known: true}, {ID: "alias", Group: "server", RTTMS: 100, Known: true}, {ID: "b", Group: "independent", RTTMS: 100, Known: true}}, nil)
	if p.N != 2 || p.Shares[0].PPM != 200000 || p.Shares[1].PPM != 800000 {
		t.Fatalf("fresh alias hid server load: %+v", p)
	}
}

func TestPickFiltersAndSmoothing(t *testing.T) {
	a := Build([]Input{{ID: "a", RTTMS: 100}, {ID: "b", RTTMS: 100}}, nil)
	b := Build([]Input{{ID: "a", RTTMS: 1}, {ID: "b", RTTMS: 100}}, nil)
	b = Smooth(a, b, 100000)
	if b.Shares[0].PPM > 600001 || b.Shares[0].PPM+b.Shares[1].PPM != Million {
		t.Fatal("hysteresis", b)
	}
	for i := uint64(0); i < Million; i += 7919 {
		id, p := b.Pick(i, func(id string) bool { return id == "b" })
		if id != "b" || p != 1 {
			t.Fatal("revoked ID selected")
		}
	}
	if id, _ := b.Pick(0, func(string) bool { return false }); id != "" {
		t.Fatal("empty eligible set")
	}
}

func BenchmarkCachedPick(b *testing.B) {
	in := make([]Input, 16)
	for i := range in {
		in[i] = Input{ID: string(rune('a' + i)), RTTMS: float64(i + 1)}
	}
	p := Build(in, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Pick(uint64(i)*7919, func(string) bool { return true })
	}
}

func BenchmarkBuild256(b *testing.B) {
	in := make([]Input, 256)
	for i := range in {
		in[i] = Input{ID: string(rune(1000 + i)), RTTMS: float64(i + 1)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Build(in, nil)
	}
}
