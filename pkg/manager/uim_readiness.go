package manager

import (
	"context"
	"strings"

	"github.com/iniwex5/quectel-qmi-go/pkg/qmi"
)

type UIMReadinessReason string

const (
	UIMReadinessReady              UIMReadinessReason = "ready"
	UIMReadinessTransportFatal     UIMReadinessReason = "transport_fatal"
	UIMReadinessControlUnavailable UIMReadinessReason = "control_unavailable"
	UIMReadinessCardAbsent         UIMReadinessReason = "card_absent"
	UIMReadinessCardResetting      UIMReadinessReason = "card_resetting"
	UIMReadinessSIMBlocked         UIMReadinessReason = "sim_blocked"
	UIMReadinessIdentityEmpty      UIMReadinessReason = "identity_empty"
)

type UIMReadiness struct {
	TransportReady bool
	ControlReady   bool
	UIMReady       bool
	CardPresent    bool
	SIMStatus      qmi.SIMStatus
	ActiveSlot     uint8
	SlotKnown      bool
	SlotSource     string
	ICCID          string
	IMSI           string
	Reason         UIMReadinessReason
	Err            error
}

func isUIMReadinessTransportFatal(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "qmi-proxy") && strings.Contains(msg, "broken pipe") {
		return true
	}
	for _, fragment := range []string{
		"qmi: read failed",
		"qmi read failed",
		"failed to open qmi device",
		"no such device",
		"no such file or directory",
		"client closed",
		"read failed: eof",
		"read failed eof",
	} {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
}

func resolveActiveUIMSlot(info *qmi.UIMSlotStatus) (uint8, bool, string) {
	if info == nil {
		return 0, false, ""
	}
	for idx, slot := range info.Slots {
		if slot.PhysicalCardStatus != qmi.UIMPhysicalCardStatePresent {
			continue
		}
		if slot.PhysicalSlotStatus != qmi.UIMSlotStateActive {
			continue
		}
		if slot.LogicalSlot != 0 {
			return slot.LogicalSlot, true, "uim_slot_status"
		}
		return uint8(idx + 1), true, "uim_slot_status_index"
	}
	return 0, false, ""
}

func buildUIMReadiness(status qmi.SIMStatus, details *qmi.CardStatusDetails, slotInfo *qmi.UIMSlotStatus, ids DeviceIdentities, sourceErr error) UIMReadiness {
	slot, slotKnown, slotSource := resolveActiveUIMSlot(slotInfo)
	out := UIMReadiness{
		TransportReady: true,
		ControlReady:   true,
		SIMStatus:      status,
		ActiveSlot:     slot,
		SlotKnown:      slotKnown,
		SlotSource:     slotSource,
		ICCID:          strings.TrimSpace(ids.ICCID),
		IMSI:           strings.TrimSpace(ids.IMSI),
		Err:            sourceErr,
	}

	if sourceErr != nil {
		if isUIMReadinessTransportFatal(sourceErr) {
			out.TransportReady = false
			out.ControlReady = false
			out.Reason = UIMReadinessTransportFatal
			return out
		}
		out.ControlReady = false
		out.Reason = UIMReadinessControlUnavailable
		return out
	}

	out.CardPresent = status != qmi.SIMAbsent
	if details != nil {
		switch details.CardState {
		case 0x00:
			out.CardPresent = false
		case 0x01, 0x02:
			out.CardPresent = true
		}
	}
	if !out.CardPresent {
		out.Reason = UIMReadinessCardAbsent
		return out
	}
	if status == qmi.SIMBlocked || status == qmi.SIMPUKRequired || status == qmi.SIMNetworkLocked {
		out.Reason = UIMReadinessSIMBlocked
		return out
	}
	if status != qmi.SIMReady {
		out.Reason = UIMReadinessCardResetting
		return out
	}

	out.UIMReady = true
	if out.ICCID == "" && out.IMSI == "" {
		out.Reason = UIMReadinessIdentityEmpty
		return out
	}
	out.Reason = UIMReadinessReady
	return out
}

func (m *Manager) GetUIMReadiness(ctx context.Context) (UIMReadiness, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil {
		err := ErrServiceNotReady("UIM")
		return buildUIMReadiness(qmi.SIMNotReady, nil, nil, DeviceIdentities{}, err), err
	}

	m.mu.RLock()
	uim := m.uim
	m.mu.RUnlock()

	var details *qmi.CardStatusDetails
	status := qmi.SIMNotReady
	var firstErr error
	if uim == nil {
		firstErr = ErrServiceNotReady("UIM")
	} else {
		details, status, firstErr = uim.GetCardStatusDetails(ctx)
	}

	var slotInfo *qmi.UIMSlotStatus
	if firstErr == nil && uim != nil {
		slotInfo, firstErr = uim.GetSlotStatus(ctx)
	}

	ids, _ := m.GetCachedIdentities()
	if strings.TrimSpace(ids.ICCID) == "" {
		if iccid, err := m.GetICCID(ctx); err == nil {
			ids.ICCID = iccid
		}
	}
	if strings.TrimSpace(ids.IMSI) == "" {
		if imsi, err := m.GetIMSI(ctx); err == nil {
			ids.IMSI = imsi
		}
	}

	readiness := buildUIMReadiness(status, details, slotInfo, ids, firstErr)
	return readiness, firstErr
}
