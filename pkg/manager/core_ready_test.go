package manager

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

func TestDoRecoverFromModemResetResetsSnapshotImmediately(t *testing.T) {
	m := newRecoveryTestManager()
	m.snapshot.UpdateIdentities(DeviceIdentities{ICCID: "old-iccid", IMSI: "old-imsi"})
	m.openClientAndAllocateServicesHook = func() error { return nil }
	m.checkSIMHook = func() error { return nil }
	m.modemResetQuietWindow = 5 * time.Millisecond
	m.getICCIDStrictHook = func(ctx context.Context) (string, error) { return "new-iccid", nil }

	if ok := m.doRecoverFromModemReset(); !ok {
		t.Fatal("expected recover to succeed")
	}

	ids, _ := m.snapshot.Identities()
	if ids.ICCID != "" || ids.IMSI != "" {
		t.Fatalf("expected snapshot SIM identities reset, got iccid=%q imsi=%q", ids.ICCID, ids.IMSI)
	}
	if !m.IsCoreReady() {
		t.Fatal("expected coreReady=true after convergence")
	}
}

func TestStartCorePowerCyclesSIMBeforeCheckSIMWhenEnabled(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.PowerCycleSIMOnStartCore = true
	m.cfg = normalizeConfig(m.cfg)

	var events []string
	m.openClientAndAllocateServicesHook = func() error {
		events = append(events, "open")
		return nil
	}
	m.startupSIMPowerCycleHook = func(ctx context.Context) error {
		events = append(events, "power_cycle")
		return nil
	}
	m.checkSIMHook = func() error {
		events = append(events, "check_sim")
		return nil
	}

	if err := m.StartCore(); err != nil {
		t.Fatalf("StartCore() error = %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	want := []string{"open", "power_cycle", "check_sim"}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events=%v want %v", events, want)
	}
}

func TestStartCoreContinuesWhenStartupSIMPowerCycleUnsupported(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.PowerCycleSIMOnStartCore = true
	m.cfg = normalizeConfig(m.cfg)
	m.openClientAndAllocateServicesHook = func() error { return nil }
	m.startupSIMPowerCycleHook = func(ctx context.Context) error {
		return &qmi.NotSupportedError{Operation: "power off SIM"}
	}

	checkCalls := 0
	m.checkSIMHook = func() error {
		checkCalls++
		return nil
	}

	if err := m.StartCore(); err != nil {
		t.Fatalf("StartCore() error = %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	if checkCalls != 1 {
		t.Fatalf("checkSIM calls=%d want 1", checkCalls)
	}
}

func TestStartCoreFailsWhenStartupSIMPowerCycleFails(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.PowerCycleSIMOnStartCore = true
	m.cfg = normalizeConfig(m.cfg)
	m.openClientAndAllocateServicesHook = func() error { return nil }
	m.startupSIMPowerCycleHook = func(ctx context.Context) error {
		return fmt.Errorf("power on SIM after startup power-off: modem rejected command")
	}
	m.checkSIMHook = func() error {
		t.Fatal("checkSIM should not run after fatal startup SIM power-cycle failure")
		return nil
	}

	err := m.StartCore()
	if err == nil {
		t.Fatal("StartCore() error = nil, want startup SIM power-cycle failure")
	}
	if !strings.Contains(err.Error(), "startup SIM power cycle failed") {
		t.Fatalf("StartCore() error = %v, want startup SIM power-cycle context", err)
	}
	if m.IsCoreReady() {
		t.Fatal("coreReady should stay false after startup SIM power-cycle failure")
	}
}

func TestDoRecoverFromModemResetIdentityGateBlocksCoreReady(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.Timeouts.SIMCheck = 80 * time.Millisecond
	m.openClientAndAllocateServicesHook = func() error { return nil }
	m.checkSIMHook = func() error { return nil }
	m.modemResetQuietWindow = 5 * time.Millisecond
	m.getICCIDStrictHook = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("uim not ready")
	}
	m.getIMSIStrictHook = func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("uim not ready")
	}

	if ok := m.doRecoverFromModemReset(); ok {
		t.Fatal("expected recover to fail when identities are unreadable")
	}
	if m.IsCoreReady() {
		t.Fatal("coreReady should stay false while identity gate is unsatisfied")
	}

	m.mu.RLock()
	stage := m.coreReadyStage
	m.mu.RUnlock()
	if stage != "recover_wait_identity" {
		t.Fatalf("expected stage recover_wait_identity, got %q", stage)
	}
}

func TestDoRecoverFromModemResetPendingStormBlocksCoreReady(t *testing.T) {
	m := newRecoveryTestManager()
	m.cfg.Timeouts.SIMCheck = 100 * time.Millisecond
	m.openClientAndAllocateServicesHook = func() error { return nil }
	m.checkSIMHook = func() error { return nil }
	m.modemResetQuietWindow = 100 * time.Millisecond
	m.modemResetPending = true

	if ok := m.doRecoverFromModemReset(); ok {
		t.Fatal("expected recover to fail when reset storm is still pending")
	}
	if m.IsCoreReady() {
		t.Fatal("coreReady should stay false during reset storm")
	}

	m.mu.RLock()
	stage := m.coreReadyStage
	m.mu.RUnlock()
	if stage != "recover_wait_reset_quiet" {
		t.Fatalf("expected stage recover_wait_reset_quiet, got %q", stage)
	}
}

func TestWaitCoreReadyTimeoutIncludesConvergenceContext(t *testing.T) {
	m := newRecoveryTestManager()
	m.mu.Lock()
	m.markCoreNotReadyLocked("recover_wait_identity", fmt.Errorf("uim not ready"))
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	err := m.WaitCoreReady(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "stage=recover_wait_identity") {
		t.Fatalf("expected stage in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "uim not ready") {
		t.Fatalf("expected last_err in error, got: %v", err)
	}
}

func TestStrictLiveIdentityBypassesSnapshot(t *testing.T) {
	m := newRecoveryTestManager()
	m.snapshot.UpdateIdentities(DeviceIdentities{ICCID: "cached-iccid", IMSI: "cached-imsi"})
	m.getICCIDStrictHook = func(ctx context.Context) (string, error) { return "live-iccid", nil }
	m.getIMSIStrictHook = func(ctx context.Context) (string, error) { return "live-imsi", nil }

	iccid, err := m.GetICCIDStrictLive(context.Background())
	if err != nil || iccid != "live-iccid" {
		t.Fatalf("GetICCIDStrictLive unexpected result: iccid=%q err=%v", iccid, err)
	}
	imsi, err := m.GetIMSIStrictLive(context.Background())
	if err != nil || imsi != "live-imsi" {
		t.Fatalf("GetIMSIStrictLive unexpected result: imsi=%q err=%v", imsi, err)
	}

	iccidViaDefault, err := m.GetICCID(context.Background())
	if err != nil || iccidViaDefault != "live-iccid" {
		t.Fatalf("GetICCID should follow strict-live path: iccid=%q err=%v", iccidViaDefault, err)
	}
	imsiViaDefault, err := m.GetIMSI(context.Background())
	if err != nil || imsiViaDefault != "live-imsi" {
		t.Fatalf("GetIMSI should follow strict-live path: imsi=%q err=%v", imsiViaDefault, err)
	}
}

func TestSnapshotIdentityGetterReturnsCopyForPointerFields(t *testing.T) {
	m := newRecoveryTestManager()
	mode := qmi.ModeOnline
	inserted := true
	m.snapshot.UpdateIdentities(DeviceIdentities{IMEI: "imei", OperatingMode: &mode, SimInserted: &inserted})

	ids, _ := m.snapshot.Identities()
	if ids.OperatingMode == nil || ids.SimInserted == nil {
		t.Fatal("expected pointer fields in identities")
	}
	*ids.OperatingMode = qmi.ModeOffline
	*ids.SimInserted = false

	again, _ := m.snapshot.Identities()
	if again.OperatingMode == nil || *again.OperatingMode != qmi.ModeOnline {
		t.Fatalf("operating mode should be isolated copy, got %+v", again.OperatingMode)
	}
	if again.SimInserted == nil || *again.SimInserted != true {
		t.Fatalf("simInserted should be isolated copy, got %+v", again.SimInserted)
	}
}

func TestIdentityGenerationRejectsStaleWrite(t *testing.T) {
	s := &DeviceSnapshot{}
	gen := s.IdentityGeneration()
	s.ResetIdentities(false)
	ok := s.UpdateIdentitiesIfGeneration(DeviceIdentities{ICCID: "stale", IMSI: "stale"}, gen)
	if ok {
		t.Fatal("expected stale generation write to be rejected")
	}
	ids, _ := s.Identities()
	if ids.ICCID != "" || ids.IMSI != "" {
		t.Fatalf("stale write should not update identities, got iccid=%q imsi=%q", ids.ICCID, ids.IMSI)
	}
}

func TestIdentityReadinessSemantics(t *testing.T) {
	s := &DeviceSnapshot{}
	s.UpdateIdentities(DeviceIdentities{IMEI: "imei-only"})
	staticReady, simReady := s.IdentityReadiness()
	if !staticReady || simReady {
		t.Fatalf("unexpected readiness after static update: static=%v sim=%v", staticReady, simReady)
	}

	s.UpdateIdentities(DeviceIdentities{ICCID: "iccid", IMSI: "imsi"})
	staticReady, simReady = s.IdentityReadiness()
	if !staticReady || !simReady {
		t.Fatalf("unexpected readiness after sim update: static=%v sim=%v", staticReady, simReady)
	}

	s.ResetIdentities(false)
	staticReady, simReady = s.IdentityReadiness()
	if !staticReady || simReady {
		t.Fatalf("unexpected readiness after sim reset: static=%v sim=%v", staticReady, simReady)
	}

	s.ResetIdentities(true)
	staticReady, simReady = s.IdentityReadiness()
	if staticReady || simReady {
		t.Fatalf("unexpected readiness after full reset: static=%v sim=%v", staticReady, simReady)
	}
}
