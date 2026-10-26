package agent

import (
	"context"
	"net"
	"testing"
)

func TestRunChecksAggregates(t *testing.T) {
	pass := func(context.Context) CheckResult { return CheckResult{OK: true} }
	skip := func(context.Context) CheckResult { return CheckResult{Skipped: true} }
	fail := func(context.Context) CheckResult { return CheckResult{Detail: "nope"} }
	if results, ok := RunChecks(context.Background(), []Check{{"a", pass}, {"b", skip}}); !ok || results[1].Name != "b" {
		t.Fatalf("pass+skip = %v %+v", ok, results)
	}
	if _, ok := RunChecks(context.Background(), []Check{{"a", pass}, {"c", fail}}); ok {
		t.Fatal("a failing check must fail preflight")
	}
}

func TestSleepSettingParsing(t *testing.T) {
	disabled := []byte("System-wide power settings:\nCurrently in use:\n standby              0\n sleep                0 (sleep prevented by sharingd)\n displaysleep         10\n")
	if r := sleepSettingResult(disabled); !r.OK {
		t.Fatalf("disabled = %+v", r)
	}
	enabled := []byte("Currently in use:\n sleep                1\n displaysleep         10\n")
	if r := sleepSettingResult(enabled); r.OK {
		t.Fatalf("enabled = %+v", r)
	}
	if r := sleepSettingResult([]byte("nothing here")); r.OK {
		t.Fatal("missing setting must not pass")
	}
}

func TestTCPAndDiskChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if r := checkTCP(addr)(context.Background()); !r.OK {
		t.Fatalf("listening = %+v", r)
	}
	_ = ln.Close()
	if r := checkTCP(addr)(context.Background()); r.OK {
		t.Fatal("closed port must fail")
	}
	dir := t.TempDir()
	if r := checkDisk(dir, 0)(context.Background()); !r.OK {
		t.Fatalf("zero minimum = %+v", r)
	}
	if r := checkDisk(dir, 1<<62)(context.Background()); r.OK {
		t.Fatal("an impossible minimum must fail")
	}
	if r := checkBinary("fleet-no-such-binary")(context.Background()); r.OK {
		t.Fatal("missing binary must fail")
	}
}
